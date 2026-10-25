package model

import (
	"context"
	"time"
)

// AttemptUsage measures one actual provider invocation. Charged is the shared
// token allowance debit, not currency. Unknown usage charges the reservation.
type AttemptUsage struct {
	Provider    string    `json:"provider,omitempty"`
	Number      int       `json:"attempt,omitempty"`
	Fallback    bool      `json:"fallback,omitempty"`
	RequestID   string    `json:"request_id,omitempty"`
	Model       string    `json:"model"`
	Operation   string    `json:"operation,omitempty"`
	Class       WorkClass `json:"class"`
	Reservation int64     `json:"reservation"`
	Charged     int64     `json:"charged"`
	Known       bool      `json:"usage_known"`
	Usage       Usage     `json:"usage"`
	QueueMS     int64     `json:"queue_ms"`
	DurationMS  int64     `json:"duration_ms"`
	ErrorKind   ErrorKind `json:"error_kind,omitempty"`
}
type usageRecorderKey struct{}

func WithUsageRecorder(ctx context.Context, record func(context.Context, AttemptUsage) error) context.Context {
	// Parent/children share one authoritative allowance/attempt ledger.
	if ctx.Value(usageRecorderKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, usageRecorderKey{}, record)
}

type admissionGovernorKey struct{}

// AttemptManagedClient routes each actual invocation through DispatchAttempt.
// Decorators around such clients must preserve this contract to avoid charging
// or holding a slot around the entire retry loop.
type AttemptManagedClient interface {
	Client
	ManagesModelAttempts() bool
}

func DispatchAttempt(ctx context.Context, client Client, request Request, sink EventSink) (response Response, err error) {
	err = dispatchUsage(ctx, request, func() (Usage, error) {
		var callErr error
		response, callErr = client.Generate(ctx, request, sink)
		return response.Usage, callErr
	})
	return response, err
}

// dispatchUsage owns one resource lifetime for both generation and embeddings:
// admission, reservation, actual call, settlement and durable attempt evidence.
func dispatchUsage(ctx context.Context, request Request, invoke func() (Usage, error)) error {
	if request.InputTokenEstimate < 0 || request.MaxOutputTokens < 0 {
		return &Error{Kind: ErrorInvalid, Message: "model token estimates and limits cannot be negative"}
	}
	governor, _ := ctx.Value(admissionGovernorKey{}).(*Governor)
	if governor == nil {
		governor = processGovernor
	}
	release, queued, err := governor.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	budget, _ := ctx.Value(budgetKey{}).(*Budget)
	reservation := int64(request.InputTokenEstimate) + int64(request.MaxOutputTokens)
	if err := budget.reserve(ctx, reservation); err != nil {
		return err
	}
	receipt := AttemptUsage{RequestID: request.Metadata["request_id"], Model: request.Model, Operation: request.Operation, Class: workloadFrom(ctx).Class, Reservation: reservation, QueueMS: queued.Milliseconds()}
	identity, _ := ctx.Value(attemptIdentityKey{}).(attemptIdentity)
	receipt.Provider, receipt.Number, receipt.Fallback = identity.provider, identity.number, identity.fallback
	started := time.Now()
	usage, err := invoke()
	receipt.DurationMS = time.Since(started).Milliseconds()
	receipt.Usage = usage
	receipt.Known = usage.Known()
	receipt.Charged = reservation
	if receipt.Known {
		receipt.Charged = UsageTokens(usage)
	}
	if err != nil {
		receipt.ErrorKind = ErrorKindOf(err)
	}
	if settleErr := budget.settle(ctx, reservation, usage, err); settleErr != nil {
		return settleErr
	}
	if record, ok := ctx.Value(usageRecorderKey{}).(func(context.Context, AttemptUsage) error); ok && record != nil {
		if recordErr := record(ctx, receipt); recordErr != nil {
			return recordErr
		}
	}
	return err
}

type attemptIdentityKey struct{}
type attemptIdentity struct {
	provider string
	number   int
	fallback bool
}
