package service

import (
	"strings"

	"github.com/google/uuid"
)

type mentionToken struct {
	start int
	end   int
	kind  string
	id    string
}

func mentionTokens(body string) []mentionToken {
	var tokens []mentionToken
	for start := 0; start < len(body)-2; start++ {
		if body[start] != '@' || body[start+1] != '[' || escapedAt(body, start) {
			continue
		}
		labelEnd := start + 2
		for labelEnd < len(body) {
			if body[labelEnd] == '\\' {
				labelEnd += 2
				continue
			}
			if body[labelEnd] == ']' && labelEnd+1 < len(body) && body[labelEnd+1] == '(' {
				break
			}
			labelEnd++
		}
		if labelEnd >= len(body) || labelEnd+2 >= len(body) {
			continue
		}
		contentStart := labelEnd + 2
		close := strings.IndexByte(body[contentStart:], ')')
		if close < 0 {
			continue
		}
		contentEnd := contentStart + close
		end := contentEnd + 1
		parts := strings.Split(body[contentStart:contentEnd], ":")
		if len(parts) != 3 || parts[0] != "mention" || (parts[1] != "user" && parts[1] != "channel") {
			continue
		}
		id, err := uuid.Parse(parts[2])
		if err != nil {
			continue
		}
		tokens = append(tokens, mentionToken{start: start, end: end, kind: parts[1], id: id.String()})
		start = end - 1
	}
	return tokens
}

func escapedAt(body string, index int) bool {
	slashes := 0
	for i := index - 1; i >= 0 && body[i] == '\\'; i-- {
		slashes++
	}
	return slashes%2 == 1
}

func extractMentionIDs(body string) ([]string, []string) {
	users, channels := []string{}, []string{}
	seenUsers, seenChannels := map[string]bool{}, map[string]bool{}
	for _, token := range mentionTokens(body) {
		if token.kind == "user" && !seenUsers[token.id] {
			seenUsers[token.id] = true
			users = append(users, token.id)
		}
		if token.kind == "channel" && !seenChannels[token.id] {
			seenChannels[token.id] = true
			channels = append(channels, token.id)
		}
	}
	return users, channels
}

func rewriteMentionLabels(body string, labels map[string]string) string {
	var out strings.Builder
	last := 0
	for _, token := range mentionTokens(body) {
		label, ok := labels[token.kind+":"+token.id]
		if !ok {
			continue
		}
		out.WriteString(body[last:token.start])
		out.WriteString("@[")
		out.WriteString(escapeMentionLabel(label))
		out.WriteString("](mention:")
		out.WriteString(token.kind)
		out.WriteByte(':')
		out.WriteString(token.id)
		out.WriteByte(')')
		last = token.end
	}
	if last == 0 {
		return body
	}
	out.WriteString(body[last:])
	return out.String()
}

func escapeMentionLabel(label string) string {
	return strings.NewReplacer(
		"\\", "\\\\",
		"@", "\\@",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
	).Replace(label)
}
