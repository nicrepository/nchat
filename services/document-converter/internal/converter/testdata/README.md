# Test fixtures

`valid.ppt`, `blocked-objectpool.doc` (originally `novpapplan.doc`) and
`no-powerpoint-stream.xls` (originally `test.xls`) are copied unmodified from
`github.com/richardlehane/mscfb`'s own `test/` directory (Apache License 2.0),
used here as real, structurally valid CFB/OLE2 documents that `validatePPT`
can be tested against without hand-crafting the binary format:

- `valid.ppt` has a genuine `PowerPoint Document` stream and no active-content
  stream — the success path.
- `blocked-objectpool.doc` has an `ObjectPool` stream, which
  `validatePPT`'s blocklist matches on the substring `objectpool` — the
  blocked-active-content path.
- `no-powerpoint-stream.xls` is a valid CFB file with neither a
  `PowerPoint Document` stream nor anything blocklisted — the
  "valid container, wrong contents" path.
