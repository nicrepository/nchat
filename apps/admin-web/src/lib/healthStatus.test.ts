import { describe, expect, it } from "vitest";

import type { HealthState, PlatformMetric } from "../api/observabilityApi";
import {
  attentionRank,
  categoryLabel,
  formatAge,
  formatLatency,
  formatMetric,
  presentState,
  sortServices,
  windowLabel,
  HEALTH_STATE_ORDER,
} from "./healthStatus";

describe("presentState", () => {
  it("gives every state a distinct word and a distinct shape", () => {
    const labels = new Set<string>();
    const marks = new Set<string>();
    for (const state of HEALTH_STATE_ORDER) {
      const presentation = presentState(state);
      labels.add(presentation.label);
      marks.add(presentation.mark);
      expect(presentation.meaning).not.toBe("");
    }
    // Colour is reinforcement. If two states shared a word or a shape, an
    // operator on a monochrome screen could not tell them apart at all.
    expect(labels.size).toBe(HEALTH_STATE_ORDER.length);
    expect(marks.size).toBe(HEALTH_STATE_ORDER.length);
  });

  it("falls back to unknown rather than to anything better", () => {
    expect(presentState("invented" as HealthState)).toEqual(presentState("unknown"));
  });
});

describe("attentionRank", () => {
  it("ranks a blind spot above a working dependency and below a broken one", () => {
    expect(attentionRank("unknown")).toBeGreaterThan(attentionRank("healthy"));
    expect(attentionRank("unknown")).toBeLessThan(attentionRank("degraded"));
    expect(attentionRank("unavailable")).toBeGreaterThan(attentionRank("degraded"));
  });

  it("ranks a deliberately disabled integration lowest", () => {
    expect(attentionRank("disabled")).toBeLessThan(attentionRank("healthy"));
  });

  it("treats an unrecognised state as unknown", () => {
    expect(attentionRank("invented" as HealthState)).toBe(attentionRank("unknown"));
  });
});

describe("formatLatency", () => {
  it("renders a measured round trip", () => {
    expect(formatLatency(12)).toBe("12 ms");
    expect(formatLatency(3001)).toBe("3.0 s");
  });

  it("renders an unmeasured one as an em dash and never as zero", () => {
    // A disabled integration has no latency. "0 ms" would claim a measurement
    // that never happened.
    expect(formatLatency(null)).toBe("—");
    expect(formatLatency(0)).toBe("0 ms");
  });
});

describe("formatAge", () => {
  const base = Date.parse("2026-08-22T12:00:00.000Z");

  it("describes how long ago the check ran", () => {
    expect(formatAge("2026-08-22T12:00:00.000Z", base)).toBe("agora");
    expect(formatAge("2026-08-22T11:59:40.000Z", base)).toBe("há 20 s");
    expect(formatAge("2026-08-22T11:55:00.000Z", base)).toBe("há 5 min");
    expect(formatAge("2026-08-22T09:00:00.000Z", base)).toBe("há 3 h");
  });

  it("never reports a negative age from a clock that disagrees", () => {
    expect(formatAge("2026-08-22T12:00:30.000Z", base)).toBe("agora");
  });

  it("renders an unparseable timestamp as an em dash", () => {
    expect(formatAge("not a date", base)).toBe("—");
  });
});

describe("formatMetric", () => {
  const metric = (overrides: Partial<PlatformMetric>): PlatformMetric => ({
    key: "users.total",
    label: "Usuários",
    definition: "Contas existentes.",
    window: "cumulative",
    unit: "count",
    value: 1234,
    available: true,
    ...overrides,
  });

  it("renders counts and sizes in their own units", () => {
    expect(formatMetric(metric({}))).toBe((1234).toLocaleString("pt-BR"));
    expect(formatMetric(metric({ unit: "bytes", value: 2 * 1024 * 1024 * 1024 }))).toBe("2.0 GiB");
  });

  it("distinguishes an unavailable aggregate from a zero", () => {
    expect(formatMetric(metric({ value: 0 }))).toBe("0");
    expect(formatMetric(metric({ available: false, value: null }))).toBe("Indisponível");
    // A payload claiming availability with no number must not render as zero
    // either: it is a contract mismatch, not an observation.
    expect(formatMetric(metric({ available: true, value: null }))).toBe("Indisponível");
  });
});

describe("windowLabel and categoryLabel", () => {
  it("names every declared window", () => {
    expect(windowLabel("instant")).toBe("agora");
    expect(windowLabel("last_24h")).toBe("últimas 24 h");
    expect(windowLabel("cumulative")).toBe("total");
  });

  it("names the sanitized failure categories", () => {
    expect(categoryLabel("connection_timeout")).toBe("Tempo limite de conexão");
    expect(categoryLabel("not_observable")).toBe("Não observável deste serviço");
    expect(categoryLabel("")).toBe("");
  });

  it("shows an unrecognised category rather than hiding it", () => {
    // The two ends disagreeing is worth seeing; a blank cell is not.
    expect(categoryLabel("brand_new_category")).toBe("brand_new_category");
  });
});

describe("sortServices", () => {
  const services = [
    { displayName: "SeaweedFS", state: "healthy" as HealthState, latencyMS: 18 },
    { displayName: "LiveKit", state: "unavailable" as HealthState, latencyMS: 3001 },
    { displayName: "SMTP", state: "disabled" as HealthState, latencyMS: null },
    { displayName: "ClamAV", state: "degraded" as HealthState, latencyMS: 640 },
    { displayName: "TURN", state: "unknown" as HealthState, latencyMS: null },
  ];

  it("puts the most troubled dependency first by default", () => {
    expect(sortServices(services, "attention").map((row) => row.displayName)).toEqual([
      "LiveKit",
      "ClamAV",
      "TURN",
      "SeaweedFS",
      "SMTP",
    ]);
  });

  it("orders by name when asked", () => {
    expect(sortServices(services, "name").map((row) => row.displayName)).toEqual([
      "ClamAV",
      "LiveKit",
      "SeaweedFS",
      "SMTP",
      "TURN",
    ]);
  });

  it("sorts unmeasured latencies last rather than as zero", () => {
    // Otherwise every disabled integration would crowd the top of a column
    // that is supposed to be about speed.
    expect(sortServices(services, "latency").map((row) => row.displayName)).toEqual([
      "LiveKit",
      "ClamAV",
      "SeaweedFS",
      "SMTP",
      "TURN",
    ]);
  });

  it("does not mutate the array it was given", () => {
    const original = [...services];
    sortServices(services, "name");
    expect(services).toEqual(original);
  });
});
