#!/usr/bin/env python3

import copy
import importlib.util
import unittest
from pathlib import Path

import yaml

SCRIPT_PATH = Path(__file__).with_name("check_gateway_notifications.py")
DYNAMIC = Path(__file__).resolve().parents[2] / "infra/traefik/local/dynamic.yml"
SPEC = importlib.util.spec_from_file_location("check_gateway_notifications", SCRIPT_PATH)
assert SPEC and SPEC.loader
check = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check)

PROBE_RULE = "Host(`nchat.local`) && (Path(`/api/notifications/healthz`) || Path(`/api/notifications/readyz`) || Path(`/api/notifications/version`))"
SERVICE = "notification-service"
PREFIX = "/api/notifications"
APP_RULE = "Host(`nchat.local`) && PathPrefix(`/api/notifications`)"
PROBE_MIDDLEWARE = {
    "replacePathRegex": {
        "regex": "^/api/notifications/(healthz|readyz|version)$",
        "replacement": "/${1}",
    }
}


def healthy() -> dict:
    """The smallest gateway the check accepts: probes rewritten, app routes verbatim."""
    routers = {}
    for suffix, entry in (("", "web"), ("-https", "websecure")):
        routers[f"notifications-probe{suffix}"] = {
            "entryPoints": [entry],
            "rule": PROBE_RULE,
            "service": SERVICE,
            "priority": 200,
            "middlewares": ["security-headers", "compress", "probe-rewrite"],
        }
        if entry == "websecure":
            routers[f"notifications-probe{suffix}"]["tls"] = {}
        routers[f"notifications-app{suffix}"] = {
            "entryPoints": [entry],
            "rule": APP_RULE,
            "service": SERVICE,
            "priority": 100,
            "middlewares": ["security-headers", "compress"],
        }
        if entry == "websecure":
            routers[f"notifications-app{suffix}"]["tls"] = {}
    return {
        "http": {
            "routers": routers,
            "middlewares": {
                "security-headers": {"headers": {"frameDeny": True}},
                "compress": {"compress": {}},
                "probe-rewrite": copy.deepcopy(PROBE_MIDDLEWARE),
            },
        }
    }


