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
input -- the candidate manifest or a cluster-wide file that does not follow the
collector's line contract. Unusable and inconclusive are deliberately different:
only the second is something an operator may override.
"""

from __future__ import annotations

import argparse
import re
import sys
from decimal import Decimal, InvalidOperation

EXIT_OK = 0
EXIT_INSUFFICIENT = 1
EXIT_INCONCLUSIVE = 2
EXIT_BAD_INPUT = 3

# Kubernetes' own grammar, from apimachinery's resource.Quantity:
#
#     quantity        ::= signedNumber suffix
#     suffix          ::= binarySI | decimalExponent | decimalSI
#     binarySI        ::= Ki | Mi | Gi | Ti | Pi | Ei
#     decimalSI       ::= n | u | m | "" | k | M | G | T | P | E
#     decimalExponent ::= ("e" | "E") signedNumber
#
# A suffix is one of the three, never a combination: "129e6" is an exponent,
# "1Ei" is a binary suffix, and "1e3Ki" is not a quantity at all -- hence the
# alternation rather than two optional groups in sequence. The exponent is
# handed to Decimal still attached to its digits, which is what makes "129E6"
# and "129e6" the same value without a second code path.
#
# 'K' is accepted alongside 'k' as the one deliberate extension: Kubernetes
# rejects it, the manifests in this repository have always written it, and
# reading it as anything other than a thousand would be worse than accepting it.
_QUANTITY = re.compile(
    r"(?P<number>[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+))"
    r"(?:(?P<suffix>Ki|Mi|Gi|Ti|Pi|Ei|[numkKMGTPE])|(?P<exponent>[eE][+-]?[0-9]+))?"
)

# suffix -> (numerator, denominator). Kept as an exact ratio rather than a float
# so that 'n', 'u' and 'm' are a division by a whole number instead of a binary
# fraction that cannot represent a tenth.
_DECIMAL_SI = {"n": -9, "u": -6, "m": -3, "k": 3, "K": 3,
               "M": 6, "G": 9, "T": 12, "P": 15, "E": 18}
_BINARY_SI = {"Ki": 10, "Mi": 20, "Gi": 30, "Ti": 40, "Pi": 50, "Ei": 60}

# The ceiling Kubernetes itself imposes: a Quantity is an int64 internally, and
# the API server refuses what does not fit. Applying the same bound is what
# keeps '1Ei' (2^60 bytes) a valid quantity while '1e309' is not -- and it is
# the API's limit rather than one invented here, which is why it can be stated
# in a message an operator can act on.
# The largest value this evaluator will carry, in the unit it counts that
# resource in -- millicores for CPU, bytes for memory and ephemeral-storage.
#
# This is the accessor's limit, not a rule of the Kubernetes quantity grammar.
# Kubernetes' own Value() and MilliValue() return int64 and saturate there, and
# every figure downstream of here is a Python int compared against a cluster
# total, so a quantity that cannot be expressed as an int64 of the unit in
# question is one this program has no way to reason about. The grammar itself
# is wider; what is bounded is what the evaluator can count.
_MAX_EVALUATOR_UNITS = 2**63 - 1

# How far a decimal exponent may reach upward before the quantity is refused
# without being expanded.
#
# Only upward. The two directions cost different things. A positive exponent has
# to be written out as digits -- '1e999999999999999999' asks Python to build an
# integer with a quintillion of them, and the process stops responding rather
# than answering, which in front of a production deploy is the one outcome worse
# than a wrong answer. A negative exponent needs no such thing: a positive value
# below one unit is one unit after the ceiling, and _in_units settles that by
# comparing digit counts. Guarding both alike is what made '1e-89' -- an
# ordinary, if tiny, quantity -- come back as invalid input.
#
# Derived from _MAX_EVALUATOR_UNITS rather than chosen: an accepted value is at
# most 19 digits, the largest a suffix multiplies by is Ei (2^60, under 19
# digits) and the smallest it divides by is n (10^-9). Nothing past 10^88 can
# become an accepted value under any suffix, so this refuses only what is
# already impossible and leaves every borderline case to the exact check below.
_MAX_POSITIVE_EXPONENT = len(str(_MAX_EVALUATOR_UNITS)) + 60 + 9


class InvalidQuantity(ValueError):
    """A resource quantity outside the contract.

    Every quantity this program reads -- from the candidate manifest, from the
    slot it is replacing, from the namespace quota, from the nodes and from the
    cluster's Pods -- names an amount of a resource, and there is no such thing
    as a negative one, an infinite one or one that is not a number. Each of
    those reaches an arithmetic that subtracts committed from allocatable, so a
    negative request does not read as an error: it reads as a cluster with more
    room than it has. That is the shape of a false pass, which is why this is
    refused as unusable input rather than absorbed as a zero.

    A ValueError, so that the callers that already treat an unreadable
    quantity as unusable input keep doing so without a second code path.
    """


def _scale_of(suffix: str) -> tuple[int, int]:
    """A suffix as the exact ratio it multiplies by."""
    if suffix in _BINARY_SI:
        return 2 ** _BINARY_SI[suffix], 1
    power = _DECIMAL_SI.get(suffix, 0)
    return (10 ** power, 1) if power >= 0 else (1, 10 ** -power)


def parse_quantity(value: str) -> tuple[Decimal, int, int]:
    """One Kubernetes quantity as (number, scale numerator, scale denominator).

    Every quantity in this program goes through here, so CPU, memory and
    ephemeral-storage cannot drift apart on what a suffix means or on how a
    fraction is rounded.

    No float anywhere on the path. The version before last read the number with
    float() and multiplied by the suffix, which cost it the grammar in both
    directions: 'm', 'n', 'u', 'k', 'Pi' and 'Ei' were not suffixes it knew, so
    an ordinary '400m' came back as "not a number" and stopped a deploy; and
    what it did accept it accepted approximately, because 0.1 is not a binary
    fraction.

    Unexpanded, and the suffix kept apart from the number: the caller is the one
    that knows whether this is about to be counted in bytes or in millicores,
    and it is also the one that can decide a magnitude question without building
    the digits. Deciding either here is what produced the two regressions this
    function has already had.
    """
    text = str(value).strip().strip('"\'')
    match = _QUANTITY.fullmatch(text)
    if match is None:
        raise InvalidQuantity("quantity is not a Kubernetes quantity")
    try:
        number = Decimal(match["number"] + (match["exponent"] or ""))
    except InvalidOperation:
        raise InvalidQuantity("quantity is not a number") from None
    if number < 0:
        raise InvalidQuantity("quantity is negative")
    scale_numerator, scale_denominator = _scale_of(match["suffix"] or "")
    return number, scale_numerator, scale_denominator


def _digits_and_exponent(number: Decimal) -> tuple[int, int]:
    """The digits Decimal is holding, as one integer, and their exponent.

    as_tuple() hands back what Decimal already stores, so this costs what the
    input is long rather than what the value is big: 1e999999999999999999 is the
    digit 1 and an exponent, and stays that way.
    """
    _, digits, exponent = number.as_tuple()
    return int("".join(map(str, digits))), exponent


def _ceil(numerator: int, denominator: int) -> int:
    """Integer ceiling. Kubernetes' Value() and MilliValue() round up, and so
    does a preflight: half a byte of demand costs a byte, and no amount of
    demand rounds away to nothing."""
    return -(-numerator // denominator)


def _in_units(value: str, per_unit: int) -> int:
    """A quantity counted in units of 1/per_unit of the resource's base unit.

    Zero is zero. Everything else is positive, and a positive value below one
    unit is one unit -- which is settled by comparing how many digits the
    numerator has against how many zeros the denominator would have, rather than
    by writing those zeros out. That is what lets '1e-999999999999999999' answer
    as fast as '1', and it is exact rather than an approximation: a denominator
    of 10^k with k at least the numerator's digit count is strictly larger than
    the numerator, so the quotient is strictly between 0 and 1.
    """
    number, scale_numerator, scale_denominator = parse_quantity(value)
    if number.is_zero():
        return 0
    digits, exponent = _digits_and_exponent(number)
    numerator = digits * scale_numerator * per_unit
    if exponent < 0:
        if -exponent >= len(str(numerator)):
            return 1
        return _bounded(_ceil(numerator, scale_denominator * 10 ** -exponent))
    if exponent > _MAX_POSITIVE_EXPONENT:
        raise InvalidQuantity("quantity's exponent is beyond any usable magnitude")
    return _bounded(_ceil(numerator * 10 ** exponent, scale_denominator))


def _bounded(units: int) -> int:
    """The exact size check, on the finished figure in the resource's own unit."""
    if units > _MAX_EVALUATOR_UNITS:
        raise InvalidQuantity("quantity is too large to be counted by this evaluator")
    return units


