package server

import "net/http"

type principalKey struct{}
type Principal struct {
	UserID    string
	SessionID string
}

func authenticatedPrincipal(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(Principal)
	return p, ok
}
func authenticatedUserID(r *http.Request) string {
	p, ok := authenticatedPrincipal(r)
	if !ok {
		return ""
	}
	return p.UserID
}