class NotificationsGatewayTest(unittest.TestCase):
    def test_the_committed_gateway_is_accepted(self):
        self.assertEqual(check.audit(yaml.safe_load(DYNAMIC.read_text())), [])

    def test_the_healthy_fixture_is_accepted(self):
        self.assertEqual(check.audit(healthy()), [])

    def assertRejected(self, config: dict, fragment: str):
        problems = check.audit(config)
        self.assertTrue(any(fragment in p for p in problems), f"{fragment!r} not in {problems}")

    def test_a_renamed_strip_prefix_on_the_app_router_is_rejected(self):
        config = healthy()
        config["http"]["middlewares"]["innocent-name"] = {"stripPrefix": {"prefixes": ["/api/notifications"]}}
        config["http"]["routers"]["notifications-app"]["middlewares"].append("innocent-name")
        self.assertRejected(config, "notifications-app")

    def test_a_renamed_rewrite_on_the_https_app_router_is_rejected(self):
        config = healthy()
        config["http"]["middlewares"]["x"] = {"replacePathRegex": {"regex": "^/api/notifications/(.*)", "replacement": "/${1}"}}
        config["http"]["routers"]["notifications-app-https"]["middlewares"].append("x")
        self.assertRejected(config, "notifications-app-https")

    def test_add_prefix_and_replace_path_on_the_app_router_are_rejected(self):
        for kind in ({"addPrefix": {"prefix": "/x"}}, {"replacePath": {"path": "/x"}}, {"stripPrefixRegex": {"regex": ["^/api"]}}):
            config = healthy()
            config["http"]["middlewares"]["m"] = kind
            config["http"]["routers"]["notifications-app"]["middlewares"].append("m")
            self.assertRejected(config, "notifications-app")

    def test_a_probe_rewrite_wider_than_the_three_probes_is_rejected(self):
        config = healthy()
        config["http"]["middlewares"]["probe-rewrite"]["replacePathRegex"]["regex"] = "^/api/notifications/(.*)"
        self.assertRejected(config, "notifications-probe")

    def test_a_probe_router_matching_more_than_the_probes_is_rejected(self):
        config = healthy()
        config["http"]["routers"]["notifications-probe"]["rule"] = APP_RULE
        self.assertRejected(config, "notifications-probe")

    def test_a_probe_router_not_above_the_app_router_is_rejected(self):
        config = healthy()
        config["http"]["routers"]["notifications-probe-https"]["priority"] = 100
        self.assertRejected(config, "priority")

    def test_a_missing_probe_router_for_an_entrypoint_is_rejected(self):
        config = healthy()
        del config["http"]["routers"]["notifications-probe-https"]
        self.assertRejected(config, "websecure")

    def test_a_missing_app_router_for_an_entrypoint_is_rejected(self):
        config = healthy()
        del config["http"]["routers"]["notifications-app"]
        self.assertRejected(config, "entrypoint web:")

    def test_an_undefined_middleware_is_rejected(self):
        config = healthy()
        config["http"]["routers"]["notifications-app"]["middlewares"].append("ghost")
        self.assertRejected(config, "ghost")

    # CQ-902-004 / SEC-902-001: a router that carries nothing wrong is still wrong
    # when it lacks what the contract requires.
    def mutate(self, router: str, change) -> dict:
        config = healthy()
        change(config["http"]["routers"][router], config["http"]["middlewares"])
        return config

    def test_a_probe_router_without_the_rewrite_is_rejected(self):
        for router in ("notifications-probe", "notifications-probe-https"):
            config = self.mutate(router, lambda r, m: r["middlewares"].remove("probe-rewrite"))
            self.assertRejected(config, f"{router}: exactly one path rewrite")

    def test_a_router_on_another_service_is_rejected(self):
        for router in ("notifications-probe", "notifications-probe-https", "notifications-app", "notifications-app-https"):
            config = self.mutate(router, lambda r, m: r.update(service="chat-service"))
            self.assertRejected(config, f"{router}: must forward to {SERVICE}")

    def test_a_router_without_security_headers_is_rejected(self):
        for router in ("notifications-probe", "notifications-probe-https", "notifications-app", "notifications-app-https"):
            config = self.mutate(router, lambda r, m: r["middlewares"].remove("security-headers"))
            self.assertRejected(config, f"{router}: must use the security-headers middleware")

    def test_a_security_headers_name_without_a_headers_definition_is_rejected(self):
        config = healthy()
        config["http"]["middlewares"]["security-headers"] = {"compress": {}}
        for router in ("notifications-probe", "notifications-probe-https", "notifications-app", "notifications-app-https"):
            self.assertRejected(config, f"{router}: must use the security-headers middleware")

    def test_a_second_path_transformation_on_a_probe_router_is_rejected(self):
        def add_second(router, middlewares):
            middlewares["second"] = {"addPrefix": {"prefix": "/x"}}
            router["middlewares"].append("second")
        self.assertRejected(self.mutate("notifications-probe", add_second), "notifications-probe")

    def test_a_chain_cannot_hide_a_path_transformation(self):
        def hide(router, middlewares):
            middlewares["inner"] = {"stripPrefix": {"prefixes": ["/api/notifications"]}}
            middlewares["outer"] = {"chain": {"middlewares": ["inner"]}}
            router["middlewares"].append("outer")
        self.assertRejected(self.mutate("notifications-app", hide), "notifications-app")

    def test_a_chain_cycle_or_malformed_chain_is_rejected(self):
        def cycle(router, middlewares):
            middlewares["a"] = {"chain": {"middlewares": ["b"]}}
            middlewares["b"] = {"chain": {"middlewares": ["a"]}}
            router["middlewares"].append("a")
        self.assertRejected(self.mutate("notifications-app", cycle), "cycle")

        def malformed(router, middlewares):
            middlewares["bad"] = {"chain": {"middlewares": "inner"}}
            router["middlewares"].append("bad")
        self.assertRejected(self.mutate("notifications-app", malformed), "bad")

    def test_a_https_router_without_tls_is_rejected(self):
        config = self.mutate("notifications-app-https", lambda r, m: r.pop("tls"))
        self.assertRejected(config, "notifications-app-https: must terminate TLS")

    # CQ-902-005 / SEC-902-002: Traefik reads `tls: null` (and the other non-objects)
    # as "no TLS", so the websecure port would speak plain HTTP. Presence of the key
    # is not enough; it has to be a TLS object.
    HTTPS_ROUTERS = ("notifications-probe-https", "notifications-app-https")

    def test_a_tls_value_that_is_not_an_object_is_rejected(self):
        for router in self.HTTPS_ROUTERS:
            for bad in (None, False, True, [], ["x"], "", "true", 0):
                config = self.mutate(router, lambda r, m: r.update(tls=bad))
                self.assertRejected(config, f"{router}: must terminate TLS")

    def test_an_empty_or_optioned_tls_object_is_accepted(self):
        for tls in ({}, {"options": "default"}, {"certResolver": "le"}, {"domains": [{"main": "nchat.local"}]}):
            for router in self.HTTPS_ROUTERS:
                config = self.mutate(router, lambda r, m: r.update(tls=tls))
                self.assertEqual(check.audit(config), [], tls)

    def test_a_tls_object_with_unknown_keys_is_rejected(self):
        for router in self.HTTPS_ROUTERS:
            config = self.mutate(router, lambda r, m: r.update(tls={"passthrough": True}))
            self.assertRejected(config, f"{router}: must terminate TLS")

    # Routers the guard does not classify can still select /api/notifications/*.
    # Verified against Traefik v3.6: a PathRegexp router with a higher priority and
    # a stripPrefix delivered "/push/config" to the backend. Any other router that
    # can match these paths on the same host and entrypoints must not be able to win.
    def add_router(self, **fields) -> dict:
        config = healthy()
        config["http"]["middlewares"]["strip"] = {"stripPrefix": {"prefixes": [PREFIX]}}
        router = {"entryPoints": ["web"], "service": SERVICE, "middlewares": ["strip"], "priority": 300}
        router.update(fields)
        config["http"]["routers"]["extra"] = router
        return config

    def test_an_unclassified_router_that_can_shadow_the_notifications_routes_is_rejected(self):
        host = "Host(`nchat.local`) && "
        for rule, extra in (
            (host + "PathRegexp(`^/api/(notifications|x)/.*`)", {}),
            (host + "PathRegexp(`^/api/.*`)", {}),
            (host + "PathPrefix(`/api`)", {}),
            ("Host(`nchat.local`)", {}),
            (host + "PathPrefix(`/api`) && " + "Header(`X-A`, `" + "a" * 120 + "`)", {"priority": None}),
        ):
            config = self.add_router(rule=rule, **extra)
            if config["http"]["routers"]["extra"]["priority"] is None:
                del config["http"]["routers"]["extra"]["priority"]
            self.assertRejected(config, "extra: can select")

    def test_an_exact_path_router_on_the_prefix_is_held_to_the_probe_contract(self):
        config = self.add_router(rule="Host(`nchat.local`) && Path(`/api/notifications/push/config`)")
        self.assertRejected(config, "extra: a probe router must match exactly")

    def test_routers_that_cannot_win_or_cannot_match_are_accepted(self):
        host = "Host(`nchat.local`) && "
        for rule, extra in (
            (host + "PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)", {}),
            (host + "PathPrefix(`/api`)", {"priority": 1}),
            ("Host(`admin.nchat.local`) && PathPrefix(`/api`)", {}),
            (host + "PathPrefix(`/api`)", {"entryPoints": ["dashboard"]}),
        ):
            self.assertEqual(check.audit(self.add_router(rule=rule, **extra)), [], rule)

    # CQ-902-008 / SEC-902-003: a competing router is accepted only when it is
    # PROVABLY disjoint from /api/notifications, or cannot outrank the application
    # router. A list of sample URLs cannot show that (an exact regex can match a
    # single path), and neither can a textual Host() inside a negation or an OR.
    HOST = "Host(`nchat.local`) && "
    SUBSCRIPTION = "/api/notifications/push/subscriptions/0b0e2a58-4c1f-4a52-8f7e-7c5d1f0f9a11"

    def assertShadows(self, **fields):
        self.assertRejected(self.add_router(**fields), "extra: can select")

    def test_an_exact_pathregexp_cannot_take_any_single_route(self):
        for path in (
            "/api/notifications/healthz",
            "/api/notifications/readyz",
            "/api/notifications/version",
            "/api/notifications/push/config",
            "/api/notifications/push/subscriptions",
            self.SUBSCRIPTION,
        ):
            with self.subTest(path=path):
                self.assertShadows(rule=self.HOST + f"PathRegexp(`^{path}$`)", priority=300)

    def test_a_host_that_only_looks_foreign_does_not_make_a_router_disjoint(self):
        for rule in (
            "!Host(`admin.nchat.local`) && PathPrefix(`/api`)",
            "Host(`other.local`) || PathPrefix(`/api`)",
            "!(Host(`admin.nchat.local`)) && PathPrefix(`/api`)",
            "HostRegexp(`^.+\\.local$`) && PathPrefix(`/api`)",
            "(Host(`other.local`) || Host(`nchat.local`)) && PathPrefix(`/api`)",
        ):
            with self.subTest(rule=rule):
                self.assertShadows(rule=rule, priority=300)

    def test_priority_ties_implicit_priority_and_the_band_between_are_conservative(self):
        self.assertShadows(rule=self.HOST + "PathPrefix(`/api`)", priority=100)
        self.assertShadows(rule=self.HOST + "PathPrefix(`/api`)", priority=150)
        self.assertShadows(rule=self.HOST + "PathPrefix(`/api`)", priority=200)
        long_rule = self.HOST + "PathRegexp(`^/api/(notifications|x)/" + "a" * 90 + "`)"
        config = self.add_router(rule=long_rule)
        del config["http"]["routers"]["extra"]["priority"]
        self.assertRejected(config, "extra: can select")

    def test_both_public_entrypoints_and_unreadable_entrypoints_are_covered(self):
        for entry_points in (["web"], ["websecure"], ["web", "websecure"], "web", None):
            with self.subTest(entry_points=entry_points):
                self.assertShadows(rule=self.HOST + "PathPrefix(`/api`)", priority=300, entryPoints=entry_points)

    def test_regexes_that_are_not_clearly_anchored_to_another_namespace_are_rejected(self):
        for pattern in (
            "^/api/notif",
            "^/api/(notifications|files)/.*",
            "/api/files",
            "(?i)^/api/files",
            "^/api/files|^/api/notifications/x",
            "^/(.*)",
            "^/api/notifications",
            "^/api/notificationsX",
            "(",
        ):
            with self.subTest(pattern=pattern):
                self.assertShadows(rule=self.HOST + f"PathRegexp(`{pattern}`)", priority=300)

    def test_unparseable_rules_are_rejected(self):
        for rule in ("", "Host(`nchat.local`) &&", "Host(`nchat.local`) && ((PathPrefix(`/api/chat`)", "PathPrefix(`/api/chat`) garbage", "PathPrefix(`/api/chat`) &&& Method(`GET`)"):
            with self.subTest(rule=rule):
                self.assertShadows(rule=rule, priority=300)

    def test_provably_disjoint_or_outranked_routers_are_accepted(self):
        for rule, extra in (
            (self.HOST + "PathPrefix(`/api/chat`)", {}),
            (self.HOST + "Path(`/api/auth/healthz`)", {}),
            (self.HOST + "PathPrefix(`/api/files`) && Method(`POST`)", {}),
            (self.HOST + "PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)", {}),
            (self.HOST + "(Path(`/api/chat/healthz`) || Path(`/api/chat/readyz`))", {}),
            ("Host(`admin.nchat.local`) && PathPrefix(`/api`)", {}),
            ("Host(`admin.nchat.local`, `other.local`)", {}),
            (self.HOST + "PathPrefix(`/api`)", {"priority": 99}),
            (self.HOST + "PathPrefix(`/api`)", {"entryPoints": ["dashboard"]}),
            (self.HOST + "PathPrefix(`/`)", {"priority": 1}),
        ):
            with self.subTest(rule=rule, extra=extra):
                self.assertEqual(check.audit(self.add_router(rule=rule, **({"priority": 300} | extra))), [], rule)

    def test_surviving_mutant_regressions(self):
        # A negation proves nothing, whatever it negates.
        for rule in (
            "!PathPrefix(`/api/chat`)",
            "!Path(`/api/chat`) && Method(`GET`)",
            "!Host(`other.local`)",
            "!PathRegexp(`^/api/notifications/readyz$`)",  # matches everything BUT one probe
        ):
            with self.subTest(rule=rule):
                self.assertShadows(rule=rule, priority=300)
        # Host names are case-insensitive: NCHAT.local is the application host.
        self.assertShadows(rule="Host(`NCHAT.local`) && PathPrefix(`/api`)", priority=300)
        # An exact Path on the prefix itself is still inside the namespace.
        self.assertShadows(rule=self.HOST + "Path(`/api/notifications`)", priority=300)
        # A regex whose last literal character is optional does not require it.
        for pattern in ("^/api/notificationX?", "^/api/notificationX*", "^/api/notification{0,1}"):
            with self.subTest(pattern=pattern):
                self.assertShadows(rule=self.HOST + f"PathRegexp(`{pattern}`)", priority=300)
        # A stray closing parenthesis after a disjoint matcher is not a disjoint rule.
        self.assertShadows(rule="PathPrefix(`/api/chat`))", priority=300)

    def test_no_host_on_the_application_routers_means_no_host_is_foreign(self):
        config = self.add_router(rule="Host(`other.local`)", priority=300)
        for name in ("notifications-app", "notifications-app-https"):
            config["http"]["routers"][name]["rule"] = "PathPrefix(`/api/notifications`)"
        self.assertRejected(config, "extra: can select")

    # CQ-902-009 / SEC-902-004 (A): Traefik v3.6 reads priority 0, a missing priority
    # and null as len(rule), keeps negatives, and parses a numeric string ("300" is
    # 300). Measured against Traefik v3.6 through its API.
    SHADOW_ZERO = "Host(`nchat.local`) && PathPrefix(`/api`) && (Method(`GET`) || Method(`POST`) || Method(`DELETE`) || Method(`HEAD`))"

    def test_priority_zero_null_and_missing_mean_the_rule_length(self):
        self.assertEqual(len(self.SHADOW_ZERO), 116)
        for priority in (0, None, "absent"):
            with self.subTest(priority=priority):
                config = self.add_router(rule=self.SHADOW_ZERO, priority=priority, service="chat-service", middlewares=[])
                if priority == "absent":
                    del config["http"]["routers"]["extra"]["priority"]
                self.assertRejected(config, "extra: can select")

    def test_a_priority_that_is_not_a_plain_integer_proves_nothing(self):
        for priority in ("500", "1", True, False, 1.5, [1], {"p": 1}):
            with self.subTest(priority=priority):
                self.assertShadows(rule=self.HOST + "PathPrefix(`/api`)", priority=priority)

    def test_positive_and_equal_priorities_against_the_application_router(self):
        self.assertShadows(rule=self.HOST + "PathPrefix(`/api`)", priority=101)
        self.assertShadows(rule=self.HOST + "PathPrefix(`/api`)", priority=100)
        self.assertEqual(check.audit(self.add_router(rule=self.HOST + "PathPrefix(`/api`)", priority=-5)), [])

    def test_probe_priority_is_compared_with_the_effective_application_priority(self):
        app_length = len(APP_RULE)
        for app_priority in ("absent", 0, None):
            with self.subTest(app_priority=app_priority):
                config = self.mutate("notifications-app", lambda r, m: r.update(priority=app_priority))
                if app_priority == "absent":
                    del config["http"]["routers"]["notifications-app"]["priority"]
                config["http"]["routers"]["notifications-probe"]["priority"] = app_length - 1
                self.assertRejected(config, "entrypoint web: probe router priority must be above")

    def test_an_implicit_probe_priority_above_the_application_router_is_accepted(self):
        self.assertGreater(len(PROBE_RULE), 100)
        config = self.mutate("notifications-probe", lambda r, m: r.pop("priority"))
        self.assertEqual(check.audit(config), [])

    def test_a_notifications_router_priority_must_be_a_plain_integer(self):
        for router in ("notifications-probe", "notifications-app-https"):
            for priority in ("300", True, 1.5):
                with self.subTest(router=router, priority=priority):
                    config = self.mutate(router, lambda r, m: r.update(priority=priority))
                    self.assertRejected(config, f"{router}: priority must be an integer")

    # (B): `_regexp_disjoint` may return True only when RE2 cannot reach PREFIX.
    # Every pattern here compiles in Go's regexp and matches a protected path
    # (verified with Go 1.25 regexp, the engine Traefik uses).
    RE2_ALTERNATION_HIDERS = (
        r"^/api/files/\[|/api/notifications/[^/]+",
        r"^/api/files/\Q(\E|^/api/notifications/push/config$|\Q)\E",
        r"^/api/files/[](]|^/api/notifications/push/config$|[])]",
        r"^/api/files/[^]|]|^/api/notifications/push/config$",
        r"^/api/files/[[:alpha:]|]|^/api/notifications/push/config$",
        r"^/api/files/\x28|^/api/notifications/push/config$",
        r"^/api/files/(?:x|y)|^/api/notifications/readyz$",
    )

    def test_re2_escapes_quoting_and_classes_cannot_hide_a_top_level_alternation(self):
        for pattern in self.RE2_ALTERNATION_HIDERS:
            with self.subTest(pattern=pattern):
                self.assertFalse(check._regexp_disjoint(pattern))
                self.assertShadows(rule=self.HOST + f"PathRegexp(`{pattern}`)", priority=300)

    # Found by differential fuzzing against Go's regexp: a flag group is not an
    # atom, so a quantifier after it applies to the literal's last character.
    RE2_FLAG_GROUP_QUANTIFIERS = (
        "^/x(?i)?",
        "^/x(?i)*",
        "^/x(?i)?a",
        "^/api/notifn(?i)?",
        "^/aQ+(?i)*",
        "^/a-(?i)?(?i)",
        "^/api/files(?s)?",
        "^/api/filesx(?-i){0}",
    )

    def test_a_quantifier_behind_a_flag_group_is_never_proof(self):
        for pattern in self.RE2_FLAG_GROUP_QUANTIFIERS:
            with self.subTest(pattern=pattern):
                self.assertFalse(check._regexp_disjoint(pattern))

    def test_simple_anchored_regexes_of_other_services_still_prove_disjointness(self):
        for pattern in (
            "^/api/files/(channels|dm)/[^/]+/attachments$",
            "^/api/chat/[a-z0-9-]+$",
            "^/api/files/(a|(b|c))/x$",
        ):
            with self.subTest(pattern=pattern):
                self.assertTrue(check._regexp_disjoint(pattern))

    def test_regex_constructs_the_prover_does_not_read_are_never_proof(self):
        for pattern in (
            r"^/api/files/\d+$",
            r"^/api/files/\.x$",
            "^/api/files/[[:alpha:]]+$",
            "^/api/files/[a-z\\]]+$",
            "^/api/files/[]a]$",
            "^/api/files/[^]a]$",
            "^/api/files/(x",
            "^/api/files/x)",
            "^/api/files/x)(",
            "^/api/files/[a-z",
        ):
            with self.subTest(pattern=pattern):
                self.assertFalse(check._regexp_disjoint(pattern))

    # Secondary: a host restriction counts only when EVERY application router has
    # one, in the exact form Host(`h`) && PathPrefix(PREFIX).
    def test_an_application_router_without_a_host_restriction_lets_no_host_prove_anything(self):
        for app_rule in ("PathPrefix(`/api/notifications`)", "Host(`nchat.local`) || PathPrefix(`/api/notifications`)"):
            with self.subTest(app_rule=app_rule):
                config = self.add_router(rule="Host(`other.local`) && PathPrefix(`/api`)", priority=300, entryPoints=["websecure"])
                config["http"]["routers"]["notifications-app-https"]["rule"] = app_rule
                self.assertRejected(config, "extra: can select")

    def test_an_unreadable_application_priority_lets_no_priority_dismiss_a_router(self):
        # Traefik reads "50" as 50, so a competing router at 75 may win.
        config = self.add_router(rule=self.HOST + "PathPrefix(`/api`)", priority=75)
        config["http"]["routers"]["notifications-app"]["priority"] = "50"
        self.assertRejected(config, "extra: can select")

    def test_a_host_restriction_widened_by_or_is_not_a_restriction(self):
        config = self.add_router(rule="Host(`other.local`) && PathPrefix(`/api`)", priority=300)
        config["http"]["routers"]["notifications-app"]["rule"] = APP_RULE + " || Host(`other.local`)"
        self.assertRejected(config, "extra: can select")

    # CQ-902-010 / SEC-902-005 (A): Traefik v3.6 computes the implicit priority with
    # Go's len(rule), which counts BYTES (pkg/muxer/http/mux.go GetRulePriority).
    UNICODE_RULE = "Host(`nchat.local`) && PathRegexp(`^/api/(notifications|" + "\u00e9" * 25 + ")/`)"
    ASCII_TWIN = "Host(`nchat.local`) && PathRegexp(`^/api/(notifications|" + "e" * 25 + ")/`)"

    def test_the_implicit_priority_of_a_unicode_rule_is_its_utf8_length(self):
        self.assertEqual((len(self.UNICODE_RULE), len(self.UNICODE_RULE.encode("utf-8"))), (85, 110))
        for priority in (0, None, "absent"):
            with self.subTest(priority=priority):
                config = self.add_router(rule=self.UNICODE_RULE, priority=priority, service="chat-service", middlewares=[])
                if priority == "absent":
                    del config["http"]["routers"]["extra"]["priority"]
                self.assertRejected(config, "extra: can select")

    def test_unicode_priority_controls(self):
        # The same rule in ASCII is 85 bytes and cannot outrank 100.
        self.assertEqual(check.audit(self.add_router(rule=self.ASCII_TWIN, priority=0)), [])
        # Explicit priorities are taken as declared, whatever the rule contains.
        self.assertShadows(rule=self.UNICODE_RULE, priority=300)
        self.assertEqual(check.audit(self.add_router(rule=self.UNICODE_RULE, priority=-5)), [])
        # 65 characters, 70 bytes: still below the application router.
        short = "Host(`nchat.local`) && PathRegexp(`^/api/(notifications|" + "\u00e9" * 5 + ")/`)"
        self.assertEqual(len(short.encode("utf-8")), 70)
        self.assertEqual(check.audit(self.add_router(rule=short, priority=0)), [])

    # (B): Traefik's Host matcher lowercases the rule host and accepts one trailing
    # period on either side (pkg/muxer/http/matcher.go host()); the request host is
    # CanonicalDomain'd (lowercased, trimmed). Two hosts are disjoint only when the
    # sets of request hosts they accept do not meet.
    VERSION_REGEX = " && PathRegexp(`^/api/notifications/version$`)"

    def with_app_host(self, app_host: str, rule: str) -> dict:
        config = self.add_router(rule=rule, priority=300)
        for name in ("notifications-app", "notifications-app-https"):
            config["http"]["routers"][name]["rule"] = f"Host(`{app_host}`) && PathPrefix(`/api/notifications`)"
        return config

    def test_a_trailing_period_does_not_make_a_host_foreign(self):
        for app_host, competing in (
            ("nchat.local", "nchat.local."),
            ("nchat.local.", "nchat.local"),
            ("nchat.local.", "nchat.local."),
            ("nchat.local", "NCHAT.LOCAL."),
            ("NCHAT.local.", "nchat.LOCAL"),
            ("nchat.local", "nchat.local.."),  # both accept the request host "nchat.local."
        ):
            with self.subTest(app_host=app_host, competing=competing):
                config = self.with_app_host(app_host, f"Host(`{competing}`)" + self.VERSION_REGEX)
                self.assertRejected(config, "extra: can select")

    def test_a_host_that_really_differs_is_still_foreign(self):
        for app_host in ("nchat.local", "nchat.local."):
            for competing in ("other.local", "other.local.", "nchat.loca", "nchat.localx", "admin.nchat.local."):
                with self.subTest(app_host=app_host, competing=competing):
                    config = self.with_app_host(app_host, f"Host(`{competing}`)" + self.VERSION_REGEX)
                    self.assertEqual(check.audit(config), [])

    def test_trailing_periods_keep_the_boolean_semantics(self):
        for rule in (
            "Host(`other.local.`) || PathPrefix(`/api`)",
            "!Host(`admin.nchat.local.`) && PathPrefix(`/api`)",
            "Host(`other.local`) || PathPrefix(`/api`)",
            "!Host(`admin.nchat.local`) && PathPrefix(`/api`)",
        ):
            with self.subTest(rule=rule):
                self.assertShadows(rule=rule, priority=300)

    def test_a_non_ascii_host_proves_nothing(self):
        # Traefik refuses it; this file does not guess how it would compare.
        self.assertShadows(rule="Host(`nch\u0430t.local`)" + self.VERSION_REGEX, priority=300)

    def test_a_foreign_host_argument_cannot_hide_an_overlapping_or_non_ascii_host(self):
        for host in ("nchat.local", "nchat.local.", "nchat.local..", "nch\u0430t.local"):
            with self.subTest(host=host):
                self.assertShadows(rule=f"Host(`other.local`, `{host}`)" + self.VERSION_REGEX, priority=300)

    def test_malformed_input_never_passes(self):
        for config in (None, [], {"http": []}, {"http": {"routers": {"r": "x"}}}):
            self.assertTrue(check.audit(config), config)

    def test_other_services_may_still_rewrite_their_own_prefix(self):
        config = healthy()
        config["http"]["middlewares"]["strip-chat"] = {"stripPrefix": {"prefixes": ["/api/chat"]}}
        config["http"]["routers"]["chat"] = {
            "entryPoints": ["web"],
            "rule": "Host(`nchat.local`) && PathPrefix(`/api/chat`)",
            "middlewares": ["strip-chat"],
        }
        self.assertEqual(check.audit(config), [])


