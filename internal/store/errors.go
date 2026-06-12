package store

import "errors"

var (
	ErrNotFound                     = errors.New("store: not found")
	ErrTaskConflict                 = errors.New("store: task_conflict")
	ErrTaskIDOwnedByDifferentOrigin = errors.New("store: task id belongs to a different origin")
	ErrInvalidStateTransition       = errors.New("store: invalid task state transition")
	ErrExpiredIdempotencyRetention  = errors.New("store: idempotency retention must be positive")
)
