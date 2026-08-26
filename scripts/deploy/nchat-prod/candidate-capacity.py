#!/usr/bin/env python3
"""Capacity preflight for a candidate release slot (issue #626).

What this replaced, and why. An earlier revision called

    kubectl apply --dry-run=server

on the candidate's Deployments and treated success as proof that the namespace
could hold a second slot. It is not. A server dry-run admits the Deployment
object; it does not create ReplicaSets or Pods, and ResourceQuota is charged
against Pods. The check therefore passed unconditionally, and the first time
anyone would have learned the quota was too small was half-way through a
rollout, with two slots already competing for it.

This sums what the candidate actually asks for -- every container's requests,
multiplied by the workload's replica count -- and compares it against the
namespace's own accounting and, when supplied, the nodes' allocatable capacity.

It is a preflight, not a scheduler, and it says so. It answers "is there
provably not enough room"; it never claims the opposite. Per-node
fragmentation, taints, affinity, topology spread and eviction pressure are
settled by the rollout, which remains the second barrier. A dimension whose
numbers the cluster did not supply is reported INCONCLUSIVE rather than
counted as a pass.

Exit codes: 0 sufficient, 1 provably insufficient, 2 inconclusive, 3 unusable
input.
"""

from __future__ import annotations

import argparse
import re
import sys

EXIT_OK = 0
EXIT_INSUFFICIENT = 1
EXIT_INCONCLUSIVE = 2
EXIT_BAD_INPUT = 3

_MEMORY_SUFFIXES = (
    ("Ki", 1024), ("Mi", 1024**2), ("Gi", 1024**3), ("Ti", 1024**4),
    ("K", 1000), ("M", 1000**2), ("G", 1000**3), ("T", 1000**4),
)


def parse_cpu(value: str) -> int:
    """Kubernetes CPU to millicores: '1500m' -> 1500, '2' -> 2000, '0.5' -> 500."""
    text = str(value).strip().strip('"\'')
    if text.endswith("m"):
        return int(round(float(text[:-1])))
    return int(round(float(text) * 1000))


def parse_memory(value: str) -> int:
    """Kubernetes memory to bytes, binary and decimal suffixes alike."""
    text = str(value).strip().strip('"\'')
    for suffix, factor in _MEMORY_SUFFIXES:
        if text.endswith(suffix):
            return int(float(text[: -len(suffix)]) * factor)
    return int(float(text))


def _indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def _requests_in(lines: list[str]) -> tuple[int, int, int]:
    """Sum every `requests:` block in one document.

    Deliberately not a general YAML parser. The input is kustomize's own
    canonical output -- two-space indentation, one key per line -- so an
    indentation-scoped scan is exact for this shape and fails loudly rather
    than silently mis-summing anything else.
    """
    cpu = memory = storage = 0
    index = 0
    while index < len(lines):
        line = lines[index]
        index += 1
        if line.strip() != "requests:":
            continue
        block = _indent(line)
        while index < len(lines) and _indent(lines[index]) > block:
            entry = lines[index].strip()
            index += 1
            key, _, raw = entry.partition(":")
            if key == "cpu":
                cpu += parse_cpu(raw)
            elif key == "memory":
                memory += parse_memory(raw)
            elif key == "ephemeral-storage":
                # Same units as memory, and the same reason to count it: the
                # scheduler reserves it per pod, so a second slot needs its own.
                storage += parse_memory(raw)
    return cpu, memory, storage


class Workload:
    """One Deployment of the candidate, reduced to what capacity depends on."""

    def __init__(self, name: str, replicas: int, cpu: int, memory: int, max_surge: int,
                 storage: int = 0) -> None:
        self.name = name
        self.replicas = replicas
        self.cpu = cpu          # per pod, millicores
        self.memory = memory    # per pod, bytes
        self.storage = storage  # per pod, bytes of ephemeral-storage
        self.max_surge = max_surge


def _replicas_of(lines: list[str]) -> int:
    for line in lines:
        match = re.fullmatch(r"  replicas: (\d+)", line)
        if match:
            return int(match.group(1))
    return 1


