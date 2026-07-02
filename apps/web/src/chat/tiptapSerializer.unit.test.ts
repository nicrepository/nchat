/**
 * tiptapSerializer unit tests — pure TTNode inputs, no TipTap runtime.
 *
 * Verifies the serializer contract independently of TipTap's editor:
 * given a ProseMirror-shaped TTNode document, produce the correct Markdown.
 *
 * For round-trip tests using a live TipTap Editor instance, see
 * tiptapSerializer.integration.test.ts.
 */

import { describe, expect, it } from "vitest";
import { tiptapDocToMarkdown as serializeDoc, applyMarks } from "./tiptapSerializer";
import type { CodecFormat, TTNode } from "./tiptapSerializer";
import { buildMentionToken, MENTION_TOKEN_RE } from "./richTextMarkers";

// ── Helpers ────────────────────────────────────────────────────────────────────

function doc(...content: TTNode[]): TTNode {
  return { type: "doc", content };
}
const tiptapDocToMarkdown = (value: TTNode, format: CodecFormat = "v2") =>
  serializeDoc(value, format);
function para(...content: TTNode[]): TTNode {
  return { type: "paragraph", content };
}
function text(t: string, ...marks: string[]): TTNode {
  return {
    type: "text",
    text: t,
    marks: marks.map((type) => ({ type })),
  };
}
function hardBreak(): TTNode {
  return { type: "hardBreak" };
}
function mention(type: "user" | "channel", id: string, label: string): TTNode {
  return { type: "mention", attrs: { mentionType: type, id, label } };
}
function codeBlock(code: string): TTNode {
  return { type: "codeBlock", content: [{ type: "text", text: code }] };
}
function bulletList(...items: string[]): TTNode {
  return {
    type: "bulletList",
    content: items.map((item) => ({
      type: "listItem",
      content: [para(text(item))],
    })),
  };
}
function orderedList(...items: string[]): TTNode {
  return {
    type: "orderedList",
    content: items.map((item) => ({
      type: "listItem",
      content: [para(text(item))],
    })),
  };
}

// ── Empty / plain ──────────────────────────────────────────────────────────────

describe("tiptapDocToMarkdown — empty / plain", () => {
  it("empty doc returns empty string", () => {
    expect(tiptapDocToMarkdown(doc())).toBe("");
  });

  it("empty paragraph returns empty string", () => {
    expect(tiptapDocToMarkdown(doc(para()))).toBe("");
  });

  it("plain paragraph returns text", () => {
    expect(tiptapDocToMarkdown(doc(para(text("hello"))))).toBe("hello");
  });

  it("two paragraphs are joined with \\n", () => {
    expect(tiptapDocToMarkdown(doc(para(text("para1")), para(text("para2"))))).toBe("para1\npara2");
  });

  it("empty paragraph between two paras preserves blank line", () => {
    expect(tiptapDocToMarkdown(doc(para(text("top")), para(), para(text("bottom"))))).toBe(
      "top\n\nbottom",
    );
  });
});

// ── Inline marks ──────────────────────────────────────────────────────────────

