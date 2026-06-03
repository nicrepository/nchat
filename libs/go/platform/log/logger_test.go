package log

import "testing"

func TestNewReturnsLogger(t *testing.T) {
	logger := New("auth-service", "test")

	if logger == nil {
		t.Fatal("expected logger")
	}
}

func TestNewLoggerEmitsWithoutPanic(_ *testing.T) {
	logger := New("auth-service", "test")

	logger.Info("test log")
}
