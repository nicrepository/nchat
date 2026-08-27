#!/usr/bin/env python3
"""Reads facts out of a rendered production manifest (issue #626).

scripts/ci/prod-blue-green-check.sh asserts relationships between objects --
which slot a Service selects, whether an Ingress reaches a stable name or a
per-slot one, whether the two slots differ in anything but their images. Those
are joins across documents, and `grep -q blue` cannot express any of them.

Deliberately not a general YAML parser and not a new dependency. The input is
kustomize's own canonical output -- two-space indentation, one key per line,
keys sorted -- so an indentation-scoped reader is exact for this shape. Anything
else fails loudly rather than answering wrongly.

Usage: prod_blue_green_query.py <manifest> <query>
"""

from __future__ import annotations

import re
import sys

SLOT_ANNOTATION = "nchat.io/release-sha"
SLOT_LABEL = "nchat.io/release-slot"

# Distinct from 1, which _slots_equivalent uses to mean "they differ".
EXIT_UNKNOWN_QUERY = 2


def indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


class Document:
    """One rendered Kubernetes object, kept as its raw lines."""

    def __init__(self, text: str) -> None:
        self.lines = text.split("\n")
        self.kind = self._top("kind") or ""
        self.name = self.nested("metadata", "name") or ""
        self.namespace = self.nested("metadata", "namespace") or ""

    def _top(self, key: str) -> str | None:
        for line in self.lines:
            if indent(line) == 0 and line.startswith(f"{key}: "):
                return line.split(": ", 1)[1].strip()
        return None

    def block(self, *path: str) -> list[str]:
        """The lines strictly inside a nested mapping, by key path."""
        lines, depth = self.lines, 0
        for key in path:
            start = _find_key(lines, depth, key)
            if start is None:
                return []
            lines = lines[start + 1 : _block_end(lines, start + 1, depth)]
            depth += 2
        return lines

    def nested(self, *path: str) -> str | None:
        *parents, key = path
        for line in self.block(*parents):
            if line.strip().startswith(f"{key}: "):
                return line.split(": ", 1)[1].strip()
        return None

    def mapping(self, *path: str) -> dict[str, str]:
        result = {}
        for line in self.block(*path):
            stripped = line.strip()
            if ": " in stripped and not stripped.startswith("-"):
                key, value = stripped.split(": ", 1)
                result[key.strip("'\"")] = value.strip().strip("'\"")
        return result

    def values(self, key: str) -> list[str]:
        pattern = re.compile(rf"^\s*-?\s*{re.escape(key)}: (.+)$")
        return [m.group(1).strip().strip("'\"")
                for m in (pattern.match(line) for line in self.lines) if m]


def _find_key(lines: list[str], depth: int, key: str) -> int | None:
    """Index of `key:` at exactly `depth`, or None."""
    for position, line in enumerate(lines):
        if indent(line) == depth and line.strip() == f"{key}:":
            return position
    return None


def _inside_block(line: str, depth: int) -> bool:
    """Whether a line still belongs to a block opened at `depth`.

    A sequence may be indented level with its key -- "sourceRange:" followed by
    "- 10.0.0.0/8" at the same column is valid YAML and is what kustomize emits
    -- so a list item at the key's own depth is inside the block, while any
    other key at that depth ends it.
    """
    if not line.strip():
        return True
    if indent(line) > depth:
        return True
    return indent(line) == depth and line.strip().startswith("-")


def _block_end(lines: list[str], start: int, depth: int) -> int:
    """Index one past the last line of the block beginning at `start`."""
    end = start
    while end < len(lines) and _inside_block(lines[end], depth):
        end += 1
    return end


def _first_name_after(lines: list[str], start: int, span: int) -> str | None:
    """The first `name:` value in a short window, list item or not."""
    for line in lines[start : start + span]:
        match = re.match(r"^\s*-?\s*name: (\S+)$", line)
        if match:
            return match.group(1)
    return None


def load(path: str) -> list[Document]:
    with open(path, encoding="utf-8") as handle:
        return [Document(chunk) for chunk in handle.read().split("\n---\n") if chunk.strip()]


def by_kind(documents: list[Document], kind: str) -> list[Document]:
    return [d for d in documents if d.kind == kind]


def find(documents: list[Document], kind: str, name: str) -> Document | None:
    for document in documents:
        if document.kind == kind and document.name == name:
            return document
    return None


PROBE_KINDS = ("readinessProbe", "livenessProbe", "startupProbe")


def _field_release_sha(document: Document) -> str:
    return document.mapping("metadata", "annotations").get(SLOT_ANNOTATION, "")