class RealGatewayInjectionTest(unittest.TestCase):
    """Adversarial routers added to an in-memory copy of the committed dynamic.yml."""

    def audit_with(self, rule: str, **fields) -> list[str]:
        config = yaml.safe_load(DYNAMIC.read_text())
        config["http"]["middlewares"]["injected-strip"] = {"stripPrefix": {"prefixes": [PREFIX]}}
        router = {"entryPoints": ["web"], "rule": rule, "service": SERVICE, "middlewares": ["injected-strip"], "priority": 300}
        router.update(fields)
        for key in [k for k, v in router.items() if v == "absent"]:
            del router[key]
        config["http"]["routers"]["injected"] = router
        return check.audit(config)

    def test_rejected_on_the_real_gateway(self):
        host = "Host(`nchat.local`) && "
        cases = [
            (NotificationsGatewayTest.SHADOW_ZERO, {"priority": 0}),
            (NotificationsGatewayTest.SHADOW_ZERO, {"priority": "absent"}),
            (host + "PathPrefix(`/api`)", {"priority": "500"}),
            (host + "PathRegexp(`^/api/notifications/push/config$`)", {}),
            (host + "PathRegexp(`^/api/notifications/readyz$`)", {}),
            (host + "PathRegexp(`^/api/notifications/push/subscriptions$`)", {}),
            (host + f"PathRegexp(`^{NotificationsGatewayTest.SUBSCRIPTION}$`)", {}),
            ("Host(`other.local`) || PathPrefix(`/api`)", {}),
            ("!Host(`admin.nchat.local`) && PathPrefix(`/api`)", {}),
            (host + "PathPrefix(`/api`)", {"entryPoints": "absent"}),
            ("Host(`nchat.local`) && PathPrefix(`/api`) ||", {}),
            (NotificationsGatewayTest.UNICODE_RULE, {"priority": 0}),
            (NotificationsGatewayTest.UNICODE_RULE, {"priority": "absent"}),
            (NotificationsGatewayTest.UNICODE_RULE, {"priority": None}),
            ("Host(`nchat.local.`) && PathRegexp(`^/api/notifications/version$`)", {}),
            ("Host(`NCHAT.LOCAL.`) && PathPrefix(`/api`)", {}),
        ] + [
            (host + f"PathRegexp(`{p}`)", {})
            for p in NotificationsGatewayTest.RE2_ALTERNATION_HIDERS + NotificationsGatewayTest.RE2_FLAG_GROUP_QUANTIFIERS
        ]
        for rule, fields in cases:
            with self.subTest(rule=rule, fields=fields):
                problems = self.audit_with(rule, **fields)
                self.assertTrue(any(p.startswith("injected: can select") for p in problems), problems)

    def test_accepted_on_the_real_gateway(self):
        host = "Host(`nchat.local`) && "
        for rule, fields in (
            (host + "PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)", {}),
            (host + "PathPrefix(`/api/chat`)", {}),
            ("Host(`other.local`) && PathPrefix(`/api`)", {}),
            (host + "PathPrefix(`/api`)", {"entryPoints": ["dashboard"]}),
            (host + "PathPrefix(`/`)", {"priority": 1}),
            (host + "Path(`/api/chat/healthz`)", {}),
            (host + "PathPrefix(`/api/search`)", {}),
            (host + "PathRegexp(`^/api/chat/[a-z0-9-]+$`)", {}),
            ("Host(`other.local.`) && PathPrefix(`/api`)", {}),
            (NotificationsGatewayTest.ASCII_TWIN, {"priority": 0}),
        ):
            with self.subTest(rule=rule, fields=fields):
                self.assertEqual(self.audit_with(rule, **fields), [])


if __name__ == "__main__":
    unittest.main()
