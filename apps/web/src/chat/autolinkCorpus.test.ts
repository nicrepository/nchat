/**
 * The client half of the shared URL-boundary corpus (issue #135, CQ-001).
 *
 * # The invariant
 *
 * Every href this client renders as an anchor must be a URL chat-service actually
 * extracted from the same text and submitted to Link Safety. If the two boundary
 * implementations disagree, a reader gets a clickable link to an address nobody
 * checked — which is exactly what happened with
 *
 *	https://example.test/wiki/Function_(mathematics)
 *
 * scanned as ".../Function_(mathematics" and rendered as
 * ".../Function_(mathematics)".
 *
 * # One source of expectations
 *
 * libs/testdata/link-safety/autolink-corpus.json holds both sides' expectations
 * for each input. The Go suite
 * (services/chat-service/internal/service/link_safety_corpus_test.go) asserts the
 * backend column against the real extractor; this file asserts the client column
 * against the real scanner, and re-asserts the cross-invariant so a change here
 * fails here.
 *
 * Two independently maintained expectation lists would drift back apart, which is
 * how the bug arrived in the first place. The fixture is data only.
 */

import { describe, expect, it } from "vitest";

import corpus from "../../../../libs/testdata/link-safety/autolink-corpus.json";
import { findAutolinks } from "./autolink";

interface CorpusCase {
  name: string;
  input: string;
  backendCandidates: string[];
  frontendHrefs: string[];
  note?: string;
}

const cases: CorpusCase[] = corpus.cases;

describe("the shared URL-boundary corpus", () => {
  it("is not empty", () => {
    expect(cases.length).toBeGreaterThan(20);
  });

  it.each(cases.map((c) => [c.name, c] as const))(
    "client hrefs match the corpus: %s",
    (_name, testCase) => {
      const hrefs = findAutolinks(testCase.input).map((span) => span.href);
      expect(hrefs, testCase.note ?? "").toEqual(testCase.frontendHrefs);
    },
  );

  // The invariant, asserted against the backend column of the same fixture. The
  // Go suite proves that column really is what the extractor produces, so
  // checking membership here is checking membership in the real scanned set.
  it.each(cases.map((c) => [c.name, c] as const))(
    "every client anchor is a backend candidate: %s",
    (_name, testCase) => {
      for (const href of findAutolinks(testCase.input).map((span) => span.href)) {
        expect(
          testCase.backendCandidates,
          `the client would anchor ${JSON.stringify(href)}, which the backend never ` +
            `extracted from ${JSON.stringify(testCase.input)} — an anchor to an ` +
            `unscanned URL is the whole of CQ-001`,
        ).toContain(href);
      }
    },
  );

  // The asymmetry is deliberate and load-bearing: the client is allowed to draw
  // fewer links than the backend scans, never more. Stated here so nobody
  // "fixes" it in the wrong direction.
  it("under-links but never over-links", () => {
    let underLinked = 0;
    for (const testCase of cases) {
      const hrefs = findAutolinks(testCase.input).map((span) => span.href);
      expect(hrefs.length).toBeLessThanOrEqual(testCase.backendCandidates.length);
      if (hrefs.length < testCase.backendCandidates.length) underLinked++;
    }
    expect(underLinked, "the parser-divergence cases have been lost").toBeGreaterThan(0);
  });

  // The divergence cases are the reason this corpus is not just a formatting
  // test. Named explicitly so deleting one is visible in a diff.
  it("covers every parser-divergence case", () => {
    const names = new Set(cases.map((c) => c.name));
    for (const required of [
      "backslash in authority",
      "invalid percent escape",
      "credentials in authority",
      "bidi override around a url",
      "bidi isolate around a url",
      "zero width space inside a url",
      "control character ends the run",
      "scheme inside another token",
      "scheme after a colon",
      "blob wrapping https",
      "protocol relative",
      "javascript scheme",
      "data scheme",
      "ipv6 literal",
      "short ipv4 literal",
      "octal ipv4 literal",
      "hex ipv4 literal",
      "internationalised domain",
      "punycode host",
      "balanced parentheses",
      "external parentheses",
      "percent-encoded parentheses",
    ]) {
      expect(names, `corpus case "${required}" is missing`).toContain(required);
    }
  });
});
