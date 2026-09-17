/**
 * Turns a raw User-Agent into something presentable on Perfil > Sessões
 * (issue #854).
 *
 * Presentation only: User-Agent is client-controlled and is never a security
 * boundary — nothing here may feed authentication or authorization.
 *
 * A UA carries compatibility tokens ("Safari/" inside Chrome, "Chrome/"
 * inside Edge), so the order of the tables below *is* the rule: the first
 * pattern that matches wins, and the tokens that only exist for
 * compatibility sit last.
 */

export interface UserAgentLabel {
  /** "Chrome 152", or the fallback when nothing is recognised. */
  browser: string;
  /** "Windows 10/11", or "" when the platform is not identifiable. */
  platform: string;
}

export const UNKNOWN_BROWSER = "Navegador desconhecido";

/** Ordered: first match wins. Group 1, when present, is the major version. */
const BROWSERS: ReadonlyArray<readonly [RegExp, string]> = [
  // Edge ships "Chrome/" and "Safari/" too, so it has to be tested first.
  [/\bEdg(?:e|A|iOS)?\/(\d+)/, "Microsoft Edge"],
  [/\b(?:OPR|OPiOS)\/(\d+)/, "Opera"],
  // iOS forces every engine to be WebKit, so these ship "Safari/" as well.
  [/\bFxiOS\/(\d+)/, "Firefox"],
  [/\bCriOS\/(\d+)/, "Chrome"],
  [/\bFirefox\/(\d+)/, "Firefox"],
  // Chromium builds ship both "Chromium/" and "Chrome/".
  [/\bChromium\/(\d+)/, "Chromium"],
  [/\bChrome\/(\d+)/, "Chrome"],
  // Last: "Safari/" alone proves nothing, only "Version/… Safari/" does.
  [/\bVersion\/(\d+)[.\d]*(?: Mobile\/\S+)? Safari\//, "Safari"],
];

/** Ordered: Android and ChromeOS also say "Linux", iPad also says "Mac OS X". */
const PLATFORMS: ReadonlyArray<readonly [RegExp, string]> = [
  [/\bWindows NT 10\.0\b/, "Windows 10/11"],
  [/\bWindows\b/, "Windows"],
  [/\bAndroid\b/, "Android"],
  [/\biPad\b/, "iPadOS"],
  [/\b(?:iPhone|iPod)\b/, "iOS"],
  [/\bCrOS\b/, "ChromeOS"],
  [/\b(?:Macintosh|Mac OS X)\b/, "macOS"],
  [/\bLinux\b/, "Linux"],
];

/**
 * Never throws and never invents: an unrecognised browser still reports a
 * platform when the platform alone is identifiable, and a UA that says
 * "Windows NT 10.0" reports "Windows 10/11" because that token genuinely
 * cannot tell 10 from 11.
 */
export function describeUserAgent(userAgent: unknown): UserAgentLabel {
  const ua = typeof userAgent === "string" ? userAgent : "";
  let browser = UNKNOWN_BROWSER;
  for (const [pattern, name] of BROWSERS) {
    const found = pattern.exec(ua);
    if (found) {
      browser = found[1] ? `${name} ${found[1]}` : name;
      break;
    }
  }
  let platform = "";
  for (const [pattern, name] of PLATFORMS) {
    if (pattern.test(ua)) {
      platform = name;
      break;
    }
  }
  return { browser, platform };
}
