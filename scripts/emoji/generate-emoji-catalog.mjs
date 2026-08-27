#!/usr/bin/env node
/**
 * Regenerates the NChat reaction emoji catalog (issue #496).
 *
 * There is exactly one source of truth for which emoji exist and what they are
 * called, and it is not this repository: it is Unicode's RGI list plus CLDR's
 * Portuguese annotations. This script reads both and writes the two projections
 * the product needs, so the backend allowlist and the frontend picker can never
 * drift apart — they are produced by the same run, from the same input.
 *
 *   - services/chat-service/internal/service/emoji_catalog.txt
 *       validation projection: every RGI sequence, one per line, go:embed-ed.
 *   - apps/web/src/chat/emoji/emojiCatalog.json
 *       presentation projection: base emoji with Portuguese label, search
 *       keywords, group index and skin-tone variants, minified and lazy-loaded.
 *
 * It is a maintenance tool, run by a person when the project adopts a new
 * Unicode version — never by CI and never at runtime. Both outputs are checked
 * in, so neither build needs network access or this script.
 *
 * Usage: node scripts/emoji/generate-emoji-catalog.mjs
 */

import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const UNICODE_EMOJI_VERSION = "16.0";
const CLDR_VERSION = "46";

const SOURCES = {
  emojiTest: `https://unicode.org/Public/emoji/${UNICODE_EMOJI_VERSION}/emoji-test.txt`,
  annotations: `https://raw.githubusercontent.com/unicode-org/cldr/release-${CLDR_VERSION}/common/annotations/pt.xml`,
  annotationsDerived: `https://raw.githubusercontent.com/unicode-org/cldr/release-${CLDR_VERSION}/common/annotationsDerived/pt.xml`,
};

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const GO_OUTPUT = join(ROOT, "services/chat-service/internal/service/emoji_catalog.txt");
const WEB_OUTPUT = join(ROOT, "apps/web/src/chat/emoji/emojiCatalog.json");

/** U+FE0F is absent from CLDR annotation keys; strip it before every lookup. */
const VARIATION_SELECTOR_16 = "️";
const SKIN_TONE = /[\u{1F3FB}-\u{1F3FF}]/gu;
/** Unicode's five skin-tone modifiers, in the order a tone selector offers them. */
const SKIN_TONE_MODIFIERS = ["\u{1F3FB}", "\u{1F3FC}", "\u{1F3FD}", "\u{1F3FE}", "\u{1F3FF}"];
const ANNOTATION = /<annotation cp="([^"]*)"(\s+type="tts")?>([^<]*)<\/annotation>/g;
const EMOJI_LINE = /^([0-9A-F ]+);\s+fully-qualified\s+#\s+(\S+)\s+E\d+\.\d+\s+(.+)$/;
const GROUP_LINE = /^# group: (.+)$/;

async function fetchText(url) {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`GET ${url} failed with ${response.status}`);
  return response.text();
}

function annotationKey(emoji) {
  return emoji.replaceAll(VARIATION_SELECTOR_16, "");
}

/** Portuguese display names (type="tts") and search keywords, keyed by sequence. */
function parseAnnotations(documents) {
  const labels = new Map();
  const keywords = new Map();
  for (const document of documents) {
    for (const [, cp, isName, value] of document.matchAll(ANNOTATION)) {
      const key = annotationKey(cp);
      if (isName) labels.set(key, value.trim());
      else keywords.set(key, value.split("|").map((word) => word.trim()));
    }
  }
  return { labels, keywords };
}

/** Every RGI sequence in CLDR order, tagged with the group it is listed under. */
function parseEmojiTest(document) {
  const groups = [];
  const sequences = [];
  for (const line of document.split("\n")) {
    const group = GROUP_LINE.exec(line);
    if (group) {
      groups.push(group[1]);
      continue;
    }
    const emoji = EMOJI_LINE.exec(line);
    if (emoji) sequences.push({ unicode: emoji[2], name: emoji[3], group: groups.length - 1 });
  }
  return sequences;
}

/**
 * Splits the flat RGI list into base emoji and the skin-tone variants that
 * belong under them. A variant whose de-toned form is not itself RGI (there are
 * a few) stays a base entry of its own rather than being dropped.
 */
