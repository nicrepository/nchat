// Package documentguard validates passive package XML before document processing.
package documentguard

import (
	"errors"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrUnsafe = errors.New("unsafe document XML or reference")

// Resolve uses one URI decoding step. Encoded separators, dot segments and
// percent signs are refused to avoid different interpretations by converters.
// exists is nil for standalone XML, where only same-document fragments are valid.
func Resolve(source, reference string, fragments bool, exists func(string) bool) (string, error) {
	u, err := parseReference(reference, fragments)
	if err != nil {
		return "", err
	}
	if u.Path == "" {
		return fragmentTarget(source, u, fragments)
	}
	if ambiguousPath(u) || exists == nil {
		return "", ErrUnsafe
	}
	target := path.Join(path.Dir(source), u.Path)
	if target == "." || target == ".." || strings.HasPrefix(target, "../") || !exists(target) {
		return "", ErrUnsafe
	}
	return target, nil
}

func parseReference(reference string, fragments bool) (*url.URL, error) {
	if strings.TrimSpace(reference) != reference || strings.IndexFunc(reference, unicode.IsControl) >= 0 {
		return nil, ErrUnsafe
	}
	u, err := url.Parse(reference)
	if err != nil || !localURI(u, fragments) {
		return nil, ErrUnsafe
	}
	return u, nil
}

func localURI(u *url.URL, fragments bool) bool {
	if u == nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return false
	}
	return !u.ForceQuery && u.RawQuery == "" && (fragments || u.Fragment == "")
}

func ambiguousPath(u *url.URL) bool {
	if !utf8.ValidString(u.Path) || strings.HasPrefix(u.Path, "/") || strings.ContainsAny(u.Path, `\:%`) || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 {
		return true
	}
	raw := strings.ToLower(u.EscapedPath())
	if strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") || strings.Contains(raw, "%2e") {
		return true
	}
	return false
}

func fragmentTarget(source string, u *url.URL, fragments bool) (string, error) {
	if fragments && u.Fragment != "" {
		return source, nil
	}
	return "", ErrUnsafe
}