def parse_cpu(value: str) -> int:
    """Kubernetes CPU to millicores: '1500m' -> 1500, '2' -> 2000, '0.5' -> 500."""
    return _in_units(value, 1000)


def parse_count(value: str) -> int:
    """A whole, non-negative count, for the one dimension measured in pods.

    The quota's pod figures went through int(), which accepts "-5" -- and a
    negative "used" enlarges the free space the same way a negative request
    does. Nothing else about the dimension changes: it is still a plain count.
    """
    text = str(value).strip().strip('"\'')
    if not text.isdigit():
        raise InvalidQuantity("count is not a whole, non-negative number")
    return int(text)


def parse_memory(value: str) -> int:
    """Kubernetes memory or ephemeral-storage to whole bytes, rounded up.

    '2.5' is three bytes, not two: bytes are the base unit here, so the ceiling
    _in_units applies is the one Kubernetes' Value() applies.
    """
    return _in_units(value, 1)


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


class InvalidClusterInput(ValueError):
    """A cluster-wide input line that does not follow the collector's contract.

    Kept apart from "the cluster did not report this dimension" on purpose. A
    file that is absent, empty or short of a column is a gap in what the cluster
    answered, and INCONCLUSIVE is the honest verdict; an operator who has
    checked by hand can override it. A line that is not of the shape the
    collector emits is a broken input, and reporting that as INCONCLUSIVE would
    put it within reach of the same override.

    It matters because a malformed line is not inert. `not-a-pod-record` parsed
    as "no node name", which reads as a Pod holding nothing, so a file of them
    reported an empty cluster and passed a candidate that does not fit.
    """


