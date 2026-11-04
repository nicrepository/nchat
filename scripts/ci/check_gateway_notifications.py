#!/usr/bin/env python3
"""Structural guard for the local Traefik routes of notification-service (#902).

notification-service serves its operational probes at /healthz, /readyz and
/version, and Web Push at the full /api/notifications/... path. So the gateway
must rewrite exactly the three probes and forward everything else untouched.

The check follows the configuration rather than middleware names: it resolves
the middlewares each notifications router really uses and rejects any that can
change the request path, whatever it is called.
"""

import re
import sys
from pathlib import Path

import yaml

PROBES = ("healthz", "readyz", "version")
PROBE_REGEX = "^/api/notifications/(healthz|readyz|version)$"
PROBE_REPLACEMENT = "/${1}"
# Every Traefik middleware that can alter the path a backend receives.
PATH_MIDDLEWARES = {"stripPrefix", "stripPrefixRegex", "replacePath", "replacePathRegex", "addPrefix"}
ENTRYPOINTS = ("web", "websecure")
PREFIX = "/api/notifications"


def _paths(rule: str) -> tuple[set[str], set[str]]:
    exact = set(re.findall(r"\bPath\(`([^`]+)`\)", rule))
    prefixes = set(re.findall(r"\bPathPrefix\(`([^`]+)`\)", rule))
    return exact, prefixes


SERVICE = "notification-service"
HEADERS = "security-headers"


def _chain_members(chain) -> list | None:
    """The middleware names a `chain` lists, or None when the definition is malformed."""
    members = chain.get("middlewares") if isinstance(chain, dict) else None
    return members if isinstance(members, list) else None


def _expand(ref, middlewares: dict, where: str, problems: list[str], trail: tuple) -> list[tuple[str, str, object]]:
    """What one middleware reference applies: its own kinds, or its chain's members."""
    name = str(ref).split("@")[0]
    definition = middlewares.get(name)
    if not isinstance(definition, dict):
        problems.append(f"{where}: middleware {ref!r} is not defined in this file")
        return []
    if name in trail:
        problems.append(f"{where}: middleware chain cycle through {name!r}")
        return []
    if "chain" not in definition:
        return [(name, kind, body) for kind, body in definition.items()]
    members = _chain_members(definition["chain"])
    if members is None:
        problems.append(f"{where}: chain {ref!r} is malformed")
        return []
    return _resolve(members, middlewares, where, problems, trail + (name,))


def _resolve(refs, middlewares: dict, where: str, problems: list[str], trail=()) -> list[tuple[str, str, object]]:
    """Every (middleware, kind, definition) a router applies, chains expanded."""
    if not isinstance(refs, list):
        return []
    return [item for ref in refs for item in _expand(ref, middlewares, where, problems, trail)]


# What Traefik accepts under a router's `tls`. Only an object turns TLS on: `tls:`
# left empty (null), false, a list or a string are read as "no TLS", and the
# websecure port then answers plain HTTP.
TLS_KEYS = {"options", "certResolver", "domains"}


def _terminates_tls(router: dict) -> bool:
    tls = router.get("tls")
    return isinstance(tls, dict) and set(tls) <= TLS_KEYS


def _has_security_headers(applied: list) -> bool:
    return any(m == HEADERS and kind == "headers" for m, kind, _ in applied)


def _check_common(name: str, router: dict, middlewares: dict, problems: list[str]) -> list[tuple[str, str, object]]:
    """What both kinds of notifications router must have; returns its path transformations."""
    refs = router.get("middlewares")
    applied = _resolve(refs, middlewares, name, problems)
    if router.get("service") != SERVICE:
        problems.append(f"{name}: must forward to {SERVICE}")
    if not _has_security_headers(applied):
        problems.append(f"{name}: must use the {HEADERS} middleware")
    if "websecure" in (router.get("entryPoints") or []) and not _terminates_tls(router):
        problems.append(f"{name}: must terminate TLS")
    if _effective_priority(router) is None:
        problems.append(f"{name}: priority must be an integer")
    return [(m, kind, body) for m, kind, body in applied if kind in PATH_MIDDLEWARES]


