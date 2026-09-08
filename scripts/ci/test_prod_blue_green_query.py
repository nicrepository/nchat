#!/usr/bin/env python3
"""Unit tests for the production Blue/Green manifest reader (issue #626).

The reader was previously exercised only through prod-blue-green-check.sh, so a
change that made it return the empty string for everything would have satisfied
every negative assertion in that script and silently stopped checking anything.
These tests pin the reader's own behaviour, which is what makes reducing its
complexity safe.

Standard library only, run directly: python3 scripts/ci/test_prod_blue_green_query.py
"""

from __future__ import annotations

import importlib.util
import io
import pathlib
import sys
import tempfile
import unittest
from contextlib import redirect_stdout

MODULE_PATH = pathlib.Path(__file__).resolve().parent / "prod_blue_green_query.py"
_spec = importlib.util.spec_from_file_location("prod_blue_green_query", MODULE_PATH)
query = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(query)


DEPLOYMENT = """apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    nchat.io/release-sha: abc123
  name: nchat-web-blue
  namespace: nchat-prod
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/component: web
      nchat.io/release-slot: blue
  template:
    spec:
      containers:
      - env:
        - name: NCHAT_WEB_LIVEKIT_CONNECT_SRC
          valueFrom:
            configMapKeyRef:
              key: NCHAT_WEB_LIVEKIT_CONNECT_SRC
              name: nchat-config
        image: ghcr.io/nicrepository/nchat/web@sha256:aaaa
        livenessProbe:
          httpGet:
            path: /healthz
        name: web
        readinessProbe:
          httpGet:
            path: /readyz
      imagePullSecrets:
      - name: ghcr-pull
"""

INGRESS = """apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: nchat-prod-preview-allowlist@kubernetescrd
  name: nchat-prod
  namespace: nchat-prod
spec:
  rules:
  - host: nchat.example.com
    http:
      paths:
      - backend:
          service:
            name: auth-service
            port:
              name: http
        path: /api/auth
      - backend:
          service:
            name: nchat-web
            port:
              name: http
        path: /
"""

MIDDLEWARE = """apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: preview-allowlist
  namespace: nchat-prod
spec:
  ipAllowList:
    ipStrategy:
      depth: 1
    sourceRange:
    - 198.51.100.0/24
    - 203.0.113.0/24
"""

CONFIGMAP = """apiVersion: v1
kind: ConfigMap
data:
  FILE_UPLOADS_ENABLED: "true"
  NCHAT_WEB_LIVEKIT_CONNECT_SRC: wss://livekit.example.com
metadata:
  name: nchat-config
  namespace: nchat-prod
"""


def document(text: str) -> "query.Document":
    return query.Document(text.rstrip("\n"))


class BlockReaderTests(unittest.TestCase):
    def test_finds_a_nested_block(self):
        labels = document(DEPLOYMENT).block("spec", "selector", "matchLabels")
        self.assertEqual(
            [line.strip() for line in labels],
            ["app.kubernetes.io/component: web", "nchat.io/release-slot: blue"],
        )

    def test_absent_block_is_empty_not_an_error(self):
        self.assertEqual(document(DEPLOYMENT).block("spec", "nosuchkey"), [])

    def test_absent_intermediate_key_is_empty(self):
        self.assertEqual(document(DEPLOYMENT).block("nosuch", "matchLabels"), [])

    def test_a_sibling_key_ends_the_block(self):
        # metadata.annotations must not swallow metadata.name.
        annotations = document(DEPLOYMENT).block("metadata", "annotations")
        self.assertEqual([line.strip() for line in annotations], ["nchat.io/release-sha: abc123"])

    def test_a_sequence_level_with_its_key_stays_inside(self):
        # "sourceRange:" followed by "- ..." at the same column is valid YAML.
        values = document(MIDDLEWARE).block("spec", "ipAllowList", "sourceRange")
        self.assertEqual(len(values), 2)

    def test_end_of_document_ends_the_block(self):
        pulls = document(DEPLOYMENT).block("spec", "template", "spec")
        self.assertTrue(pulls)
        self.assertTrue(any("ghcr-pull" in line for line in pulls))


class IngressBackendTests(unittest.TestCase):
    def test_collects_every_backend_service(self):
        self.assertEqual(
            query._ingress_backends([document(INGRESS)]),
            ["nchat-prod|auth-service", "nchat-prod|nchat-web"],
        )

    def test_port_names_are_not_reported_as_services(self):
        for entry in query._ingress_backends([document(INGRESS)]):
            self.assertNotEqual(entry.split("|", 1)[1], "http")

    def test_a_document_with_no_backend_yields_nothing(self):
        self.assertEqual(query._backend_service_names(document(MIDDLEWARE)), [])

    def test_non_ingress_documents_are_ignored(self):
        self.assertEqual(query._ingress_backends([document(DEPLOYMENT)]), [])


