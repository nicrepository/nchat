package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONWrapsPayloadInEnvelope(t *testing.T) {
	response := httptest.NewRecorder()

	WriteJSON(response, http.StatusAccepted, map[string]string{"status": "ok"})

	if response.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", response.Code)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json content type, got %q", response.Header().Get("Content-Type"))
	}

	var body Envelope
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != nil {
		t.Fatalf("expected no error, got %+v", body.Error)
	}
}

func TestWriteJSONReturnsInternalErrorOnEncodeFailure(t *testing.T) {
	response := httptest.NewRecorder()

	WriteJSON(response, http.StatusOK, make(chan int))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", response.Code)
	}

	var body Envelope
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error == nil || body.Error.Code != ErrCodeInternal {
		t.Fatalf("expected internal error, got %+v", body.Error)
	}
}

func TestWriteErrorWritesStandardErrorEnvelope(t *testing.T) {
	response := httptest.NewRecorder()

	WriteError(response, http.StatusForbidden, ErrCodeForbidden, "forbidden")

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", response.Code)
	}

	var body Envelope
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error == nil {
		t.Fatal("expected error envelope")
	}
	if body.Error.Code != ErrCodeForbidden {
		t.Fatalf("expected forbidden code, got %q", body.Error.Code)
	}
	if body.Error.Message != "forbidden" {
		t.Fatalf("expected forbidden message, got %q", body.Error.Message)
	}
}
