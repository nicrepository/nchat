package httpapi_test

import (
	"crypto/sha256"
	"encoding/base64"
)

func makeTestOpaqueValue(label string) string {
	sum := sha256.Sum256([]byte("nchat-http-test:" + label))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