class SecretRefTests(unittest.TestCase):
    def test_image_pull_secrets_are_collected(self):
        self.assertIn("ghcr-pull", query._secret_refs([document(DEPLOYMENT)]))

    def test_config_map_key_refs_are_not_secrets(self):
        self.assertNotIn("nchat-config", query._secret_refs([document(DEPLOYMENT)]))

    def test_secret_key_ref_is_collected(self):
        text = DEPLOYMENT.replace(
            "          valueFrom:\n            configMapKeyRef:",
            "          valueFrom:\n            secretKeyRef:",
        )
        self.assertIn("nchat-config", query._secret_refs([document(text)]))

    def test_env_from_secret_ref_is_collected(self):
        text = DEPLOYMENT + "        envFrom:\n        - secretRef:\n            name: nchat-secrets\n"
        self.assertIn("nchat-secrets", query._secret_refs([document(text)]))

    def test_a_document_with_no_secret_yields_nothing(self):
        self.assertEqual(query._secret_refs([document(MIDDLEWARE)]), [])


# A production stateful PersistentVolume, whose three safety properties -- the
# path it points at, that it is retained rather than collected, and the single
# node it may bind on -- are the ones prod-stateful-check reads back.
LOCAL_VOLUME = """apiVersion: v1
kind: PersistentVolume
metadata:
  name: nchat-prod-postgres
spec:
  capacity:
    storage: 30Gi
  persistentVolumeReclaimPolicy: Retain
  storageClassName: local-hdd-geral
  local:
    path: /mnt/hdd-geral/k3s/nchat-prod/postgres
  nodeAffinity:
    required:
      nodeSelectorTerms:
      - matchExpressions:
        - key: kubernetes.io/hostname
          operator: In
          values:
          - srv-apps-01
"""


# kustomize emits a rule's keys alphabetically, so `backend` precedes `path`.
# A reader that took the nearest preceding path would shift every backend onto
# the previous rule's path, which is exactly the mistake this fixture pins.
class ConfigMapEnvFromTests(unittest.TestCase):
    ENVFROM = """apiVersion: apps/v1
kind: Deployment
metadata:
  name: media-service-blue
spec:
  template:
    spec:
      containers:
      - name: media-service
        envFrom:
        - configMapRef:
            name: nchat-config
        - secretRef:
            name: nchat-secrets
        env:
        - name: LIVEKIT_TOKEN_TTL_SECONDS
          valueFrom:
            configMapKeyRef:
              name: nchat-config
              key: LIVEKIT_TTL_SECONDS
"""

    def test_reports_the_whole_config_map_the_workload_inherits(self):
        self.assertEqual(
            query.document_field(document(self.ENVFROM), "configmap-envfrom"), "nchat-config")

    def test_a_single_key_reference_is_not_an_envfrom(self):
        text = self.ENVFROM.replace(
            "        envFrom:\n        - configMapRef:\n            name: nchat-config\n", "")
        self.assertEqual(query.document_field(document(text), "configmap-envfrom"), "")

    def test_app_env_is_inherited_when_no_container_override_exists(self):
        self.assertEqual(query.document_field(document(self.ENVFROM), "app-env"), "")

    def test_app_env_container_override_is_reported(self):
        text = self.ENVFROM.replace(
            "        - name: LIVEKIT_TOKEN_TTL_SECONDS",
            "        - name: APP_ENV\n          value: blue\n        - name: LIVEKIT_TOKEN_TTL_SECONDS",
        )
        self.assertEqual(query.document_field(document(text), "app-env"), "blue")


class LocalVolumeFieldTests(unittest.TestCase):
    def test_dotted_spec_path(self):
        self.assertEqual(
            query.document_field(document(LOCAL_VOLUME), "spec.persistentVolumeReclaimPolicy"),
            "Retain",
        )

    def test_dotted_spec_path_reaches_a_nested_mapping(self):
        self.assertEqual(
            query.document_field(document(LOCAL_VOLUME), "spec.local.path"),
            "/mnt/hdd-geral/k3s/nchat-prod/postgres",
        )

    def test_absent_spec_path_is_empty(self):
        self.assertEqual(query.document_field(document(LOCAL_VOLUME), "spec.nope"), "")

    def test_node_hosts(self):
        self.assertEqual(query.document_field(document(LOCAL_VOLUME), "node-hosts"), "srv-apps-01")

    def test_node_hosts_collects_every_value(self):
        text = LOCAL_VOLUME.replace("          - srv-apps-01\n", "          - srv-apps-01\n          - other\n")
        self.assertEqual(query.document_field(document(text), "node-hosts"), "srv-apps-01 other")

    def test_a_document_with_no_affinity_reports_no_host(self):
        self.assertEqual(query.document_field(document(DEPLOYMENT), "node-hosts"), "")


