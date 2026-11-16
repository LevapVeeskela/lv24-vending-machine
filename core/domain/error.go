package domain

import "errors"

// ErrorCode classifies domain and provider failures.
type ErrorCode string

// Stable error codes shared by the core and providers.
const (
	// CodeAuth signals missing or invalid credentials.
	CodeAuth ErrorCode = "auth"
	// CodePaymentRequired signals that the provider needs payment or quota.
	CodePaymentRequired ErrorCode = "payment_required"
	// CodeRateLimit signals that the caller was throttled.
	CodeRateLimit ErrorCode = "rate_limit"
	// CodeInvalidRequest signals a malformed request from the caller.
	CodeInvalidRequest ErrorCode = "invalid_request"
	// CodeInvalidOutput signals a malformed response from the provider.
	CodeInvalidOutput ErrorCode = "invalid_output"
	// CodeUnavailable signals a transient provider outage.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeTimeout signals a deadline exceeded.
	CodeTimeout ErrorCode = "timeout"
	// CodeUnknown is the fallback for unclassified errors.
	CodeUnknown ErrorCode = "unknown"
)

// Error is a coded failure with an optional cause.
//
// Cause may carry secrets and is never included in Error output.
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Cause     error
}

// Error renders "<code>: <message>" without the cause.
func (e *Error) Error() string {
	if e == nil {
		return string(CodeUnknown) + ": <nil>"
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewError builds an *Error with the given code and message.
func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

// WrapError builds an *Error with the given code, message and cause.
func WrapError(code ErrorCode, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

// CodeOf extracts the ErrorCode from err via errors.As.
//
// Errors without an *Error in the chain map to CodeUnknown.
func CodeOf(err error) ErrorCode {
	if err == nil {
		return CodeUnknown
	}
	var de *Error
	if errors.As(err, &de) {
		if de == nil || de.Code == "" {
			return CodeUnknown
		}
		return de.Code
	}
	return CodeUnknown
}

// IsRetryable reports whether err is worth retrying, by code.
//
// rate_limit, unavailable and timeout are retryable; auth,
// payment_required, invalid_request, invalid_output and unknown are not.
func IsRetryable(err error) bool {
	switch CodeOf(err) {
	case CodeRateLimit, CodeUnavailable, CodeTimeout:
		return true
	default:
		return false
	}
}
