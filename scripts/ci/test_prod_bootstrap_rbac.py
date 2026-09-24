#!/usr/bin/env python3
"""The production deploy identity's RBAC, as rendered, and nothing more (#1000).

infra/k8s/bootstrap/nchat-prod is applied by a cluster administrator, never by
a release. These checks render it the way it is applied and hold the Role to
the production baseline plus exactly one rule: get/patch/update on the one
ConfigMap named nchat-release-state.

Two independent checks, because each catches what the other cannot:

  * the rules, compared as a set with the baseline, reject any extra grant in
    any rule and in any order;
  * the effective-permission matrix evaluates requests the way the RBAC
    authorizer does -- resourceNames never match a request without a name,
    such as create or list -- so the answer to "can it create a ConfigMap" is
    computed, not assumed from the text of one rule.

PyYAML is already a dependency of the CD workflow checks.
"""

import copy
import os
import pathlib
import shutil
import subprocess
import unittest

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]
BOOTSTRAP = ROOT / "infra/k8s/bootstrap/nchat-prod"
RELEASE_UNITS = [".", "shared", "slots/blue", "slots/green", "migrations", "stateful"]
NAME = "nchat-prod-deployer"
NAMESPACE = "nchat-prod"
STATE = "nchat-release-state"
READ = ["get", "list", "watch"]
WRITE = READ + ["create", "patch", "update"]
SERVICES = [
    "admin-service", "auth-service", "chat-service", "document-converter",
    "file-service", "media-service", "nchat-admin-web", "nchat-web",
    "notification-service", "search-service",
]
# Role/nchat-prod-deployer as it exists in production before #1000, as the
# operator read it from the cluster. Not inferred from the scripts: this is the
# grant set the change must preserve exactly.
BASELINE = [
    ("", ["pods", "services", "configmaps", "events"], READ, []),
    ("events.k8s.io", ["events"], READ, []),
    ("apps", ["deployments"], WRITE, []),
    ("apps", ["replicasets"], READ, []),
    ("batch", ["jobs"], WRITE, []),
    ("", ["services"], ["patch"], SERVICES),
    ("", ["services"], WRITE, []),
    ("policy", ["poddisruptionbudgets"], WRITE, []),
    ("networking.k8s.io", ["networkpolicies"], WRITE, []),
    ("", ["services/proxy"], ["get"], []),
]
STATE_RULE = {
    "apiGroups": [""], "resources": ["configmaps"],
    "resourceNames": [STATE], "verbs": ["get", "patch", "update"],
}
# (verb, name, allowed) on core/configmaps. name None is a request without a
# resource name: create, list, watch of the collection.
CONFIGMAP_MATRIX = [
    ("get", STATE, True), ("patch", STATE, True), ("update", STATE, True),
    ("create", None, False), ("create", STATE, False),
    ("delete", STATE, False), ("delete", "nchat-config", False),
    ("deletecollection", None, False),
    ("patch", "nchat-config", False), ("update", "nchat-config", False),
    ("patch", "any-other", False), ("update", "any-other", False),
    ("get", "nchat-config", True), ("list", None, True),
]


def require(condition, message):
    """An explicit failure, unlike `assert`, which `python -O` removes."""
    if not condition:
        raise AssertionError(message)


def normalized(rule):
    return tuple(sorted((key, tuple(sorted(value))) for key, value in rule.items()))


def expected_rules():
    rules = []
    for group, resources, verbs, names in BASELINE:
        rule = {"apiGroups": [group], "resources": resources, "verbs": verbs}
        if names:
            rule["resourceNames"] = names
        rules.append(rule)
    return rules + [STATE_RULE]


def rule_matches(rule, group, resource, verb, name):
    """One PolicyRule against one request, as the RBAC authorizer decides it."""
    if not {group, "*"} & set(rule.get("apiGroups", [])):
        return False
    if not {resource, "*"} & set(rule.get("resources", [])):
        return False
    if not {verb, "*"} & set(rule.get("verbs", [])):
        return False
    names = rule.get("resourceNames") or []
    return not names or (name is not None and name in names)


def allows(role, group, resource, verb, name=None):
    return any(rule_matches(rule, group, resource, verb, name) for rule in role["rules"])


def validate_objects(objects):
    kinds = sorted(obj["kind"] for obj in objects)
    require(kinds == ["Role", "RoleBinding", "ServiceAccount"],
            f"bootstrap must render exactly a ServiceAccount, a Role and a RoleBinding, got {kinds}")
    for obj in objects:
        require(obj["metadata"]["name"] == NAME, f"unexpected object {obj['metadata']['name']}")
        require(obj["metadata"].get("namespace") == NAMESPACE,
                f"{obj['kind']} is not scoped to {NAMESPACE}")


def validate_identity(by_kind):
    require(by_kind["ServiceAccount"].get("automountServiceAccountToken") is False,
            "the deployer ServiceAccount must not automount a token")
    binding = by_kind["RoleBinding"]
    require(binding["roleRef"] == {
        "apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": NAME,
    }, "the RoleBinding must bind the namespaced Role, never a ClusterRole")
    require(binding["subjects"] == [
        {"kind": "ServiceAccount", "name": NAME, "namespace": NAMESPACE},
    ], "the RoleBinding must bind the deployer ServiceAccount and nothing else")


