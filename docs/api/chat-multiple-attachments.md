# Multiple attachments in one chat message

Issue: not linked (local provisional branch uses issue `0`).

Channels, direct messages, and group conversations accept an ordered
`attachment_ids` array. Each file is uploaded independently to file-service,
but chat-service validates and associates the complete array in the same SQL
statement that creates the message. A rejected candidate therefore creates no
partial message.

`GET /api/chat/sidebar` publishes the effective message limits in `workspace`:

| Field                          |     Default | Meaning                                       |
| ------------------------------ | ----------: | --------------------------------------------- |
| `max_message_attachments`      |        `10` | Maximum ordered attachment IDs in one message |
| `max_message_attachment_bytes` | `536870912` | Maximum sum of plaintext `size_bytes`         |

Both values are server-authoritative. A client that does not receive the new
fields must limit itself to one attachment during a rolling deployment.

Uploads from the composer include multipart field `purpose=message_draft`.
file-service assigns `draft_expires_at`; the client may cancel an unpublished
draft with `DELETE /api/files/attachments/{attachmentID}`. Only its uploader
can cancel it, associated attachments cannot be cancelled, and all denied
outcomes use the same non-enumerating response. A bounded worker expires stale
drafts and queues their objects for durable cleanup.

For idempotency, zero or one attachment retains the `create.v1` request
fingerprint. Ordered batches use `create.v2`; changing either the caption or
the attachment order requires a new `Idempotency-Key`.
