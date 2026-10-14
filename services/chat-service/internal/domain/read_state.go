package domain

import "time"

// ReadThrough is how far a reader has read one conversation (issue #1082).
//
// With a MessageID it is a position in the timeline's canonical
// (created_at, id) order: that message and everything before it are read. With
// none it is an instant — everything created at or before CreatedAt is read —
// which is what a read state written before the cursor existed means.
type ReadThrough struct {
	CreatedAt time.Time
	MessageID *string
}

// ConversationReadState is the server's answer to "what has this reader not
// read here": the count, and the point it is counted from.
type ConversationReadState struct {
	UnreadCount int
	ReadThrough *ReadThrough
}