def validate_rules(role):
    require(sorted(map(normalized, role["rules"])) == sorted(map(normalized, expected_rules())),
            "only the named lifecycle rule may differ from the production baseline")
    for verb, name, allowed in CONFIGMAP_MATRIX:
        require(allows(role, "", "configmaps", verb, name) is allowed,
                f"configmaps {verb} {name or '(collection)'} must be "
                f"{'allowed' if allowed else 'refused'}")
    for resource in ["secrets", "nodes", "namespaces", "roles", "rolebindings"]:
        for verb in READ + ["create", "patch", "update", "delete", "escalate", "bind"]:
            require(not allows(role, "", resource, verb) and
                    not allows(role, "rbac.authorization.k8s.io", resource, verb),
                    f"the deployer must not {verb} {resource}")


def validate(objects):
    validate_objects(objects)
    by_kind = {obj["kind"]: obj for obj in objects}
    validate_identity(by_kind)
    validate_rules(by_kind["Role"])


def render(path):
    if shutil.which("kustomize"):
        command = ["kustomize", "build", str(path)]
    else:
        command = ["kubectl", "kustomize", str(path)]
    # Rendering is local: no kubeconfig, so nothing can reach a cluster.
    output = subprocess.check_output(command, env={**os.environ, "KUBECONFIG": "/dev/null"})
    return [obj for obj in yaml.safe_load_all(output) if obj]


def role_of(objects):
    return next(obj for obj in objects if obj["kind"] == "Role")


class BootstrapRBAC(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.objects = render(BOOTSTRAP)

    def test_rendered_contract(self):
        validate(self.objects)

    def test_rule_order_is_irrelevant(self):
        objects = copy.deepcopy(self.objects)
        role = role_of(objects)
        role["rules"].reverse()
        for rule in role["rules"]:
            for values in rule.values():
                values.reverse()
        validate(objects)

    def test_rejects_any_additional_grant(self):
        forbidden = [
            ("", "configmaps", ["create"], []),
            ("", "configmaps", ["create"], [STATE]),
            ("", "configmaps", ["delete"], [STATE]),
            ("", "configmaps", ["patch", "update"], []),
            ("", "configmaps", ["patch"], ["nchat-config"]),
            ("", "configmaps", ["update"], ["arbitrary"]),
            ("", "configmaps", ["*"], []),
            ("", "secrets", READ, []),
            ("", "nodes", READ, []),
            ("", "*", READ, []),
            ("*", "*", ["*"], []),
            ("rbac.authorization.k8s.io", "roles", ["escalate", "bind"], []),
        ]
        for group, resource, verbs, names in forbidden:
            with self.subTest(group=group, resource=resource, verbs=verbs, names=names):
                objects = copy.deepcopy(self.objects)
                rule = {"apiGroups": [group], "resources": [resource], "verbs": verbs}
                if names:
                    rule["resourceNames"] = names
                role_of(objects)["rules"].append(rule)
                with self.assertRaises(AssertionError):
                    validate(objects)

    def test_rejects_widening_the_lifecycle_rule_in_place(self):
        for field, value in [("verbs", ["get", "patch", "update", "create"]),
                             ("verbs", ["get", "patch", "update", "delete"]),
                             ("resourceNames", [STATE, "nchat-config"]),
                             ("resourceNames", [])]:
            with self.subTest(field=field, value=value):
                objects = copy.deepcopy(self.objects)
                rule = next(r for r in role_of(objects)["rules"]
                            if r.get("resourceNames") == [STATE])
                rule[field] = value
                with self.assertRaises(AssertionError):
                    validate(objects)

    def test_rejects_cluster_scope_and_other_namespaces(self):
        mutations = [
            lambda objs: role_of(objs).update(kind="ClusterRole"),
            lambda objs: role_of(objs)["metadata"].update(namespace="other"),
            lambda objs: next(o for o in objs if o["kind"] == "RoleBinding")["roleRef"]
            .update(kind="ClusterRole"),
        ]
        for mutate in mutations:
            objects = copy.deepcopy(self.objects)
            mutate(objects)
            with self.assertRaises(AssertionError):
                validate(objects)

    def test_bootstrap_is_not_release_owned(self):
        # Every unit a release (or the stateful apply) renders. The lifecycle
        # record and the deployer's own RBAC must be in none of them: a release
        # re-applies what it renders, and re-applying an empty record would
        # reset the lifecycle.
        for unit in RELEASE_UNITS:
            with self.subTest(unit=unit):
                for obj in render(ROOT / "infra/k8s/overlays/k3s-prod" / unit):
                    self.assertNotIn(obj["metadata"]["name"], [NAME, STATE])
                    self.assertNotIn(obj["kind"], ["Role", "RoleBinding", "ClusterRole",
                                                   "ClusterRoleBinding"])


if __name__ == "__main__":
    unittest.main()
