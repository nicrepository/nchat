package ws

import "errors"

// ErrSubscribeForbidden is returned when a client is not authorized to subscribe
// to a target. The error is intentionally non-enumerating: it does not reveal
// whether the target exists or the exact reason for denial.
var ErrSubscribeForbidden = errors.New("subscribe forbidden")
