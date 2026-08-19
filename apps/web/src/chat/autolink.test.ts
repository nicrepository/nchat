/**
 * The URL scanner behind autolinking (issue #135).
 *
 * Two properties, and the second is the security one.
 *
 * It finds the http(s) URLs a reader would recognise, matching what
 * chat-service's own extractor submits for scanning — a link the renderer makes
 * clickable but the backend never extracted is a link nobody checked.
 *
 * And it can never produce an anchor for a dangerous scheme. Not because such
 * schemes are filtered out, but because the scanner matches the literal prefixes
 * `http://` and `https://`: there is no code path from `javascript:` to a span.
 * The table below is a regression net over that claim rather than the mechanism
 * enforcing it.
 */

import { describe, expect, it } from "vitest";

import { findAutolinks } from "./autolink";

const hrefs = (text: string) => findAutolinks(text).map((span) => span.href);

describe("findAutolinks", () => {
  it("finds plain http and https URLs", () => {
    expect(hrefs("veja https://example.test/a e http://other.test/b")).toEqual([
      "https://example.test/a",
      "http://other.test/b",
    ]);
  });

  it("matches the scheme case-insensitively", () => {
    // The body is stored as the sender wrote it, and "HTTPS://" is a URL to every
    // browser. Missing it here while the backend matches it would be exactly the
    // drift that leaves a checked link unlinked.
    expect(hrefs("HTTPS://Example.test/A")).toEqual(["HTTPS://Example.test/A"]);
  });

  it("gives trailing sentence punctuation back to the sentence", () => {
    expect(hrefs("veja https://example.test/a.")).toEqual(["https://example.test/a"]);
    expect(hrefs("(https://example.test/a)")).toEqual(["https://example.test/a"]);
    expect(hrefs("https://example.test/a, e mais")).toEqual(["https://example.test/a"]);
  });

  it("keeps path, query and fragment", () => {
    // The href is what the reader sees. Normalising it — dropping the fragment,
    // adding a trailing slash — would make the anchor point somewhere other than
    // the address printed next to it.
    const url = "https://example.test/a/b?x=1&y=2#frag";
    expect(hrefs(`ver ${url} agora`)).toEqual([url]);
  });

  it("reports offsets that slice the source exactly", () => {
    const text = "antes https://example.test/a depois";
    const [span] = findAutolinks(text);
    expect(text.slice(span.start, span.end)).toBe("https://example.test/a");
  });

  it("stops at whitespace and control characters", () => {
    expect(hrefs("https://example.test/a\nhttps://other.test/b")).toEqual([
      "https://example.test/a",
      "https://other.test/b",
    ]);
  });

  // The heart of the matter. None of these may ever become a span, and therefore
  // none of them can ever become an anchor.
  it("never produces a span for a dangerous scheme", () => {
    for (const hostile of [
      "javascript:alert(1)",
      "JavaScript:alert(1)",
      "  javascript:alert(document.domain)",
      "data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
      "data:text/html,<script>alert(1)</script>",
      "file:///etc/passwd",
      "blob:https://example.test/uuid",
      "vbscript:msgbox(1)",
      "about:blank",
      "chrome://settings",
      "ftp://example.test/a",
      "mailto:someone@example.test",
      "tel:+5511999999999",
      "//example.test/protocol-relative",
      "\\\\example.test\\share",
    ]) {
      expect(findAutolinks(hostile)).toEqual([]);
    }
  });

  // A scheme embedded in another token is not a URL the sender wrote, so it is
  // not offered as one.
  it("does not linkify a scheme that starts inside another token", () => {
    expect(findAutolinks("blob:https://example.test/uuid")).toEqual([]);
    expect(findAutolinks("shttp://example.test")).toEqual([]);
    expect(findAutolinks("https://a.test/r?u=/https://b.test")).toEqual([
      { start: 0, end: 34, href: "https://a.test/r?u=/https://b.test" },
    ]);
  });

  // When a real https URL genuinely follows a sentence character, it *is* a link
  // the sender wrote, and it is offered — but the dangerous prefix around it can
  // never be the destination. This is the property that matters: whatever the
  // text, every href produced here is http or https.
  it("never yields a dangerous href, whatever surrounds the URL", () => {
    for (const text of [
      "javascript:void(https://example.test/a)",
      "data:text/html,https://example.test/a",
      "clique em javascript:alert(1) ou https://example.test/a",
    ]) {
      for (const { href } of findAutolinks(text)) {
        expect(href).toMatch(/^https?:\/\//i);
        expect(new URL(href).protocol).toMatch(/^https?:$/);
      }
    }
  });

  it("ignores text that only looks like a URL", () => {
    for (const notALink of [
      "https://",
      "http://",
      "https:// example.test",
      "example.test/a",
      "www.example.test",
      "shttp://example.test",
      "hxxps://example.test/a",
    ]) {
      expect(findAutolinks(notALink)).toEqual([]);
    }
  });

  it("refuses a URL carrying credentials", () => {
    // The backend refuses to publish a message containing one at all, so this
    // cannot normally arrive. Refused here too, so the renderer does not depend on
    // that staying true — and because an anchor whose authority section is a
    // lookalike host is a classic disguise.
    expect(findAutolinks("https://user:pass@evil.test/a")).toEqual([]);
    expect(findAutolinks("https://example.test@evil.test/a")).toEqual([]);
  });

  it("handles a URL at either end of the text", () => {
    expect(hrefs("https://example.test/a")).toEqual(["https://example.test/a"]);
    expect(hrefs("ver https://example.test/a")).toEqual(["https://example.test/a"]);
    expect(hrefs("https://example.test/a ver")).toEqual(["https://example.test/a"]);
  });

  it("returns nothing for text with no URL", () => {
    expect(findAutolinks("")).toEqual([]);
    expect(findAutolinks("uma mensagem comum, sem link")).toEqual([]);
  });
});

// Delimiter balancing (CQ-009).
//
// Stripping every trailing bracket breaks ordinary addresses — Wikipedia's
// disambiguated titles are the canonical example — while keeping every trailing
// bracket swallows the closing paren of a parenthesised sentence. The rule is
// balance: a closer that the candidate itself opened belongs to the URL, one that
// does not belongs to the sentence.
describe("findAutolinks trailing delimiters", () => {
  it("keeps a balanced closing bracket", () => {
    expect(hrefs("veja https://example.test/wiki/Function_(mathematics) agora")).toEqual([
      "https://example.test/wiki/Function_(mathematics)",
    ]);
    expect(hrefs("https://example.test/a_(b)")).toEqual(["https://example.test/a_(b)"]);
    expect(hrefs("https://example.test/a_[b]")).toEqual(["https://example.test/a_[b]"]);
    expect(hrefs("https://example.test/a_{b}")).toEqual(["https://example.test/a_{b}"]);
  });

  it("gives back an unbalanced closing bracket", () => {
    expect(hrefs("(https://example.test/foo)")).toEqual(["https://example.test/foo"]);
    expect(hrefs("[https://example.test/foo]")).toEqual(["https://example.test/foo"]);
    expect(hrefs("veja (https://example.test/a_(b)) ok")).toEqual(["https://example.test/a_(b)"]);
  });

  it("peels mixed trailing punctuation right to left", () => {
    expect(hrefs("https://example.test/foo].")).toEqual(["https://example.test/foo"]);
    expect(hrefs("(https://example.test/foo).")).toEqual(["https://example.test/foo"]);
    expect(hrefs("https://example.test/a_(b).")).toEqual(["https://example.test/a_(b)"]);
  });

  it("does not treat percent-encoded brackets as delimiters", () => {
    // %28/%29 are not brackets to a reader, and counting literal characters only
    // is what keeps them out of the balance.
    expect(hrefs("https://example.test/a%28b%29")).toEqual(["https://example.test/a%28b%29"]);
    expect(hrefs("(https://example.test/a%29)")).toEqual(["https://example.test/a%29"]);
  });
});

// Hardening around the edges of what a host may be.
describe("findAutolinks host shapes", () => {
  it("links an internationalised domain without mangling it", () => {
    // The href is the text the sender wrote. Normalising to punycode here would
    // make the anchor point at an address that is not the one printed beside it,
    // which is the disguise this deliberately avoids.
    expect(hrefs("veja https://münchen.example/straße agora")).toEqual([
      "https://münchen.example/straße",
    ]);
  });

  it("links an explicit punycode host", () => {
    expect(hrefs("https://xn--mnchen-3ya.example/a")).toEqual(["https://xn--mnchen-3ya.example/a"]);
  });

  it("does not link a bracketed IPv6 literal", () => {
    // The trailing `]` is balanced by the `[` that opens the authority, so the
    // delimiter rule must not eat it and leave a broken address.
    expect(hrefs("https://[2001:db8::1]/a")).toEqual([]);
    expect(hrefs("https://[2001:db8::1]:8443/a")).toEqual([]);
  });

  it("does not link ordinary or WHATWG legacy IPv4 literals", () => {
    expect(hrefs("http://192.0.2.10:8080/a")).toEqual([]);
    expect(hrefs("http://127.1/a")).toEqual([]);
    expect(hrefs("http://0177.0.0.1/a")).toEqual([]);
    expect(hrefs("http://0x7f.0.0.1/a")).toEqual([]);
  });

  it("is not confused by bidi and zero-width characters around a URL", () => {
    // Written as explicit escapes rather than literal invisible characters: a
    // reviewer cannot see U+202E in a diff, and a scanner flags it as a bidi
    // smuggling risk. The runtime values are the same code points either way.
    const RLO = "\u202E"; // right-to-left override
    const PDF = "\u202C"; // pop directional formatting
    const LRI = "\u2066"; // left-to-right isolate
    const PDI = "\u2069"; // pop directional isolate
    const ZWSP = "\u200B"; // zero-width space

    // Every one of these must produce no anchor at all: the rendered label would
    // not be the address the browser resolves, and the backend's own extractor
    // sees different text, so an anchor here could point somewhere unscanned.
    for (const text of [
      `${RLO}https://example.test/a${PDF}`,
      `${ZWSP}https://example.test/a${ZWSP}`,
      `veja ${LRI}https://example.test/a${PDI} agora`,
      `https://example.test/${RLO}gnp.exe`,
      `https://exa${ZWSP}mple.test/a`,
    ]) {
      expect(findAutolinks(text)).toEqual([]);
    }
  });

  it("still refuses credentials however the host is spelled", () => {
    expect(findAutolinks("https://user:pass@münchen.example/a")).toEqual([]);
    expect(findAutolinks("https://user@[2001:db8::1]/a")).toEqual([]);
  });

  it("still refuses a nested URL as a separate anchor", () => {
    // One span for the outer address, never a second for the embedded one.
    const spans = findAutolinks("https://a.test/r?u=https://b.test/x");
    expect(spans).toHaveLength(1);
    expect(spans[0].href).toBe("https://a.test/r?u=https://b.test/x");
  });
});