describe("tiptapDocToMarkdown — inline marks", () => {
  it("uses the shared mention grammar for building and parsing tokens", () => {
    const token = buildMentionToken("user", "11111111-1111-1111-1111-111111111111", "Ana [Dev]");
    const parsed = MENTION_TOKEN_RE.exec(token);

    expect(token).toBe(
      String.raw`@[Ana \[Dev\]](mention:user:11111111-1111-1111-1111-111111111111)`,
    );
    expect(parsed?.slice(1)).toEqual([
      String.raw`Ana \[Dev\]`,
      "user",
      "11111111-1111-1111-1111-111111111111",
    ]);
  });

  it("serializes a v3 mention with its stable identity and escaped label", () => {
    expect(
      tiptapDocToMarkdown(
        doc(
          para(text("Oi "), mention("user", "11111111-1111-1111-1111-111111111111", "Ana [Dev]")),
        ),
        "v3",
      ),
    ).toBe(String.raw`Oi @[Ana \[Dev\]](mention:user:11111111-1111-1111-1111-111111111111)`);
  });

  it("escapes literal mention delimiters in v3 plain text", () => {
    expect(tiptapDocToMarkdown(doc(para(text("@[not a mention](literal)"))), "v3")).toBe(
      String.raw`\@\[not a mention\]\(literal\)`,
    );
  });
  it("bold text wrapped with **", () => {
    expect(tiptapDocToMarkdown(doc(para(text("hello", "bold"))))).toBe("**hello**");
  });

  it("italic text wrapped with *", () => {
    expect(tiptapDocToMarkdown(doc(para(text("hello", "italic"))))).toBe("*hello*");
  });

  it("inline code wrapped with backtick", () => {
    expect(tiptapDocToMarkdown(doc(para(text("npm i", "code"))))).toBe("`npm i`");
  });

  it.each([
    ["*nao_italico*", String.raw`\*nao\_italico\*`],
    ["**nao_bold**", String.raw`\*\*nao\_bold\*\*`],
    ["`nao_codigo`", String.raw`\`nao\_codigo\``],
    ["```", String.raw`\`\`\``],
    ["- nao_lista", String.raw`\- nao\_lista`],
    ["1. nao_ordenada", String.raw`1\. nao\_ordenada`],
    [String.raw`barra\literal`, String.raw`barra\\literal`],
  ])("escapes literal marker text %s", (literal, escaped) => {
    expect(tiptapDocToMarkdown(doc(para(text(literal))))).toBe(escaped);
  });

  it("code takes priority over bold when both marks present", () => {
    const result = tiptapDocToMarkdown(doc(para(text("hi", "bold", "code"))));
    expect(result).toBe("`hi`");
  });

  it("mixed text in paragraph: plain + bold + plain", () => {
    const result = tiptapDocToMarkdown(doc(para(text("Hello "), text("world", "bold"), text("!"))));
    expect(result).toBe("Hello **world**!");
  });

  it("hard break becomes \\n within paragraph", () => {
    const result = tiptapDocToMarkdown(doc(para(text("line1"), hardBreak(), text("line2"))));
    expect(result).toBe("line1\nline2");
  });
});

// ── Block nodes ────────────────────────────────────────────────────────────────

describe("tiptapDocToMarkdown — block nodes", () => {
  it("code block wrapped in triple backticks", () => {
    expect(tiptapDocToMarkdown(doc(codeBlock("const x = 1;")))).toBe("```\nconst x = 1;\n```");
  });

  it("empty code block produces empty fence", () => {
    expect(tiptapDocToMarkdown(doc(codeBlock("")))).toBe("```\n\n```");
  });

  it("bullet list prefixes each item with '- '", () => {
    expect(tiptapDocToMarkdown(doc(bulletList("alpha", "beta", "gamma")))).toBe(
      "- alpha\n- beta\n- gamma",
    );
  });

  it("ordered list prefixes each item with N.", () => {
    expect(tiptapDocToMarkdown(doc(orderedList("first", "second", "third")))).toBe(
      "1. first\n2. second\n3. third",
    );
  });

  it("preserves an ordered list start attribute", () => {
    const list = orderedList("third", "fourth");
    list.attrs = { start: 3 };
    expect(tiptapDocToMarkdown(doc(list))).toBe("3. third\n4. fourth");
  });

  it("rejects a code block inside a list item instead of flattening it", () => {
    const list: TTNode = {
      type: "bulletList",
      content: [{ type: "listItem", content: [para(text("item")), codeBlock("a\nb")] }],
    };
    expect(() => tiptapDocToMarkdown(doc(list))).toThrow(/codeBlock.*listItem/);
  });

  it("paragraph followed by bullet list", () => {
    const result = tiptapDocToMarkdown(doc(para(text("intro")), bulletList("a", "b")));
    expect(result).toBe("intro\n- a\n- b");
  });
});

// ── Edge cases ────────────────────────────────────────────────────────────────

describe("tiptapDocToMarkdown — edge cases", () => {
  it("rejects a mention node in v2 instead of silently dropping it", () => {
    expect(() =>
      tiptapDocToMarkdown(
        doc(para(mention("user", "11111111-1111-1111-1111-111111111111", "Ana"))),
        "v2",
      ),
    ).toThrow(/mention.*v3/i);
  });

  it("rejects a malformed mention node in v3 instead of silently dropping it", () => {
    expect(() => tiptapDocToMarkdown(doc(para({ type: "mention", attrs: {} })), "v3")).toThrow(
      /invalid mention/i,
    );
  });

  it("unknown inline node type returns empty string (no crash)", () => {
    const result = tiptapDocToMarkdown(doc(para({ type: "unknown", content: [] })));
    expect(result).toBe("");
  });

  it("italic takes priority when italic mark is present without bold or code", () => {
    const result = tiptapDocToMarkdown(doc(para(text("hi", "italic"))));
    expect(result).toBe("*hi*");
  });

  it("codeBlock with no content array uses empty string body", () => {
    const result = tiptapDocToMarkdown(doc({ type: "codeBlock" }));
    expect(result).toBe("```\n\n```");
  });

  it("text node with no text property uses empty string", () => {
    const result = tiptapDocToMarkdown(doc(para({ type: "text" })));
    expect(result).toBe("");
  });

  it("text node with no marks array uses plain text", () => {
    const result = tiptapDocToMarkdown(
      doc(para({ type: "text", text: "plain" })), // no marks key
    );
    expect(result).toBe("plain");
  });

  it("bulletList item with no content uses empty item text", () => {
    const result = tiptapDocToMarkdown(
      doc({
        type: "bulletList",
        content: [{ type: "listItem" }], // no content on listItem
      }),
    );
    expect(result).toBe("- ");
  });
});