def _field_probes(document: Document) -> str:
    present = [name for name in PROBE_KINDS
               if any(name in line for line in document.lines)]
    return " ".join(name.replace("Probe", "").lower() for name in present)


def _field_livekit_env(document: Document) -> str:
    """Whether the container takes the LiveKit allowlist from nchat-config.

    base pins the variable to an empty literal, and an explicit env entry wins
    over envFrom -- so a slot that did not override it would render
    LIVEKIT_ENABLED=true beside a CSP blocking every call.
    """
    for position, line in enumerate(document.lines):
        if line.strip() != "- name: NCHAT_WEB_LIVEKIT_CONNECT_SRC":
            continue
        window = "\n".join(document.lines[position : position + 6])
        return "configMapKeyRef" if "configMapKeyRef" in window and "value:" not in window else "literal"
    return "literal"


def _field_selector_slot(document: Document) -> str:
    return document.mapping("spec", "selector").get(SLOT_LABEL, "")


def _field_middlewares(document: Document) -> str:
    return document.mapping("metadata", "annotations").get(
        "traefik.ingress.kubernetes.io/router.middlewares", "")


def _field_source_range(document: Document) -> str:
    values = document.block("spec", "ipAllowList", "sourceRange")
    return " ".join(value.lstrip("- ").strip() for value in values)


def _field_node_hosts(document: Document) -> str:
    """Every hostname a node affinity admits, space separated.

    Written for the production stateful layer's local PersistentVolumes, whose
    whole safety property is that they can only bind on the node their directory
    is actually on. The values live under nodeSelectorTerms as bare list items,
    so they are collected positionally -- from the `values:` key to the end of
    its sequence -- rather than by a key path, which cannot address a list.
    """
    hosts, collecting = [], False
    for line in document.lines:
        stripped = line.strip()
        if stripped == "values:":
            collecting = True
        elif collecting and stripped.startswith("- "):
            hosts.append(stripped[2:].strip().strip("'\""))
        else:
            collecting = False
    return " ".join(hosts)


def _field_configmap_envfrom(document: Document) -> str:
    """Every ConfigMap this workload takes its whole environment from.

    `configMapRef` is envFrom -- the whole ConfigMap becomes environment. It is
    a different key from `configMapKeyRef`, which names one entry, so matching
    the line exactly keeps the two apart.
    """
    names = []
    for position, line in enumerate(document.lines):
        # A list item under envFrom ("- configMapRef:") or, less usually, a bare
        # key. Never configMapKeyRef, which is a different word entirely.
        if line.strip().lstrip("- ") != "configMapRef:":
            continue
        name = _first_name_after(document.lines, position + 1, 3)
        if name:
            names.append(name)
    return " ".join(names)


FIELD_QUERIES = {
    "selector-slot": _field_selector_slot,
    "middlewares": _field_middlewares,
    "source-range": _field_source_range,
    "release-sha": _field_release_sha,
    "probes": _field_probes,
    "livekit-env": _field_livekit_env,
    "node-hosts": _field_node_hosts,
    "configmap-envfrom": _field_configmap_envfrom,
}


def _backend_service_names(document: Document) -> list[str]:
    """Every Service an Ingress routes to.

    Read from the `backend.service.name` position specifically: an Ingress rule
    also carries a `port.name`, and taking every `name:` in the document would
    report the port "http" as though it were a Service.
    """
    names = []
    for position, line in enumerate(document.lines):
        if line.strip() != "service:":
            continue
        name = _first_name_after(document.lines, position + 1, 2)
        if name:
            names.append(name)
    return names


def _ingress_backends(documents: list[Document]) -> list[str]:
    return [f"{document.name}|{service}"
            for document in by_kind(documents, "Ingress")
            for service in _backend_service_names(document)]


def _slot_shapes(documents: list[Document], slot: str) -> dict[str, list[str]]:
    """Each slot workload reduced to the lines that are not its image.

    Comparing the two slots this way is what proves they differ only by release:
    a resource limit, a probe threshold or a mount that existed in one and not
    the other would make the candidate an untested configuration.
    """
    shapes = {}
    for document in by_kind(documents, "Deployment"):
        if not document.name.endswith(f"-{slot}"):
            continue
        stripped = [line for line in document.lines
                    if "image:" not in line and SLOT_ANNOTATION not in line
                    and not line.strip().startswith(f"{SLOT_LABEL}:")]
        shapes[document.name[: -len(slot) - 1]] = [line.replace(f"-{slot}", "") for line in stripped]
    return shapes