def _max_surge_of(lines: list[str], replicas: int) -> int:
    """maxSurge as a pod count, from an integer or a percentage."""
    for line in lines:
        match = re.fullmatch(r"\s*maxSurge: \"?([0-9]+)%?\"?", line)
        if not match:
            continue
        value = int(match.group(1))
        if line.rstrip().rstrip('"').endswith("%"):
            return -(-value * replicas // 100)  # ceil, as Kubernetes rounds up
        return value
    return 1  # the Kubernetes default when RollingUpdate leaves it unset


def workloads(manifest: str) -> list[Workload]:
    found = []
    for document in manifest.split("\n---\n"):
        lines = document.split("\n")
        if not any(line.strip() == "kind: Deployment" for line in lines):
            continue
        name = next((line.split(": ", 1)[1].strip()
                     for line in lines if line.startswith("  name: ")), "")
        replicas = _replicas_of(lines)
        cpu, memory, storage = _requests_in(lines)
        found.append(Workload(name, replicas, cpu, memory,
                              _max_surge_of(lines, replicas), storage))
    return found


class CurrentWorkload:
    """A Deployment of the target slot as the cluster holds it right now."""

    def __init__(self, replicas: int, cpu: int, memory: int, storage: int = 0) -> None:
        self.replicas = replicas
        self.cpu = cpu          # per pod, millicores
        self.memory = memory    # per pod, bytes
        self.storage = storage  # per pod, bytes of ephemeral-storage


def parse_current_slot(text: str) -> dict[str, CurrentWorkload]:
    """Parse "<deployment>|<replicas>|<cpu>|<memory>|<storage>", per container.

    Containers of the same Deployment are summed, so a per-pod figure covers
    every container in the pod -- the same thing the candidate side counts, and
    for the same reason: two parsers with different notions of "the requests of
    a pod" would compare quantities that are not comparable.
    """
    current: dict[str, CurrentWorkload] = {}
    for line in text.splitlines():
        fields = line.split("|")
        if len(fields) != 5 or not fields[1].strip().isdigit():
            continue
        name = fields[0].strip()
        entry = current.setdefault(name, CurrentWorkload(int(fields[1]), 0, 0, 0))
        if fields[2].strip():
            entry.cpu += parse_cpu(fields[2])
        if fields[3].strip():
            entry.memory += parse_memory(fields[3])
        if fields[4].strip():
            entry.storage += parse_memory(fields[4])
    return current


def rollout_peak(desired: int, surge: int, current: int, new_cost: int, old_cost: int) -> int:
    """The most of one resource a RollingUpdate can hold at once.

    Kubernetes caps the total pods of a Deployment at desired+maxSurge while the
    replacement runs, the new ReplicaSet never exceeds desired, and the old one
    never exceeds what is already running. Within those three limits the worst
    instant is the one holding as many of the expensive pods as it allows, so
    the more expensive kind is filled first and the remainder given to the other.

    Costing this in resources rather than pod counts is the correction: an
    earlier version counted the surge as one extra pod and priced it at the new
    per-pod requests, which reported 1000m for a release going from 2x100m to
    2x1000m. The final state alone needs 1800m more than the cluster holds.
    """
    total_cap = desired + surge
    if new_cost >= old_cost:
        new_pods = min(desired, total_cap)
        old_pods = min(current, total_cap - new_pods)
    else:
        old_pods = min(current, total_cap)
        new_pods = min(desired, total_cap - old_pods)
    return new_pods * new_cost + old_pods * old_cost


def _additional(workload: "Workload", current: CurrentWorkload | None) -> tuple[int, int, int, int]:
    """(millicores, bytes, pods) the cluster must find beyond what it holds.

    A slot that does not exist costs its whole replica count: there is no old
    ReplicaSet to replace and nothing already accounted for. A slot that does
    exist is being replaced, so only the difference between the rollout's peak
    and what is already committed is new demand.
    """
    if current is None:
        return (workload.cpu * workload.replicas,
                workload.memory * workload.replicas,
                workload.storage * workload.replicas,
                workload.replicas)

    def extra(new_cost: int, old_cost: int, held: int) -> int:
        peak = rollout_peak(workload.replicas, workload.max_surge, current.replicas,
                            new_cost, old_cost)
        return max(0, peak - held)

    return (
        extra(workload.cpu, current.cpu, current.replicas * current.cpu),
        extra(workload.memory, current.memory, current.replicas * current.memory),
        extra(workload.storage, current.storage, current.replicas * current.storage),
        extra(1, 1, current.replicas),
    )


def summarise(manifest: str, current: dict[str, CurrentWorkload] | None = None
              ) -> tuple[int, int, int, int]:
    """(millicores, bytes, storage-bytes, pods) added on top of current usage."""
    cpu = memory = storage = pods = 0
    for workload in workloads(manifest):
        held = (current or {}).get(workload.name)
        extra_cpu, extra_memory, extra_storage, extra_pods = _additional(workload, held)
        cpu += extra_cpu
        memory += extra_memory
        storage += extra_storage
        pods += extra_pods
    return cpu, memory, storage, pods


TERMINAL_PHASES = frozenset({"Succeeded", "Failed"})


def counts_against_capacity(phase: str, node_name: str) -> bool:
    """Whether a Pod is holding capacity on a node right now.

    Two conditions, and phase alone is neither of them:

    - it must be scheduled. A Pod the scheduler has not bound to a node has
      reserved nothing anywhere.
    - it must not be finished. Succeeded and Failed Pods stay listed by the API
      long after they have released their reservation.

    Everything else counts, Pending included. A Pod that has been bound to a
    node is charged against it from that moment, and it can then sit in Pending
    for a long time -- pulling an image, attaching a volume, working through
    init containers. Counting only Running Pods understates commitment exactly
    when a cluster is busy, which is exactly when a second release slot is most
    likely to be admitted and then evicted.
    """
    if not node_name.strip():
        return False
    return phase.strip() not in TERMINAL_PHASES


def sum_node_pod_slots(text: str) -> int | None:
    """Total pod slots the nodes offer, from a "<cpu> <memory> <storage> <pods>".

    A namespace quota is not the only ceiling on pods: the kubelet caps each
    node at status.allocatable.pods, and a cluster can be out of slots while the
    quota still has room. A node that does not report the figure makes the whole
    sum unknown rather than smaller — reading a missing value as zero would fail
    every deploy, and skipping it would silently overstate the cluster.
    """
    total = 0
    for line in text.splitlines():
        if not line.strip():
            continue
        fields = line.split()
        if len(fields) < 4 or not fields[3].isdigit():
            return None
        total += int(fields[3])
    return total or None


def sum_node_allocatable(text: str) -> tuple[int, int, int | None]:
    """Sum "<cpu> <memory> <storage> <pods>" node lines into (cpu, memory, storage).

    Field-positional rather than reusing the request-line parser: a node line
    carries a fourth column, and a parser that expects three would read
    "200Gi 110" as one quantity and reject the whole file.

    Storage comes back as None when any node omits it. A node that does not
    report the resource means the cluster could not tell us, and reading that as
    zero would turn "unknown" into "no room at all".
    """
    cpu = memory = storage = 0
    storage_known = True
    for line in text.splitlines():
        if not line.strip():
            continue
        fields = line.split()
        if len(fields) < 2:
            raise ValueError(f"node line has no memory figure: {line!r}")
        cpu += parse_cpu(fields[0])
        memory += parse_memory(fields[1])
        if len(fields) < 3:
            storage_known = False
            continue
        storage += parse_memory(fields[2])
    return cpu, memory, (storage if storage_known else None)


def count_scheduled_pods(text: str) -> int:
    """Pod slots already taken on nodes, from "<phase>|<nodeName>" lines.

    One line per Pod, deliberately a different input from the request sums,
    which are per container: counting those lines would charge a two-container
    Pod for two slots.

    The rule is the one the resource sums use — a Pod holds its slot from the
    moment it is bound, so Pending-with-a-node counts, Pending-without-one does
    not, and Succeeded/Failed have released theirs.
    """
    taken = 0
    for line in text.splitlines():
        if not line.strip():
            continue
        phase, _, node_name = line.partition("|")
        if counts_against_capacity(phase, node_name):
            taken += 1
    return taken


def sum_scheduled_pod_requests(text: str) -> tuple[int, int, int]:
    """Sum "<phase>|<nodeName>|<cpu> <memory> <storage>" for scheduled, live Pods."""
    kept = []
    for line in text.splitlines():
        if not line.strip():
            continue
        phase, _, rest = line.partition("|")
        node_name, _, requests = rest.partition("|")
        if counts_against_capacity(phase, node_name):
            kept.append(requests)
    return sum_request_lines("\n".join(kept))


def sum_request_lines(text: str) -> tuple[int, int, int]:
    """Sum "<cpu> <memory>" lines into (millicores, bytes).

    The caller collects one line per running container from the cluster; either
    field may be empty, because a container without a declared request reserves
    nothing and must contribute nothing. Keeping the arithmetic here rather than
    in the shell means every unit suffix is parsed by the same tested code that
    parses the candidate.
    """
    cpu = memory = storage = 0
    for line in text.splitlines():
        if not line.strip():
            continue
        # Split positionally, not on whitespace. The caller's jsonpath emits
        # "<cpu> <memory>" with an empty field where a request is absent, so a
        # container declaring only memory arrives as " 128Mi" -- and splitting on
        # whitespace would read that lone value as a CPU quantity.
        cpu_text, _, rest = line.partition(" ")
        memory_text, _, storage_text = rest.partition(" ")
        if cpu_text.strip():
            cpu += parse_cpu(cpu_text)
        if memory_text.strip():
            memory += parse_memory(memory_text)
        if storage_text.strip():
            storage += parse_memory(storage_text)
    return cpu, memory, storage


class Report:
    """Collects per-dimension verdicts and reduces them to one exit code.

    Insufficient beats inconclusive beats sufficient: a preflight that found a
    real shortfall must not be softened by another dimension it could not read.
    """

    def __init__(self) -> None:
        self.insufficient = False
        self.inconclusive = False

    def compare(self, label: str, needed: int, hard: int | None, used: int | None, unit: str) -> None:
        if hard is None or used is None:
            self.inconclusive = True
            print(f"  [INCONCLUSIVE] {label}: cluster did not report this dimension")
            return
        free = hard - used
        if needed <= free:
            print(f"  [OK]   {label}: need {needed}{unit}, free {free}{unit} of {hard}{unit}")
            return
        self.insufficient = True
        print(f"  [FAIL] {label}: need {needed}{unit}, only {free}{unit} free of {hard}{unit}")

    def exit_code(self) -> int:
        if self.insufficient:
            return EXIT_INSUFFICIENT
        return EXIT_INCONCLUSIVE if self.inconclusive else EXIT_OK


def _optional(value: str | None, convert) -> int | None:
    if value is None or value == "":
        return None
    return convert(value)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True)
    for name in ("quota-hard-cpu", "quota-used-cpu", "quota-hard-memory", "quota-used-memory",
                 "quota-hard-pods", "quota-used-pods",
                 "quota-hard-storage", "quota-used-storage"):
        parser.add_argument(f"--{name}", default="")
    # One "<cpu> <memory>" line per node.
    parser.add_argument("--node-allocatable-file", default="")
    # One "<phase>|<nodeName>|<cpu> <memory>" line per container in the cluster.
    parser.add_argument("--cluster-requests-file", default="")
    # One "<phase>|<nodeName>" line per Pod in the cluster, for pod slots.
    parser.add_argument("--cluster-pods-file", default="")
    # One "<deployment>|<replicas>|<cpu>|<memory>" line per container the target
    # slot already runs. Absent means the slot does not exist yet, which is the
    # first deploy of it.
    parser.add_argument("--current-slot-file", default="")
    return parser


