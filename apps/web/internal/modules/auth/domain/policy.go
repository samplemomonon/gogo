package domain

import "errors"

var (
	ErrPolicyDenied         = errors.New("policy administration permission denied")
	ErrInvalidPolicyChange  = errors.New("invalid policy change")
	ErrPolicyConflict       = errors.New("policy change conflicts with existing state")
	ErrPolicyTargetNotFound = errors.New("policy target not found")
)
