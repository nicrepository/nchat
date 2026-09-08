package domain

// MentionType identifies the target represented by a mention token.
type MentionType string

const (
	MentionTypeUser    MentionType = "user"
	MentionTypeChannel MentionType = "channel"
)

// MentionCandidate is an authorized autocomplete result.
type MentionCandidate struct {
	Type  MentionType
	ID    string
	Label string
}

// MaxGroupAllMentionRecipients bounds how many eligible recipients a single
// @all in a group DM may notify (issue #776, SR-002).
//
// This is a distinct contract from MaxGroupDMParticipants-style caps
// elsewhere: it bounds a broadcast's actual audience — active dm_members,
// with active workspace membership, on an active, non-deleted account — not
// how many rows a single AddGroupParticipants request may write, and not any
// total capacity a group conversation itself may hold. A group larger than
// this may exist and keep working normally for every other purpose; only its
// @all is refused, and only for as long as its eligible membership stays
// above the bound.
//
// It is a product policy decision, not a technical default: no bound existed
// for this anywhere in the codebase before it, so the number is deliberately
// its own named constant rather than a reuse of an unrelated cap that happens
// to share the value 50.
const MaxGroupAllMentionRecipients = 50