describe("tiptapDocToMarkdown — XSS safety", () => {
  it("text containing angle brackets is preserved as literal text (not HTML)", () => {
    const result = tiptapDocToMarkdown(doc(para(text("<script>alert(1)</script>"))));
    // Output is plain Markdown string — no HTML execution risk downstream
    // because RichTextRenderer uses React text nodes, not dangerouslySetInnerHTML.
    expect(result).toBe("<script>alert(1)</script>");
    expect(result).not.toContain("<b>"); // no HTML markup injected
  });
});

// ── applyMarks — combined mark branches ──────────────────────────────────────

describe("applyMarks — combined and priority", () => {
  it("code beats bold+italic", () => {
    expect(applyMarks("hi", [{ type: "bold" }, { type: "code" }])).toBe("`hi`");
  });

  it("bold+italic uses the canonical combined marker", () => {
    const result = applyMarks("hi", [{ type: "bold" }, { type: "italic" }]);
    expect(result).toBe("***hi***");
  });

  it("bold+italic survives tiptapDocToMarkdown", () => {
    const result = tiptapDocToMarkdown(doc(para(text("hi", "bold", "italic"))));
    expect(result).toBe("***hi***");
  });

  it("no marks returns plain text", () => {
    expect(applyMarks("plain", [])).toBe("plain");
  });

  it("orderedList with null content uses empty item text", () => {
    const result = tiptapDocToMarkdown(
      doc({
        type: "orderedList",
        content: [{ type: "listItem" }], // no content on listItem
      }),
    );
    expect(result).toBe("1. ");
  });

  it("bulletList node with null content array returns empty string", () => {
    const result = tiptapDocToMarkdown({ type: "doc", content: [{ type: "bulletList" }] });
    expect(result).toBe("");
  });

  it("orderedList node with null content array returns empty string", () => {
    const result = tiptapDocToMarkdown({ type: "doc", content: [{ type: "orderedList" }] });
    expect(result).toBe("");
  });

  it("tiptapDocToMarkdown with null doc.content returns empty string", () => {
    const result = tiptapDocToMarkdown({ type: "doc" }); // content is undefined
    expect(result).toBe("");
  });

  it("paragraph with null content array returns empty string", () => {
    const result = tiptapDocToMarkdown({ type: "doc", content: [{ type: "paragraph" }] });
    expect(result).toBe("");
  });
});

describe("tiptapDocToMarkdown — list item multi-paragraph", () => {
  it("bulletList item with two paragraphs joins them with space", () => {
    const result = tiptapDocToMarkdown({
      type: "doc",
      content: [
        {
          type: "bulletList",
          content: [
            {
              type: "listItem",
              content: [para(text("first para")), para(text("second para"))],
            },
          ],
        },
      ],
    });
    expect(result).toBe("- first para second para");
  });

  it("orderedList item with two paragraphs joins them with space", () => {
    const result = tiptapDocToMarkdown({
      type: "doc",
      content: [
        {
          type: "orderedList",
          content: [
            {
              type: "listItem",
              content: [para(text("a")), para(text("b"))],
            },
            {
              type: "listItem",
              content: [para(text("c"))],
            },
          ],
        },
      ],
    });
    expect(result).toBe("1. a b\n2. c");
  });
});

describe("tiptapDocToMarkdown — nested lists", () => {
  it("preserves every nested item with canonical two-space indentation", () => {
    const result = tiptapDocToMarkdown(
      doc({
        type: "bulletList",
        content: [
          {
            type: "listItem",
            content: [
              para(text("parent")),
              {
                type: "orderedList",
                content: [
                  {
                    type: "listItem",
                    content: [
                      para(text("child")),
                      {
                        type: "bulletList",
                        content: [{ type: "listItem", content: [para(text("grandchild"))] }],
                      },
                    ],
                  },
                ],
              },
            ],
          },
        ],
      }),
    );

    expect(result).toBe("- parent\n  1. child\n    - grandchild");
  });
});