# Every phase Kubernetes defines for a Pod. The complete enum, not a sample:
# a line carrying anything else did not come from a Pod listing.
POD_PHASES = frozenset({"Pending", "Running", "Succeeded", "Failed", "Unknown"})

TERMINAL_PHASES = frozenset({"Succeeded", "Failed"})


def _invalid(what: str, number: int,
             reason: str = "line does not follow the collector's contract") -> InvalidClusterInput:
    """The line number and a reason, and nothing from the line itself.

    Evidence describes namespaces this deploy has no business reading, and an
    error message is the one place its contents would otherwise be printed. The
    reason says what is wrong with the shape, never what the line said.
    """
    return InvalidClusterInput(f"invalid {what} input at line {number}: {reason}")


def _quantity(value: str, convert, what: str, number: int) -> int:
    """A quantity that must be there and must be a real one."""
    try:
        return convert(value)
    except InvalidQuantity as error:
        raise _invalid(what, number, str(error)) from None
    except ValueError:
        raise _invalid(what, number) from None


def _optional_quantity(value: str, convert, what: str, number: int) -> int:
    """The same, for a field the cluster is allowed to leave empty."""
    if not value.strip():
        return 0
    return _quantity(value, convert, what, number)


def _pod_record(line: str, number: int, what: str, fields: int) -> list[str]:
    """Split a "<phase>|<nodeName>[|...]" line, or refuse it.

    Two conditions, and neither is about the values: the delimiters have to be
    there in the right number, and the phase has to be one Kubernetes defines.
    An empty nodeName is not a missing field -- it is how the collector says the
    scheduler has not bound this Pod anywhere, which is a real state.
    """
    parts = line.split("|")
    if len(parts) != fields:
        raise _invalid(what, number, f"expected {fields} fields separated by '|'")
    if parts[0].strip() not in POD_PHASES:
        raise _invalid(what, number, "the first field is not a Kubernetes pod phase")
    return parts