def _read_current_slot(path: str) -> dict[str, CurrentWorkload] | None:
    if not path:
        return None
    try:
        with open(path, encoding="utf-8") as handle:
            text = handle.read()
    except OSError:
        return None
    return parse_current_slot(text) if text.strip() else None


def _parse_nodes(text: str | None) -> tuple[int | None, int | None, int | None]:
    """Node allocatable totals, or all-unknown when the cluster reported nothing."""
    if text is None:
        return None, None, None
    try:
        return sum_node_allocatable(text)
    except ValueError:
        return None, None, None


def _read_scheduled_pods(path: str) -> int | None:
    """Pod slots already taken, or None when the cluster did not report them."""
    text = _read_text(path)
    return None if text is None else count_scheduled_pods(text)


def _read_text(path: str) -> str | None:
    if not path:
        return None
    try:
        with open(path, encoding="utf-8") as handle:
            text = handle.read()
    except OSError:
        return None
    return text if text.strip() else None


def _sum_file(path: str, parse=None) -> tuple[int | None, int | None, int | None]:
    """(cpu, memory) summed from a file, or (None, None) when unavailable.

    Absent, unreadable and EMPTY are the same answer on purpose. An empty file
    means the cluster query returned nothing -- no permission, no metrics, a
    kubectl that failed -- and reading that as a sum of zero is the dangerous
    interpretation in both directions: zero allocatable makes every candidate
    look too big, and zero committed makes every candidate look like it fits.
    A dimension the cluster did not supply is reported INCONCLUSIVE, never
    assumed.
    """
    if not path:
        return None, None, None
    try:
        with open(path, encoding="utf-8") as handle:
            text = handle.read()
    except OSError:
        return None, None, None
    if not text.strip():
        return None, None, None
    try:
        return (parse or sum_request_lines)(text)
    except ValueError:
        return None, None, None