def _check_probe(name: str, router: dict, changers: list, problems: list[str]) -> None:
    exact, prefixes = _paths(router.get("rule", ""))
    if prefixes or exact != {f"{PREFIX}/{p}" for p in PROBES}:
        problems.append(f"{name}: a probe router must match exactly the paths {', '.join(PROBES)} under {PREFIX}")
    allowed = {"regex": PROBE_REGEX, "replacement": PROBE_REPLACEMENT}
    if len(changers) != 1 or changers[0][1:] != ("replacePathRegex", allowed):
        problems.append(f"{name}: exactly one path rewrite is required, replacePathRegex {PROBE_REGEX} -> {PROBE_REPLACEMENT}")


def _check_app(name: str, router: dict, changers: list, problems: list[str]) -> None:
    rule = router.get("rule", "")
    if rule.count("PathPrefix") != 1 or "Path(" in rule:
        problems.append(f"{name}: an application router must match only PathPrefix({PREFIX})")
    for used, kind, _ in changers:
        problems.append(f"{name}: {kind} middleware {used!r} would change the path Web Push routes depend on")


def _on(entry: str, routers) -> list[dict]:
    return [r for r in routers if entry in (r.get("entryPoints") or [])]


def _check_entrypoint(entry: str, probes: dict, apps: dict, problems: list[str]) -> None:
    probe, app = _on(entry, probes.values()), _on(entry, apps.values())
    if not probe:
        problems.append(f"entrypoint {entry}: no notifications probe router")
    if not app:
        problems.append(f"entrypoint {entry}: no notifications application router")
    app_priorities = [_effective_priority(a) for a in app]
    probe_priorities = [_effective_priority(p) for p in probe]
    if None in app_priorities or None in probe_priorities:
        return  # already reported by _check_common; nothing here can be compared
    if app and any(p <= max(app_priorities) for p in probe_priorities):
        problems.append(f"entrypoint {entry}: probe router priority must be above the application router's")


def _role(router: dict) -> str | None:
    """"app" for a router on the notifications prefix, "probe" for one on its probe paths."""
    exact, prefixes = _paths(str(router.get("rule", "")))
    if any(p == PREFIX or p.startswith(PREFIX + "/") for p in prefixes):
        return "app"
    return "probe" if any(p.startswith(PREFIX + "/") for p in exact) else None


def _classify(routers: dict, problems: list[str]) -> tuple[dict, dict]:
    """Split the routers that serve /api/notifications into probe and application routers."""
    probes: dict[str, dict] = {}
    apps: dict[str, dict] = {}
    for name, router in routers.items():
        if not isinstance(router, dict):
            problems.append(f"{name}: router is not a mapping")
        elif (role := _role(router)) == "app":
            apps[name] = router
        elif role == "probe":
            probes[name] = router
    return probes, apps


# A competing router is accepted only when it cannot outrank the application router
# or when its rule is PROVABLY disjoint from PREFIX. Sample URLs cannot show that (an
# exact regex can select a single path), so the rule is read structurally instead:
#   a && b   disjoint if ANY operand is (the router needs both to match)
#   a || b   disjoint if ALL operands are
#   !a       never proven, nor is any matcher this file does not understand
# Anything that does not parse is "not proven", which rejects the router.
_TOKEN = re.compile(r"\s*(?:(&&|\|\||!|\(|\))|(\w+)\(((?:\s*`[^`]*`\s*,?)*)\s*\))\s*")


def _tokenize(rule: str) -> list:
    tokens, pos = [], 0
    while pos < len(rule):
        match = _TOKEN.match(rule, pos)
        if not match:
            raise ValueError(rule)
        tokens.append(match.group(1) or (match.group(2), re.findall(r"`([^`]*)`", match.group(3))))
        pos = match.end()
    return tokens


# The only character class the regex prover reads: [abc] or [^abc] over plain
# characters. No `]` first, no `[`, no backslash, no `|` or parentheses inside, so
# removing these leaves no class behind and nothing a class could have hidden.
_PLAIN_CLASS = re.compile(r"\[\^?[A-Za-z0-9/_.-]+\]")


