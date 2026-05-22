package observability

import "context"

// noopShutdown is a no-op shutdown function returned when tracing is disabled
// or when the OTLP exporter cannot be initialised.
func noopShutdown(_ context.Context) error {
	return nil
}