def run(args: argparse.Namespace) -> int:
    current = _read_current_slot(args.current_slot_file)
    try:
        with open(args.manifest, encoding="utf-8") as handle:
            manifest = handle.read()
        declared = workloads(manifest)
        cpu, memory, storage, pods = summarise(manifest, current)
    except (OSError, ValueError) as error:
        print(f"  [ERROR] candidate manifest is unusable: {error}", file=sys.stderr)
        return EXIT_BAD_INPUT
    # Emptiness is a property of the manifest, not of the demand. A release that
    # scales a slot down genuinely adds nothing, and that is a pass, not a
    # malformed input.
    if not declared:
        print("  [ERROR] candidate manifest declares no Deployment", file=sys.stderr)
        return EXIT_BAD_INPUT

    mode = "first deploy" if current is None else f"redeploy over {len(current)} existing workload(s)"
    print(f"candidate additional demand ({mode}): "
          f"cpu={cpu}m memory={memory}B ephemeral-storage={storage}B pods={pods}")
    report = Report()
    report.compare("namespace quota requests.cpu", cpu,
                   _optional(args.quota_hard_cpu, parse_cpu),
                   _optional(args.quota_used_cpu, parse_cpu), "m")
    report.compare("namespace quota requests.memory", memory,
                   _optional(args.quota_hard_memory, parse_memory),
                   _optional(args.quota_used_memory, parse_memory), "B")
    report.compare("namespace quota requests.ephemeral-storage", storage,
                   _optional(args.quota_hard_storage, parse_memory),
                   _optional(args.quota_used_storage, parse_memory), "B")
    report.compare("namespace quota pods", pods,
                   _optional(args.quota_hard_pods, int),
                   _optional(args.quota_used_pods, int), "")
    # Read once, then parsed twice. The caller may pass a process substitution,
    # which yields nothing on a second open — reading it again is how the pod
    # dimension came back INCONCLUSIVE against a file that did report it.
    node_text = _read_text(args.node_allocatable_file)
    allocatable_cpu, allocatable_memory, allocatable_storage = _parse_nodes(node_text)
    pod_slots = None if node_text is None else sum_node_pod_slots(node_text)
    committed_cpu, committed_memory, committed_storage = _sum_file(
        args.cluster_requests_file, sum_scheduled_pod_requests)
    report.compare("cluster allocatable cpu", cpu, allocatable_cpu, committed_cpu, "m")
    report.compare("cluster allocatable memory", memory, allocatable_memory, committed_memory, "B")
    report.compare("cluster allocatable ephemeral-storage", storage,
                   allocatable_storage, committed_storage, "B")
    report.compare("cluster allocatable pods", pods,
                   pod_slots, _read_scheduled_pods(args.cluster_pods_file), "")
    return report.exit_code()


def main(argv: list[str]) -> int:
    return run(build_parser().parse_args(argv))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
