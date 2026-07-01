# Chat Codec V2 Design

## Goal

Preserve every persisted legacy message exactly under the original grammar while making new TipTap messages lossless and keeping non-chat routes free of the TipTap bundle.

## Format versioning

Use storage-backed versioning (option A). Add `chat.messages.body_format` as `TEXT NOT NULL DEFAULT 'v1'` with a `v1`/`v2` check constraint. Existing rows and clients that omit the field remain v1. The new web client sends v2 and treats a missing response field as v1 for rolling reads. Deployment order is backend, then frontend.

Option B is rejected because a body sentinel can collide with historical content and pollutes `body_text`. Option C is rejected because legacy `\*` and v2 escaped `\*` are byte-identical and cannot be decoded deterministically without a version.

## Rendering

`richTextMarkers.ts` owns both grammars. V1 reproduces the former behavior: no unescaping, no indented lists, top-level list markers and the original inline marker rules. V2 retains symmetric escaping, nested lists, combined bold/italic and records an ordered list's first number so `<ol start>` is preserved. React text nodes remain the only output; no HTML parsing is introduced.

## Serialization and editor schema

The serializer reads `orderedList.attrs.start` and numbers items from it. A chat list item is restricted to paragraphs and nested lists, excluding code blocks that the text grammar cannot represent inside a list. The serializer also throws if out-of-schema JSON contains a code block in a list item, preventing silent flattening or data loss.

## Interaction and loading

The Shift+Enter regression test must observe a real `<br>` and an outgoing body containing `\n`. `App.tsx` loads `ChatMessageArea` with `React.lazy` and `Suspense`; authentication and admin routes therefore do not evaluate the TipTap dependency graph.

## Verification

Tests cover real legacy backslashes, marker characters, regex/code, numeric text and indented lines; v2 escaping; ordered lists starting at 3; explicit rejection/normalization of list code blocks; Shift+Enter DOM and serialization; API/migration defaults and validation; and XSS preservation. Final gates are the requested Node 24 web checks, production audit, Go tests/vet/vulnerability scan, migration checks, build chunk inspection and `git diff --check`.