class FieldQueryTests(unittest.TestCase):
    def test_release_sha(self):
        self.assertEqual(query.document_field(document(DEPLOYMENT), "release-sha"), "abc123")

    def test_probes(self):
        self.assertEqual(query.document_field(document(DEPLOYMENT), "probes"), "readiness liveness")

    def test_livekit_env_read_from_the_config_map(self):
        self.assertEqual(query.document_field(document(DEPLOYMENT), "livekit-env"), "configMapKeyRef")

    def test_livekit_env_pinned_to_a_literal_is_reported_as_such(self):
        text = DEPLOYMENT.replace(
            "          valueFrom:\n            configMapKeyRef:\n"
            "              key: NCHAT_WEB_LIVEKIT_CONNECT_SRC\n              name: nchat-config",
            '          value: ""',
        )
        self.assertEqual(query.document_field(document(text), "livekit-env"), "literal")

    def test_selector_slot(self):
        text = MIDDLEWARE.replace(
            "spec:\n  ipAllowList:",
            "spec:\n  selector:\n    nchat.io/release-slot: green\n  ipAllowList:",
        )
        self.assertEqual(query.document_field(document(text), "selector-slot"), "green")

    def test_source_range_joins_every_entry(self):
        self.assertEqual(
            query.document_field(document(MIDDLEWARE), "source-range"),
            "198.51.100.0/24 203.0.113.0/24",
        )

    def test_ip_strategy_depth(self):
        self.assertEqual(
            query.document_field(document(MIDDLEWARE), "ip-strategy-depth"), "1"
        )

    def test_absent_ip_strategy_is_empty_not_a_default(self):
        # An allowlist with no ipStrategy matches RemoteAddr, which behind a
        # proxy is the proxy. The reader must say "absent", never "1".
        text = MIDDLEWARE.replace("    ipStrategy:\n      depth: 1\n", "")
        self.assertEqual(query.document_field(document(text), "ip-strategy-depth"), "")

    def test_data_field(self):
        self.assertEqual(
            query.document_field(document(CONFIGMAP), "data.FILE_UPLOADS_ENABLED"), "true"
        )

    def test_absent_data_field_is_empty(self):
        self.assertEqual(query.document_field(document(CONFIGMAP), "data.NOT_SET"), "")

    def test_unknown_field_is_empty(self):
        self.assertEqual(query.document_field(document(DEPLOYMENT), "no-such-field"), "")


class RunDispatchTests(unittest.TestCase):
    def setUp(self):
        handle = tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False)
        handle.write("\n---\n".join([DEPLOYMENT.rstrip("\n"), INGRESS.rstrip("\n"),
                                     MIDDLEWARE.rstrip("\n"), CONFIGMAP.rstrip("\n")]))
        handle.close()
        self.manifest = handle.name
        self.addCleanup(lambda: pathlib.Path(self.manifest).unlink(missing_ok=True))

    def run_query(self, text: str) -> tuple[int, str]:
        buffer = io.StringIO()
        with redirect_stdout(buffer):
            status = query.run(self.manifest, text)
        return status, buffer.getvalue().strip()

    def test_known_collection_query(self):
        status, output = self.run_query("Deployment|name")
        self.assertEqual((status, output), (0, "nchat-web-blue"))

    def test_known_document_field_query(self):
        self.assertEqual(self.run_query("Deployment|nchat-web-blue|release-sha"), (0, "abc123"))

    def test_global_query(self):
        status, output = self.run_query("namespaces")
        self.assertEqual((status, output), (0, "nchat-prod"))

    def test_absent_document_is_a_legitimate_empty_result(self):
        self.assertEqual(self.run_query("Service|does-not-exist|selector-slot"), (0, ""))

    def test_unknown_query_fails_rather_than_printing_nothing(self):
        buffer = io.StringIO()
        with redirect_stdout(buffer):
            status = query.run(self.manifest, "totally-unknown")
        self.assertEqual(status, query.EXIT_UNKNOWN_QUERY)

    def test_slots_equivalent_reports_a_difference(self):
        green = DEPLOYMENT.replace("blue", "green").replace("abc123", "def456")
        handle = tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False)
        handle.write(DEPLOYMENT.rstrip("\n") + "\n---\n" + green.rstrip("\n"))
        handle.close()
        self.addCleanup(lambda: pathlib.Path(handle.name).unlink(missing_ok=True))
        # Same shape, different release: equivalent, because images and the
        # release annotation are exactly what may differ between slots.
        self.assertEqual(query.run(handle.name, "slots|equivalent"), 0)


if __name__ == "__main__":
    unittest.main(verbosity=2, argv=[sys.argv[0]])