def _slots_equivalent(documents: list[Document]) -> int:
    blue, green = _slot_shapes(documents, "blue"), _slot_shapes(documents, "green")
    if blue.keys() != green.keys():
        print(f"slot membership differs: {sorted(set(blue) ^ set(green))}", file=sys.stderr)
        return 1
    differing = [name for name in blue if blue[name] != green[name]]
    if differing:
        print(f"slots differ beyond their images: {differing}", file=sys.stderr)
        return 1
    return 0


SECRET_REF_KEYS = ("secretRef", "secretKeyRef", "imagePullSecrets")


def _secret_names_in(document: Document) -> set[str]:
    names = set()
    for position, line in enumerate(document.lines):
        if not any(key in line for key in SECRET_REF_KEYS):
            continue
        name = _first_name_after(document.lines, position, 4)
        if name:
            names.add(name)
    return names


def _secret_refs(documents: list[Document]) -> list[str]:
    names: set[str] = set()
    for document in documents:
        names |= _secret_names_in(document)
    return sorted(names)


def _query_namespaces(documents: list[Document]) -> int:
    print("\n".join(sorted({d.namespace for d in documents if d.namespace})))
    return 0


def _query_secret_refs(documents: list[Document]) -> int:
    print("\n".join(_secret_refs(documents)))
    return 0


def _query_ingress_backends(documents: list[Document]) -> int:
    print("\n".join(_ingress_backends(documents)))
    return 0


def _query_deployment_images(documents: list[Document]) -> int:
    for document in by_kind(documents, "Deployment"):
        for image in document.values("image"):
            print(f"{document.name}|{image}")
    return 0


def _query_pdb_components(documents: list[Document]) -> int:
    for document in by_kind(documents, "PodDisruptionBudget"):
        labels = document.mapping("spec", "selector", "matchLabels")
        print(f"{document.name}|{labels.get('app.kubernetes.io/component', '')}")
    return 0


# Queries that span the whole manifest, keyed by the exact query string.
GLOBAL_QUERIES = {
    ("slots", "equivalent"): _slots_equivalent,
    ("namespaces",): _query_namespaces,
    ("secret-refs",): _query_secret_refs,
}

# Queries that list one fact for every document of a kind.
COLLECTION_QUERIES = {
    ("Ingress", "backends"): _query_ingress_backends,
    ("Deployment", "images"): _query_deployment_images,
    ("PodDisruptionBudget", "components"): _query_pdb_components,
}


def document_field(document: Document, field: str) -> str:
    """One fact about one document, or the empty string for an unknown field."""
    if field.startswith("data."):
        return document.mapping("data").get(field[len("data.") :], "")
    if field.startswith("hard."):
        return document.mapping("spec", "hard").get(field[len("hard.") :], "")
    # A dotted path under spec, for the flat scalars a PersistentVolume keeps
    # there: persistentVolumeReclaimPolicy, storageClassName, local.path. Only
    # mappings are addressable this way; a sequence needs a handler above.
    if field.startswith("spec."):
        return document.nested("spec", *field[len("spec.") :].split(".")) or ""
    handler = FIELD_QUERIES.get(field)
    return handler(document) if handler else ""


def _run_document_query(documents: list[Document], kind: str, name: str, field: str) -> int:
    document = find(documents, kind, name)
    print(document_field(document, field) if document else "")
    return 0


def _run_kind_query(documents: list[Document], parts: list[str]) -> int:
    kind, selector = parts[0], parts[1]
    if selector == "name":
        print("\n".join(d.name for d in by_kind(documents, kind)))
        return 0
    handler = COLLECTION_QUERIES.get((kind, selector))
    if handler:
        return handler(documents)
    return _run_document_query(documents, kind, selector, parts[2] if len(parts) > 2 else "")


def run(manifest: str, query: str) -> int:
    """Route one query. Unknown queries fail loudly rather than printing nothing.

    A silent empty answer is the dangerous failure here: the check script reads
    these values into assertions, and a typo that returned "" would satisfy the
    negative ones and quietly stop asserting anything.
    """
    documents = load(manifest)
    parts = query.split("|")
    handler = GLOBAL_QUERIES.get(tuple(parts))
    if handler:
        return handler(documents)
    if len(parts) < 2:
        print(f"unknown query: {query}", file=sys.stderr)
        return EXIT_UNKNOWN_QUERY
    return _run_kind_query(documents, parts)


if __name__ == "__main__":
    sys.exit(run(sys.argv[1], sys.argv[2]))
