package health

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

func LivenessHandler(service, version, commit string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, NewLiveness(service, version, commit))
	}
}

func ReadinessHandler(service, version, commit string, checks []Checker, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response, statusCode := EvaluateReadiness(service, version, commit, checks, timeout)
		httputil.WriteJSON(w, statusCode, response)
	}
}
