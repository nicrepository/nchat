# TipTap Lossless Codec Design

## Goal

Replace the divergent TipTap serializer and rich-text renderer rules with one
bidirectional grammar so supported editor documents retain their visible text,
marks, code blocks, and list hierarchy after storage and rendering.

## Root cause

`tiptapSerializer.ts` applies an exclusive mark priority, emits literal text
without escaping, and ignores nested list nodes. `RichTextRenderer.tsx` parses a
separate regular expression and flat block grammar. `richTextMarkers.ts` only
shares some delimiters, so it does not prevent either implementation from
diverging. Tests currently codify the bold-plus-italic loss and simulate paste
through `insertContent()` and a production DOM backdoor.

## Canonical grammar

`richTextMarkers.ts` is the single source for all delimiters, escaping rules,
list indentation, and block predicates. Serializer and renderer import those
definitions rather than duplicating syntax.

````text
bold          ::= **escaped-content**
italic        ::= *escaped-content*
bold-italic   ::= ***escaped-content***
inline-code   ::= `escaped-content`
code-block    ::= ``` "\n" escaped-content "\n" ```
unordered     ::= indent "- " inline-content
ordered       ::= indent number ". " inline-content
indent        ::= two spaces per nesting level
````

Code remains exclusive when combined with presentation marks because TipTap's
configured Code extension excludes them. Bold and italic together use the
canonical `***text***` form and render as nested `<strong><em>` elements.

Nested lists are serialized recursively with two spaces per level and parsed
recursively by the renderer. Mixed ordered and unordered child lists retain
their type and order. List-item paragraphs retain all visible content; nested
nodes are never silently dropped.

## Symmetric escaping

Literal text is escaped before mark delimiters are added:

| Literal                       | Stored form          |
| ----------------------------- | -------------------- |
| `\`                           | `\\`                 |
| `*`                           | `\*`                 |
| `_`                           | `\_`                 |
| backtick                      | backslash + backtick |
| `-`                           | `\-`                 |
| `.` immediately after a digit | `\.`                 |

Escaping `-` and digit-followed dots everywhere is intentionally canonical and
context-free; it covers list-capable line starts without relying on text-node
boundaries. The renderer removes only recognized escapes. An unknown escape
such as `\x` retains its backslash. The same helpers handle marked and code
content, preventing embedded delimiters from terminating syntax early.

Unescaped legacy `**bold**`, `*italic*`, inline code, fenced code blocks, and
flat lists remain valid. Legacy `***bold+italic***` gains the intended combined
rendering.

## Round-trip property

For every document `D` composed from the configured RF-11 nodes:

```text
render(serialize(D)) ≡ D
```

Semantic equivalence preserves visible text, bold, italic, combined
bold-plus-italic, inline code, code blocks, list type, item order, and list
hierarchy. For every literal text value `T`:

```text
textContent(render(serialize(text(T)))) = T
```

Integration tests must exercise the renderer, not only the stored Markdown-like
string.

## Production interaction tests

Paste tests render `ChatComposer`, dispatch a clipboard `paste` event to its
public contenteditable element, send through the normal button, capture the
stored body, and render that body with `RichTextRenderer`. They cover formatted
HTML, literal markers, nested pasted lists, and unsafe HTML handling.

`ChatComposer` no longer exports an editor instance through `__tiptapEditor`.
Existing component tests use public paste and keyboard events; hook-level state
tests continue through `renderHook(useChatEditor)`.

## Scope reductions

History is removed from the production extension list and dependencies because
RF-11 only requires bold, italic, inline code, code block, unordered list, and
ordered list. The unmeasured TipTap `manualChunks` configuration is removed with
it. No new extension, Markdown dependency, HTML renderer, or unrelated refactor
is introduced.

## Verification

The change is complete only after the required Node 24 install, formatting,
lint, typecheck, test, coverage, build, production audit, and diff checks pass.
The changed code also receives correctness and security review plus a Semgrep
scan, with particular attention to XSS and unsafe HTML rendering.
