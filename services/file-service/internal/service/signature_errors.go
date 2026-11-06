package service

import "errors"

// Categories cross the storage boundary without exposing SQL or policy contents.
var (
	ErrSignaturePolicyInactive = errors.New("signature policy inactive")
	ErrSignaturePolicyInvalid  = errors.New("signature policy missing or invalid")
	ErrScanSuperseded          = errors.New("scan target changed status")
	ErrScanAlreadyRecorded     = errors.New("same scan already recorded; no new transition")
	ErrScanConflict            = errors.New("scan persistence conflict")
)