def _node_record(line: str, number: int) -> list[str]:
    """A node's four positional fields: <cpu> <memory> <storage> <pods>.

    Split on the single separator, never on runs of whitespace. The collector
    emits one space between each position and leaves a position empty when the
    node does not report it, so a node without ephemeral-storage arrives as

        8 32Gi  110

    and str.split() collapsed that pair of spaces into one separator: the pod
    ceiling slid into the storage position, 110 was read as 110 *bytes* of
    allocatable storage, and the run reported a storage shortfall that does not
    exist while calling the pod dimension unknown. Both answers were wrong, and
    one of them was a hard FAIL on a cluster with room.

    Trailing positions may be omitted rather than left empty -- '8 32Gi' says
    the same thing as '8 32Gi  ' -- so short lines are padded here and the
    caller sees four positions either way. cpu and memory are the contract and
    must be there; an empty storage or pods position is a dimension the cluster
    did not report, which the caller answers INCONCLUSIVE.
    """
    fields = line.split(" ")
    if not 2 <= len(fields) <= 4:
        raise _invalid("node allocatable", number,
                       "expected the positional fields <cpu> <memory> <ephemeral-storage> <pods>")
    if not fields[0].strip() or not fields[1].strip():
        raise _invalid("node allocatable", number, "cpu and memory are not optional")
    return (fields + ["", ""])[:4]


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
    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        pods = _node_record(line, number)[3].strip()
        if not pods:
            return None
        if not pods.isdigit():
            raise _invalid("node allocatable", number,
                           "the pod ceiling is not a whole, non-negative number")
        total += int(pods)
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
    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        fields = _node_record(line, number)
        cpu += _quantity(fields[0], parse_cpu, "node allocatable", number)
        memory += _quantity(fields[1], parse_memory, "node allocatable", number)
        if not fields[2].strip():
            storage_known = False
            continue
        storage += _quantity(fields[2], parse_memory, "node allocatable", number)
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
    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        phase, node_name = _pod_record(line, number, "cluster pod", 2)
        if counts_against_capacity(phase, node_name):
            taken += 1
    return taken


def parse_request_quantities(text: str, number: int) -> tuple[int, int, int]:
    """The "<cpu> <memory> <ephemeral-storage>" tail of one container's line.

    Split positionally, not on whitespace. The collector's jsonpath emits an
    empty field where a request is absent, so a container declaring only memory
    arrives as " 128Mi" -- splitting on whitespace would read that lone value as
    a CPU quantity.

    A container may declare none of the three, one, or all: that is ordinary
    Kubernetes, it reserves nothing for what it omits, and it is not a malformed
    line. A field that IS present has to be a real quantity, and a fourth field
    cannot come from this jsonpath at all.
    """
    fields = text.split(" ")
    if len(fields) > 3:
        raise _invalid("cluster request", number,
                       "expected at most <cpu> <memory> <ephemeral-storage>")
    cpu, memory, storage = (fields + ["", "", ""])[:3]
    return (
        _optional_quantity(cpu, parse_cpu, "cluster request", number),
        _optional_quantity(memory, parse_memory, "cluster request", number),
        _optional_quantity(storage, parse_memory, "cluster request", number),
    )


def sum_scheduled_pod_requests(text: str) -> tuple[int, int, int]:
    """Sum "<phase>|<nodeName>|<cpu> <memory> <storage>" for scheduled, live Pods.

    Keeping the arithmetic here rather than in the shell means every unit suffix
    is parsed by the same tested code that parses the candidate -- and that a
    line the collector cannot have produced is refused in one place, for the
    live collection and for evidence alike.
    """
    cpu = memory = storage = 0
    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        phase, node_name, requests = _pod_record(line, number, "cluster request", 3)
        # Parsed before the filter, never after. A Pod that holds no capacity is
        # still a record the collector produced, and its quantities still have to
        # follow the contract: "Succeeded|node-a|not-a-quantity" used to be
        # dropped before anything looked at it, so a file of malformed lines
        # passed as long as every Pod in it happened to be terminal. Validating
        # first does not make a terminal Pod count -- the filter below is
        # unchanged -- it only stops a broken file from arriving unread.
        line_cpu, line_memory, line_storage = parse_request_quantities(requests, number)
        if not counts_against_capacity(phase, node_name):
            continue
        cpu += line_cpu
        memory += line_memory
        storage += line_storage
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
    # One "<cpu> <memory> <ephemeral-storage> <pods>" line per node, positional:
    # a position the node did not report is left empty, never collapsed.
    parser.add_argument("--node-allocatable-file", default="")
    # One "<phase>|<nodeName>|<cpu> <memory> <ephemeral-storage>" line per
    # container in the cluster.
    parser.add_argument("--cluster-requests-file", default="")
    # One "<phase>|<nodeName>" line per Pod in the cluster, for pod slots.
    parser.add_argument("--cluster-pods-file", default="")
    # One "<deployment>|<replicas>|<cpu>|<memory>|<ephemeral-storage>" line per
    # container the target slot already runs. Absent means the slot does not
    # exist yet, which is the first deploy of it.
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
    """Node allocatable totals, or all-unknown when the cluster reported nothing.

    A malformed line is not caught here any more. It used to be, and the result
    was that garbage and silence gave the same verdict -- which put a broken
    file behind the same operator override as an honest gap.
    """
    if text is None:
        return None, None, None
    return sum_node_allocatable(text)


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