def _alternation_free(body: str) -> bool:
    """True only if `body`, read as RE2, has balanced groups and no top-level `|`.

    Sound because anything that could change what `|`, `(` or `)` mean is refused
    before counting: every backslash (escapes, \\Q...\\E quoting, \\x28), every `[`
    that is not a plain class (POSIX classes, a leading `]`, escaped brackets), and
    every `(?`: a flag group such as (?i) is not an atom, so a quantifier after it
    would apply to the literal before it.
    """
    flat = _PLAIN_CLASS.sub("", body)
    if "\\" in flat or "[" in flat or "(?" in flat:
        return False
    depth = 0
    for char in flat:
        if char == "|" and depth == 0:
            return False
        depth += (char == "(") - (char == ")")
        if depth < 0:
            return False
    return depth == 0


def _regexp_disjoint(pattern: str) -> bool:
    """True only when RE2 cannot match any path under PREFIX; False means "not proven".

    Proven only for `^<literal><body>`: the literal is plain characters, can be
    neither under PREFIX nor a prefix of it, and the body has no top-level
    alternation, so every match must start with that literal. A literal followed
    by a quantifier (?, *, + or {) loses its last character, which the quantifier
    may make optional; + is included so that no repetition is ever reasoned about.
    """
    literal = re.match(r"\^([A-Za-z0-9/_-]*)", pattern)
    if not literal:
        return False
    body = pattern[literal.end():]
    text = literal.group(1)[:-1] if body[:1] in ("?", "*", "+", "{") else literal.group(1)
    if not text or text.startswith(PREFIX) or PREFIX.startswith(text):
        return False
    return _alternation_free(body)


def _host_requests(host: str) -> set[str]:
    """Every canonical request host that Traefik v3.6's Host(`host`) accepts.

    pkg/muxer/http/matcher.go host(): the rule host is lowercased, the request host
    is lowercased and trimmed (types.CanonicalDomain), and one trailing period is
    allowed on either side: a rule "h." accepts "h", a rule "h" accepts "h.". (CNAME
    flattening, a static hostResolver option, is not configured for the local
    gateway and is not modelled.)
    """
    host = host.lower()
    accepted = {host, host + "."}
    if host.endswith("."):
        accepted.add(host[:-1])
    return accepted


def _hosts_disjoint(args: list[str], hosts: set[str]) -> bool:
    return bool(hosts) and all(a.isascii() and _host_requests(a).isdisjoint(hosts) for a in args)


def _matcher_disjoint(name: str, args: list[str], hosts: set[str]) -> bool:
    """Whether one matcher alone proves the rule disjoint; `hosts` are the application's request hosts."""
    if not args:
        return False
    if name == "Host":
        return _hosts_disjoint(args, hosts)
    if name == "Path":
        return not any(a.startswith(PREFIX) for a in args)
    if name == "PathPrefix":
        return not any(a.startswith(PREFIX) or PREFIX.startswith(a) for a in args)
    return name == "PathRegexp" and all(_regexp_disjoint(a) for a in args)


class _Rule:
    """Recursive reading of a Traefik rule for `_provably_disjoint`: ! binds tighter than &&, && than ||."""

    def __init__(self, tokens: list, hosts: set[str]):
        self.tokens, self.hosts, self.at = tokens, hosts, 0

    def peek(self):
        return self.tokens[self.at] if self.at < len(self.tokens) else None

    def take(self):
        token = self.peek()
        if token is None:
            raise ValueError("unexpected end of rule")
        self.at += 1
        return token

    def any_or_all(self, operator: str, operand, combine) -> bool:
        results = [operand()]
        while self.peek() == operator:
            self.take()
            results.append(operand())
        return combine(results)

    def disjoint(self) -> bool:
        return self.any_or_all("||", self.conjunction, all)

    def conjunction(self) -> bool:
        return self.any_or_all("&&", self.operand, any)

    def operand(self) -> bool:
        token = self.take()
        if token == "!":
            self.operand()
            return False
        if token == "(":
            inner = self.disjoint()
            if self.take() != ")":
                raise ValueError("unbalanced parenthesis")
            return inner
        if not isinstance(token, tuple):
            raise ValueError(f"unexpected {token!r}")
        return _matcher_disjoint(token[0], token[1], self.hosts)


def _provably_disjoint(rule: str, app_hosts: set[str]) -> bool:
    try:
        reader = _Rule(_tokenize(rule), app_hosts)
        proven = reader.disjoint()
        return proven and reader.peek() is None
    except ValueError:
        return False