function groupSkinTones(sequences) {
  const known = new Set(sequences.map((sequence) => sequence.unicode));
  const skinsByBase = new Map();
  const bases = [];
  for (const sequence of sequences) {
    const base = sequence.unicode.replace(SKIN_TONE, "");
    if (base === sequence.unicode || !known.has(base)) {
      bases.push(sequence);
      continue;
    }
    const skins = skinsByBase.get(base) ?? [];
    skins.push(sequence.unicode);
    skinsByBase.set(base, skins);
  }
  return { bases, skinsByBase };
}

/**
 * The tone every person in a sequence shares, as a 1-based index into
 * SKIN_TONE_MODIFIERS — or 0 when the people in it are toned differently.
 *
 * A sequence with more than one person carries the whole cartesian product of
 * tones in RGI: 🧑‍🤝‍🧑 has twenty-five variants, of which only five are the
 * "everyone the same tone" ones a global tone selector means. This is what
 * tells them apart, by reading the modifiers themselves rather than by trusting
 * a position in that product.
 */
function homogeneousTone(sequence) {
  const modifiers = [...sequence.matchAll(SKIN_TONE)].map((match) => match[0]);
  if (modifiers.length === 0 || new Set(modifiers).size !== 1) return 0;
  return SKIN_TONE_MODIFIERS.indexOf(modifiers[0]) + 1;
}

/**
 * Splits an emoji's skin variants into the five a tone selector picks from, in
 * tone order, and the mixed ones it never picks.
 *
 * The mixed variants are kept: they are valid RGI emoji that a person can have
 * reacted with, and the catalog still has to be able to name them.
 */
function splitSkinVariants(variants) {
  const homogeneous = [];
  const mixed = [];
  for (const variant of variants) {
    const tone = homogeneousTone(variant);
    if (tone === 0) mixed.push(variant);
    else homogeneous[tone - 1] = variant;
  }
  // A gap would make the array's index stop meaning "tone", so an emoji missing
  // any of the five is treated as having none and falls back to its base form.
  const complete = homogeneous.length === SKIN_TONE_MODIFIERS.length && !homogeneous.includes(undefined);
  return { homogeneous: complete ? homogeneous : [], mixed: complete ? mixed : variants };
}

function searchKeywords(sequence, keywords) {
  const portuguese = keywords.get(annotationKey(sequence.unicode)) ?? [];
  const english = sequence.name.split(/[^\p{L}\p{N}]+/u);
  return [...new Set([...portuguese, ...english].filter(Boolean))].join(" ");
}

function buildWebEntries(bases, skinsByBase, { labels, keywords }) {
  return bases.map((sequence) => {
    const entry = {
      u: sequence.unicode,
      l: labels.get(annotationKey(sequence.unicode)) ?? sequence.name,
      t: searchKeywords(sequence, keywords),
      g: sequence.group,
    };
    const variants = skinsByBase.get(sequence.unicode);
    if (variants) {
      const { homogeneous, mixed } = splitSkinVariants(variants);
      if (homogeneous.length > 0) entry.s = homogeneous;
      if (mixed.length > 0) entry.m = mixed;
    }
    return entry;
  });
}

function buildGoCatalog(sequences) {
  return [
    "# Generated by scripts/emoji/generate-emoji-catalog.mjs — do not edit by hand.",
    `# version ${UNICODE_EMOJI_VERSION}`,
    ...sequences.map((sequence) => sequence.unicode),
    "",
  ].join("\n");
}

async function write(path, contents) {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, contents, "utf8");
  console.log(`wrote ${path} (${contents.length} bytes)`);
}

async function main() {
  const [emojiTest, annotations, annotationsDerived] = await Promise.all([
    fetchText(SOURCES.emojiTest),
    fetchText(SOURCES.annotations),
    fetchText(SOURCES.annotationsDerived),
  ]);
  const sequences = parseEmojiTest(emojiTest);
  const { bases, skinsByBase } = groupSkinTones(sequences);
  const entries = buildWebEntries(bases, skinsByBase, parseAnnotations([annotations, annotationsDerived]));
  await write(GO_OUTPUT, buildGoCatalog(sequences));
  await write(
    WEB_OUTPUT,
    `${JSON.stringify({ version: UNICODE_EMOJI_VERSION, cldr: CLDR_VERSION, emojis: entries })}\n`,
  );
  console.log(`${sequences.length} sequences, ${entries.length} base emoji`);
}

await main();
