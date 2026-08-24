package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// maxRequestBodyBytes bounds every administrative request body.
//
// The bodies this API accepts hold one or two scalars, so 64 KiB is already
// several orders of magnitude more than any of them needs. The limit is applied
// before decoding, so an oversized body is refused rather than buffered — the
// same ceiling chat-service applies to its administrative endpoints.
const maxRequestBodyBytes = 64 << 10

// errInvalidQuery is returned by the parsers below. It carries no detail about
// which value was wrong beyond what the handler chooses to say, so a caller
// cannot use error text to enumerate what the allowlists contain.
var errInvalidQuery = errors.New("invalid query parameter")

// parsePageParams reads the two parameters every listing accepts.
//
// The limit is validated but not bounded here: the ceiling lives in
// domain.ClampPageSize, so one definition applies whether the value arrived
// from a query string, from a default, or from a caller that skipped this
// layer. A non-numeric or non-positive limit is refused rather than silently
// defaulted, because "limit=abc" is a bug in the caller and answering it with a
// full page hides that.
func parsePageParams(query map[string][]string) (int, domain.Cursor, error) {
	limit := 0
	if raw := firstValue(query, "limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return 0, domain.Cursor{}, errInvalidQuery
		}
		limit = parsed
	}
	cursor, err := domain.DecodeCursor(firstValue(query, "cursor"))
	if err != nil {
		return 0, domain.Cursor{}, errInvalidQuery
	}
	return limit, cursor, nil
}

// allowlisted resolves an optional filter against a closed set.
//
// An empty value means "filter not applied". Anything else must be a member of
// the set: an unrecognised value is an error, never a filter that silently
// matches nothing or, worse, everything.
func allowlisted[T any](query map[string][]string, name string, allowed map[string]T) (string, error) {
	value := strings.TrimSpace(firstValue(query, name))
	if value == "" {
		return "", nil
	}
	if _, ok := allowed[value]; !ok {
		return "", errInvalidQuery
	}
	return value, nil
}

// parseTriState reads a filter that means "yes", "no" or "either".
//
// Nil is not false. A missing platform_admin parameter must list everybody, and
// collapsing it into false would quietly hide every administrator from the
// default view of the directory.
func parseTriState(query map[string][]string, name string) (*bool, error) {
	raw := strings.TrimSpace(firstValue(query, name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errInvalidQuery
	}
	return &value, nil
}

// parseBoundedInt reads an optional numeric filter with an inclusive ceiling.
func parseBoundedInt(query map[string][]string, name string, max int) (int, error) {
	raw := strings.TrimSpace(firstValue(query, name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > max {
		return 0, errInvalidQuery
	}
	return value, nil
}

// parseUserActivity resolves the inactivity bucket, which is one closed set
// plus the "never signed in" sentinel that is not a duration.
func parseUserActivity(query map[string][]string) (string, error) {
	value := strings.TrimSpace(firstValue(query, "inactivity"))
	switch value {
	case "":
		return "", nil
	case domain.ActivityFilterNever:
		return value, nil
	default:
		if _, ok := domain.UserActivityFilter[value]; !ok {
			return "", errInvalidQuery
		}
		return value, nil
	}
}

// parseUUIDFilter reads an optional identifier filter and refuses a malformed
// one, so a bad value becomes a 400 rather than a database type error.
func parseUUIDFilter(query map[string][]string, name string) (string, error) {
	value := strings.TrimSpace(firstValue(query, name))
	if value == "" {
		return "", nil
	}
	if !domain.ValidUUID(value) {
		return "", errInvalidQuery
	}
	return value, nil
}

// searchTerm bounds the free-text search.
//
// The cap is not about safety — the term is a bound parameter — but about
// keeping a pathological pattern from being handed to the database at all.
const maxSearchTermLength = 128

func parseSearchTerm(query map[string][]string) (string, error) {
	value := strings.TrimSpace(firstValue(query, "q"))
	if len(value) > maxSearchTermLength {
		return "", errInvalidQuery
	}
	return value, nil
}

func firstValue(query map[string][]string, name string) string {
	values := query[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// decodeJSONBody reads a request body into a strict, allowlisted request type.
//
// Three properties, all of which matter:
//
//   - the body is capped before it is read, so no request can make this service
//     buffer an arbitrary amount;
//   - DisallowUnknownFields refuses anything the request type does not name. A
//     field the API never agreed to accept cannot be smuggled past a handler and
//     bound onto something it was not meant to touch — which is the mass
//     assignment this API is not willing to be one decoder change away from;
//   - exactly one JSON value is accepted, so a body carrying a second object
//     after the first is refused rather than half-applied.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return false
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
		return false
	}
	return true
}

// requireEmptyBody enforces the contract of a POST that takes no body.
//
// Two routes declare "request body: none" — the integration diagnostic and the
// SMTP test message — and neither has a field a caller could send. Silently
// accepting `{"unexpected":"payload"}` would make the documented contract a
// suggestion, and would leave a future field free to arrive from a client that
// was never reviewed.
//
// The rule is the **absence of a body**, not the absence of meaningful JSON, so
// `{}` is refused too. That is also why this cannot be a JSON decoder: a decoder
// accepts `{}` and would have to be taught to reject it.
//
// It reads through io.ReadFull over a one-byte limit rather than consulting
// Content-Length, which is unset for a chunked request and is a claim rather
// than a fact in any case. The three outcomes are exactly the three questions:
//
//	io.EOF  nothing to read — the request is empty, as promised
//	nil     a byte was read — there is a body, whatever it holds
//	other   the body could not be read — the request is broken
//
// At most one byte is ever read, so an oversized payload is refused without
// being buffered.
func requireEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	var probe [1]byte
	_, err := io.ReadFull(io.LimitReader(r.Body, 1), probe[:])
	switch {
	case errors.Is(err, io.EOF):
		return true
	case err == nil:
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "this endpoint accepts no request body")
	default:
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
	}
	return false
}

// integerField reads a required JSON integer out of a request type.
//
// The field is captured as raw JSON and parsed as a base-10 integer literal,
// which is what makes the refusals exact. Everything below is rejected rather
// than coerced into a plausible-looking policy nobody asked for:
//
//	1.5      a decimal is not a limit
//	1e8      exponent notation is not an integer literal
//	"30"     a string is not a number — json.Number would have accepted this
//	         one, because it is itself a string type
//	null     an explicit null is not a value
//	absent   an unset field is not a value
//	9999…9   a number too large for int64 cannot wrap into a small positive one
func integerField(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	parsed, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
