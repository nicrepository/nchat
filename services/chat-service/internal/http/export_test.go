// Package httpapi — test export helpers.
// This file is compiled only during tests (package httpapi, not httpapi_test)
// and exposes internal symbols needed to build request contexts in unit tests.
package httpapi

var (
	ExportCtxKeyUserID    = ctxKeyUserID
	ExportCtxKeySessionID = ctxKeySessionID
	// ExportBearerScheme is the RFC 6750 Authorization header scheme used by
	// BearerAuth middleware. Tests use this constant instead of raw string literals.
	ExportBearerScheme = bearerScheme
)