def _sum_cluster_requests(path: str) -> tuple[int | None, int | None, int | None]:
    """Committed requests summed from a file, or all-unknown when unavailable.

    Absent, unreadable and EMPTY are the same answer on purpose. An empty file
    means the cluster query returned nothing -- no permission, no metrics, a
    kubectl that failed -- and reading that as a sum of zero is the dangerous
    interpretation in both directions: zero allocatable makes every candidate
    look too big, and zero committed makes every candidate look like it fits.
    A dimension the cluster did not supply is reported INCONCLUSIVE, never
    assumed.

    A file that is present and does not parse is a different thing again, and it
    is not softened here: InvalidClusterInput travels to the caller and ends the
    run as unusable input.
    """
    text = _read_text(path)
    if text is None:
        return None, None, None
    return sum_scheduled_pod_requests(text)


def _compare_quota(report: "Report", args: argparse.Namespace,
                   cpu: int, memory: int, storage: int, pods: int) -> None:
    """The four namespace-quota dimensions, in one place so that a quantity the
    quota reports outside the contract fails the same way every other one does."""
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
                   _optional(args.quota_hard_pods, parse_count),
                   _optional(args.quota_used_pods, parse_count), "")


def run(args: argparse.Namespace) -> int:
    # The current slot is read inside the guard, not before it. It goes through
    # the same quantity parser as everything else, and a negative or unreadable
    # request there would otherwise leave by way of a traceback.
    try:
        current = _read_current_slot(args.current_slot_file)
        with open(args.manifest, encoding="utf-8") as handle:
            manifest = handle.read()
        declared = workloads(manifest)
        cpu, memory, storage, pods = summarise(manifest, current)
    except (OSError, ValueError) as error:
        print(f"  [ERROR] candidate manifest or current slot is unusable: {error}",
              file=sys.stderr)
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
    try:
        _compare_quota(report, args, cpu, memory, storage, pods)
    except ValueError as error:
        print(f"  [ERROR] namespace quota is unusable: {error}", file=sys.stderr)
        return EXIT_BAD_INPUT
    # Read once, then parsed twice. The caller may pass a process substitution,
    # which yields nothing on a second open — reading it again is how the pod
    # dimension came back INCONCLUSIVE against a file that did report it.
    node_text = _read_text(args.node_allocatable_file)
    try:
        allocatable_cpu, allocatable_memory, allocatable_storage = _parse_nodes(node_text)
        pod_slots = None if node_text is None else sum_node_pod_slots(node_text)
        committed_cpu, committed_memory, committed_storage = _sum_cluster_requests(
            args.cluster_requests_file)
        scheduled_pods = _read_scheduled_pods(args.cluster_pods_file)
    except InvalidClusterInput as error:
        print(f"  [ERROR] {error}", file=sys.stderr)
        return EXIT_BAD_INPUT
    report.compare("cluster allocatable cpu", cpu, allocatable_cpu, committed_cpu, "m")
    report.compare("cluster allocatable memory", memory, allocatable_memory, committed_memory, "B")
    report.compare("cluster allocatable ephemeral-storage", storage,
                   allocatable_storage, committed_storage, "B")
    report.compare("cluster allocatable pods", pods, pod_slots, scheduled_pods, "")
    return report.exit_code()


def main(argv: list[str]) -> int:
    return run(build_parser().parse_args(argv))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
