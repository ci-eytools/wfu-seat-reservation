package chaoxing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// Shanghai is the timezone used by the seat system. Asia/Shanghai has had no DST
// since 1991, so a fixed +08:00 offset is exact and avoids depending on system
// tzdata being installed.
var Shanghai = time.FixedZone("CST", 8*60*60)

// OperationalError corresponds to the Python RuntimeError: the remote service or
// the session failed. It is retryable in the sense that the user can refresh.
// When it wraps an underlying error, Unwrap exposes it so callers can classify
// network failures.
type OperationalError struct {
	Msg string
	Err error
}

func (e *OperationalError) Error() string { return e.Msg }
func (e *OperationalError) Unwrap() error { return e.Err }

// InputError corresponds to ValueError: the caller asked for something invalid
// (bad room id, unavailable seat, expired slot).
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// UnsupportedError corresponds to NotImplementedError: the room or configuration
// uses a mode this read-only client deliberately does not guess at.
type UnsupportedError struct{ Msg string }

func (e *UnsupportedError) Error() string { return e.Msg }

// SessionExpiredError marks a session that is no longer usable.
type SessionExpiredError struct{ Msg string }

func (e *SessionExpiredError) Error() string { return e.Msg }

func opErr(msg string) error { return &OperationalError{Msg: msg} }

func opErrf(format string, args ...any) error {
	return &OperationalError{Msg: fmt.Sprintf(format, args...)}
}

// opWrap builds an operational error that still exposes the underlying cause.
func opWrap(err error, format string, args ...any) error {
	return &OperationalError{Msg: fmt.Sprintf(format, args...), Err: err}
}

func inputErr(msg string) error       { return &InputError{Msg: msg} }
func unsupportedErr(msg string) error { return &UnsupportedError{Msg: msg} }
func expiredErr(msg string) error     { return &SessionExpiredError{Msg: msg} }

// IsBlocked reports whether err is a read-only policy refusal.
func IsBlocked(err error) bool {
	var target *BlockedSeatRequest
	return errors.As(err, &target)
}

// IsInput reports whether err is a caller-input problem.
func IsInput(err error) bool {
	var target *InputError
	return errors.As(err, &target)
}

// IsUnsupported reports whether err is an unsupported room/mode.
func IsUnsupported(err error) bool {
	var target *UnsupportedError
	return errors.As(err, &target)
}

// IsSessionExpired reports whether err means the login session is unusable.
func IsSessionExpired(err error) bool {
	var target *SessionExpiredError
	return errors.As(err, &target)
}

// IsOperational reports whether err came from the remote service or transport.
func IsOperational(err error) bool {
	var target *OperationalError
	return errors.As(err, &target)
}

// IsNetwork reports whether err is a transport-level failure rather than a
// protocol-level one. It is used to present a "cannot reach service" state.
func IsNetwork(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// Message returns the user-facing message of a classified error.
func Message(err error) string {
	if err == nil {
		return ""
	}
	var op *OperationalError
	if errors.As(err, &op) {
		return op.Msg
	}
	var in *InputError
	if errors.As(err, &in) {
		return in.Msg
	}
	var un *UnsupportedError
	if errors.As(err, &un) {
		return un.Msg
	}
	var se *SessionExpiredError
	if errors.As(err, &se) {
		return se.Msg
	}
	var bl *BlockedSeatRequest
	if errors.As(err, &bl) {
		return bl.Reason
	}
	return err.Error()
}
