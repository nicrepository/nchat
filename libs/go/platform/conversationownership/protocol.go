// Package conversationownership holds the transaction protocol shared by
// account invalidation writers and chat conversation writers.
package conversationownership

import (
	"context"
	"errors"
)

const SerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"

// Auth can deploy before chat's expand migration. Until the helper exists,
// ownership is disabled and invalidation retains its existing semantics.
const AvailabilitySQL = `SELECT to_regprocedure('chat.lock_user_ownership_conversations(uuid,uuid)') IS NOT NULL`
const LockUserSQL = `SELECT chat.lock_user_ownership_conversations($1::uuid,NULL)`

type sqlStateError interface{ SQLState() string }

func SQLState(err error) string {
	var state sqlStateError
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return ""
}

func Retry[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	var value T
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		value, err = operation()
		state := SQLState(err)
		if state != "40001" && state != "40P01" {
			return value, err
		}
		if ctx.Err() != nil {
			return value, ctx.Err()
		}
	}
	return value, err
}