def _effective_priority(router: dict) -> int | None:
    """The priority Traefik v3.6 gives the router, or None when it is not a plain integer.

    Measured against v3.6: a missing priority, null and 0 all become len(rule); a
    negative value is kept. Anything else is refused here, never guessed: a numeric
    string is read by Traefik ("300" is 300), and bool is an int only to Python.
    len(rule) is Go's (GetRulePriority in pkg/muxer/http/mux.go), so it counts the
    UTF-8 bytes of the rule, not its characters.
    """
    priority = router.get("priority")
    if priority is not None and type(priority) is not int:
        return None
    return priority or len(str(router.get("rule", "")).encode("utf-8", "surrogatepass"))


def _public_entrypoints(router: dict) -> bool:
    """Whether the router listens on web or websecure; anything unreadable counts as yes."""
    points = router.get("entryPoints")
    return not isinstance(points, list) or not points or bool(set(points) & set(ENTRYPOINTS))


def _outranked(router: dict, floor: int | None) -> bool:
    """Proven to lose to every application router; an unreadable priority proves nothing."""
    priority = _effective_priority(router)
    return priority is not None and floor is not None and priority < floor


def _can_shadow(router: dict, app_hosts: set[str], floor: int | None) -> bool:
    return (
        _public_entrypoints(router)
        and not _outranked(router, floor)
        and not _provably_disjoint(str(router.get("rule", "")), app_hosts)
    )


def _check_shadowing(routers: dict, probes: dict, apps: dict, problems: list[str]) -> None:
    """No other router on these entrypoints may be able to outrank the application router for PREFIX."""
    if not apps:
        return
    priorities = [_effective_priority(r) for r in apps.values()]
    floor = None if None in priorities else min(priorities)
    app_hosts = _application_hosts(apps)
    for name, router in routers.items():
        if name not in apps and name not in probes and isinstance(router, dict) and _can_shadow(router, app_hosts, floor):
            problems.append(f"{name}: can select {PREFIX}/* at priority {_effective_priority(router)}, not below the application router's {floor}, and is not provably disjoint from it")


_APP_HOST_RULE = re.compile(r"Host\(`([^`]+)`\)\s*&&\s*PathPrefix\(`" + re.escape(PREFIX) + r"`\)")


def _application_hosts(apps: dict) -> set[str]:
    """The request hosts the application routers accept, or none when any of them is not restricted.

    Only the exact form Host(`h`) && PathPrefix(PREFIX) counts: a router without a
    Host, or with Host under || or !, answers on other hosts too, and then no Host
    elsewhere can prove a router disjoint.
    """
    hosts = set()
    for router in apps.values():
        match = _APP_HOST_RULE.fullmatch(str(router.get("rule", "")).strip())
        if not match:
            return set()
        hosts |= _host_requests(match.group(1))
    return hosts


def audit(config) -> list[str]:
    http = config.get("http") if isinstance(config, dict) else None
    routers = http.get("routers") if isinstance(http, dict) else None
    middlewares = http.get("middlewares") if isinstance(http, dict) else None
    if not isinstance(routers, dict) or not isinstance(middlewares, dict):
        return ["http.routers and http.middlewares must both be mappings"]
    problems: list[str] = []
    probes, apps = _classify(routers, problems)
    for name, router in apps.items():
        _check_app(name, router, _check_common(name, router, middlewares, problems), problems)
    for name, router in probes.items():
        _check_probe(name, router, _check_common(name, router, middlewares, problems), problems)
    for entry in ENTRYPOINTS:
        _check_entrypoint(entry, probes, apps, problems)
    _check_shadowing(routers, probes, apps, problems)
    return problems


def main(argv: list[str]) -> int:
    path = Path(argv[1]) if len(argv) > 1 else Path(__file__).resolve().parents[2] / "infra/traefik/local/dynamic.yml"
    try:
        problems = audit(yaml.safe_load(path.read_text()))
    except (OSError, yaml.YAMLError) as error:
        problems = [f"cannot read {path.name}: {type(error).__name__}"]
    for problem in problems:
        print(f"gateway notifications check: {problem}", file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
