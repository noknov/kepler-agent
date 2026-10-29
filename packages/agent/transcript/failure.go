package transcript

import (
	"context"
	"errors"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

// Failure retains error classification across persistence without serializing
// arbitrary Go error chains. The diagnostic message remains in Event.Error.
type Failure struct {
	Kind         model.ErrorKind `json:"kind,omitempty"`
	Retryable    bool            `json:"retryable,omitempty"`
	StatusCode   int             `json:"status_code,omitempty"`
	ContextError string          `json:"context_error,omitempty"`
	Cause        *Failure        `json:"cause,omitempty"`
}

func FailureFromError(err error) *Failure {
	var failure Failure
	var typed *model.Error
	if errors.As(err, &typed) {
		failure.Kind, failure.Retryable, failure.StatusCode = typed.Kind, typed.Retryable, typed.StatusCode
		failure.Cause = FailureFromError(typed.Cause)
		return &failure
	}
	switch {
	case errors.Is(err, context.Canceled):
		failure.ContextError = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		failure.ContextError = "deadline_exceeded"
	}
	if failure.Kind == "" && failure.ContextError == "" {
		return nil
	}
	return &failure
}

// Err restores the diagnostic error and the identities consumed by runtime and
// presentation. Cancellation is also explicit in the canonical event type.
func (e Event) Err() error {
	if e.Failure != nil {
		return e.Failure.restore(e.Error)
	}
	if e.Type == TurnCanceled {
		return recordedError{message: e.Error, cause: context.Canceled}
	}
	if e.Error == "" && e.Type != TurnFailed {
		return nil
	}
	return errors.New(e.Error)
}

func (f Failure) restore(message string) error {
	var cause error
	switch f.ContextError {
	case "canceled":
		cause = context.Canceled
	case "deadline_exceeded":
		cause = context.DeadlineExceeded
	}
	if f.Cause != nil {
		cause = f.Cause.restore(message)
	}
	if f.Kind != "" {
		return &model.Error{Kind: f.Kind, Message: message, Retryable: f.Retryable, StatusCode: f.StatusCode, Cause: cause}
	}
	return recordedError{message: message, cause: cause}
}

type recordedError struct {
	message string
	cause   error
}

func (e recordedError) Error() string { return e.message }
func (e recordedError) Unwrap() error { return e.cause }
