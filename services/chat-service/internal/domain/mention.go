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
