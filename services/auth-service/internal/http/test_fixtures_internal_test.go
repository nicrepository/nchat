package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
)

func makeInternalTestOpaqueValue(label string) string {
	sum := sha256.Sum256([]byte("nchat-http-internal-test:" + label))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
