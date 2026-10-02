package web

import "errors"

var (
	errInvalidRequest     = errors.New("invalid request")
	errConversationBusy   = errors.New("conversation busy")
	errServiceUnavailable = errors.New("service unavailable")
)

type conversationError struct {
	kind    error
	message string
}

func (e conversationError) Error() string { return e.message }
func (e conversationError) Unwrap() error { return e.kind }
