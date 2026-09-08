package agent

import (
	"errors"
	"fmt"
)

type ErrorCode string

const (
	ErrorInvalidArgument   ErrorCode = "invalid_argument"
	ErrorNotFound          ErrorCode = "not_found"
	ErrorPermissionDenied  ErrorCode = "permission_denied"
	ErrorConflict          ErrorCode = "conflict"
	ErrorInProgress        ErrorCode = "in_progress"
	ErrorNotReady          ErrorCode = "not_ready"
	ErrorUnsupported       ErrorCode = "unsupported"
	ErrorRateLimited       ErrorCode = "rate_limited"
	ErrorResourceExhausted ErrorCode = "resource_exhausted"
	ErrorTimeout           ErrorCode = "timeout"
	ErrorCancelled         ErrorCode = "cancelled"
	ErrorUnavailable       ErrorCode = "unavailable"
	ErrorStorageFailure    ErrorCode = "storage_failure"
	ErrorProviderFailure   ErrorCode = "provider_failure"
	ErrorIntegrityFailure  ErrorCode = "integrity_failure"
	ErrorDeliveryPending   ErrorCode = "delivery_pending"
	ErrorUnknownOutcome    ErrorCode = "unknown_outcome"
	ErrorInternal          ErrorCode = "internal"
)

type Error struct {
	code  ErrorCode
	op    string
	cause error
}

func NewError(code ErrorCode, op string, cause error) error {
	if code == "" {
		code = ErrorInternal
	}
	return &Error{code: code, op: op, cause: cause}
}

func Errorf(code ErrorCode, op, format string, args ...any) error {
	return NewError(code, op, fmt.Errorf(format, args...))
}

func (e *Error) Error() string {
	switch {
	case e.op != "" && e.cause != nil:
		return e.op + ": " + e.cause.Error()
	case e.op != "":
		return e.op + ": " + string(e.code)
	case e.cause != nil:
		return e.cause.Error()
	default:
		return string(e.code)
	}
}

func (e *Error) Unwrap() error   { return e.cause }
func (e *Error) Code() ErrorCode { return e.code }

func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.code
	}
	return ErrorInternal
}

func IsCode(err error, code ErrorCode) bool { return CodeOf(err) == code }
