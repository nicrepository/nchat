package httputil

import (
	"encoding/json"
	"net/http"
)

type Envelope struct {
	Data  any            `json:"data,omitempty"`
	Error *ErrorResponse `json:"error,omitempty"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func WriteJSON(w http.ResponseWriter, status int, payload any) {
	env := Envelope{Data: payload}
	// Pre-marshal to detect errors before response headers are committed.
	if _, err := json.Marshal(env); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeInternal, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

func WriteError(w http.ResponseWriter, status int, code string, message string) {
	env := Envelope{
		Error: &ErrorResponse{
			Code:    code,
			Message: message,
		},
	}
	// Pre-marshal to detect errors before response headers are committed.
	if _, err := json.Marshal(env); err != nil {
		env = Envelope{Error: &ErrorResponse{Code: "internal_error", Message: "internal error"}}
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
