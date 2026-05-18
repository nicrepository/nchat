package server

import (
	"encoding/json"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/health"
)

func NewHandler(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(health.New(serviceName))
	})

	return mux
}
