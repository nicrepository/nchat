import { describe, expect, it } from "vitest";

import { describeUserAgent, UNKNOWN_BROWSER } from "./userAgentLabel";

const CHROME_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36";
const EDGE_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0";
const FIREFOX_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:142.0) Gecko/20100101 Firefox/142.0";
const SAFARI_MACOS =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15";

describe("describeUserAgent", () => {
  it.each([
    [CHROME_WINDOWS, "Chrome 152", "Windows 10/11"],
    [EDGE_WINDOWS, "Microsoft Edge 153", "Windows 10/11"],
    [FIREFOX_WINDOWS, "Firefox 142", "Windows 10/11"],
    [SAFARI_MACOS, "Safari 19", "macOS"],
  ])("describes %s", (userAgent, browser, platform) => {
    expect(describeUserAgent(userAgent)).toEqual({ browser, platform });
  });

  it("does not call Edge 'Chrome', even though Edge ships a Chrome token", () => {
    expect(describeUserAgent(EDGE_WINDOWS).browser).toBe("Microsoft Edge 153");
  });

  it("does not call Chrome 'Safari', even though Chrome ships a Safari token", () => {
    expect(describeUserAgent(CHROME_WINDOWS).browser).toBe("Chrome 152");
  });

  it("does not call Chromium 'Chrome', even though Chromium ships a Chrome token", () => {
    expect(
      describeUserAgent(
        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chromium/140.0.0.0 Chrome/140.0.0.0 Safari/537.36",
      ),
    ).toEqual({ browser: "Chromium 140", platform: "Linux" });
  });

  it.each([
    [
      "Opera on Windows",
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 OPR/125.0.0.0",
      { browser: "Opera 125", platform: "Windows 10/11" },
    ],
    [
      "Chrome on Android",
      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Mobile Safari/537.36",
      { browser: "Chrome 152", platform: "Android" },
    ],
    [
      "Edge on Android",
      "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Mobile Safari/537.36 EdgA/153.0.0.0",
      { browser: "Microsoft Edge 153", platform: "Android" },
    ],
    [
      "Firefox on Android",
      "Mozilla/5.0 (Android 14; Mobile; rv:142.0) Gecko/142.0 Firefox/142.0",
      { browser: "Firefox 142", platform: "Android" },
    ],
    [
      "Safari on iPhone",
      "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1",
      { browser: "Safari 19", platform: "iOS" },
    ],
    [
      "Chrome on iPad",
      "Mozilla/5.0 (iPad; CPU OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/152.0.0.0 Mobile/15E148 Safari/604.1",
      { browser: "Chrome 152", platform: "iPadOS" },
    ],
    [
      "Firefox on iPhone",
      "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/142.0 Mobile/15E148 Safari/605.1.15",
      { browser: "Firefox 142", platform: "iOS" },
    ],
  ])("describes %s", (_name, userAgent, expected) => {
    expect(describeUserAgent(userAgent)).toEqual(expected);
  });

  it("reports Windows without a version when the UA predates Windows NT 10.0", () => {
    expect(describeUserAgent("Mozilla/5.0 (Windows NT 6.1) Firefox/115.0")).toEqual({
      browser: "Firefox 115",
      platform: "Windows",
    });
  });

  it.each([
    ["empty", ""],
    ["unknown client", "curl/8.7.1"],
    ["a partial string", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKi"],
    ["a bot", "Mozilla/5.0 (compatible; SomeBot/1.0; +https://example.com/bot)"],
  ])("falls back to an honest label for %s", (_name, userAgent) => {
    expect(describeUserAgent(userAgent).browser).toBe(UNKNOWN_BROWSER);
  });

  it("still reports the platform when only the browser is unrecognised", () => {
    expect(describeUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKi")).toEqual({
      browser: UNKNOWN_BROWSER,
      platform: "Windows 10/11",
    });
  });

  it("never throws on values that are not strings", () => {
    const unknown: UserAgentLike[] = [null, undefined, 42, {}, []];
    for (const value of unknown) {
      expect(describeUserAgent(value)).toEqual({ browser: UNKNOWN_BROWSER, platform: "" });
    }
  });
});

type UserAgentLike = null | undefined | number | object;
