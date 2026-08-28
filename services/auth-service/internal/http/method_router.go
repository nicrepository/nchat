package httpapi

import (
	"net/http"
	"sort"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

func methodRouter(routes map[string]http.Handler) http.Handler {
	allowed := make([]string, 0, len(routes))
	for method := range routes {
		allowed = append(allowed, method)
	}
	sort.Strings(allowed)
	allowHeader := strings.Join(allowed, ", ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler := routes[r.Method]; handler != nil {
			handler.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Allow", allowHeader)
		httputil.WriteError(w, http.StatusMethodNotAllowed, httputil.ErrCodeBadRequest, "method not allowed")
	})
}
