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
	body, err := json.Marshal(Envelope{Data: payload})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeInternal, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func WriteError(w http.ResponseWriter, status int, code string, message string) {
	body, err := json.Marshal(Envelope{
		Error: &ErrorResponse{
			Code:    code,
			Message: message,
		},
	})
	if err != nil {
		body = []byte(`{"error":{"code":"internal_error","message":"internal error"}}`)
		status = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
