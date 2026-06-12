package server

import (
	"context"
	"encoding/json"

	"github.com/daios-ai/juice-ui/client"
)

// OutcomeClass is one of the three call result classes.
type OutcomeClass int

const (
	// Rejected: input-schema rejection before any charge — no transaction.
	Rejected OutcomeClass = iota
	// Failed: execution, output-schema, timeout, or service-state failure —
	// fully refunded, with a failure transaction normally recorded.
	Failed
	// Success: result plus a gross/fee/net cost breakdown.
	Success
)

// Cost is the gross/fee/net breakdown of a successful call.
type Cost struct {
	Gross int64
	Fee   int64
	Net   int64
}

// Outcome is the rendered result of a call through POST /v1/run.
type Outcome struct {
	Class   OutcomeClass
	Result  json.RawMessage // success result
	TxID    string          // success transaction
	TraceID string
	Cost    *Cost  // success only
	Message string // user-facing explanation
	Detail  string // raw kernel error message (for diagnostics; never includes secrets)
}

// IsSuccess reports a successful call outcome (template helper).
func (o Outcome) IsSuccess() bool { return o.Class == Success }

// IsFailed reports a failed-but-refunded call outcome (template helper).
func (o Outcome) IsFailed() bool { return o.Class == Failed }

// IsRejected reports an input-rejected, not-charged outcome (template helper).
func (o Outcome) IsRejected() bool { return o.Class == Rejected }

// Caller executes actions via POST /v1/run.
type Caller struct {
	c *client.Client
}

// NewCaller returns a Caller backed by the kernel client.
func NewCaller(c *client.Client) *Caller { return &Caller{c: c} }

// Run executes an action. The kernel atomically creates a process funded with
// exactly action.price, issues the call, and closes the process when done.
// The result is classified into one of the three outcome classes; auth,
// insufficient-funds, not-found, and rate-limit errors are returned as errors
// for the caller to handle generically.
func (k *Caller) Run(ctx context.Context, token, ref string, args json.RawMessage) (Outcome, error) {
	reply, err := k.c.Run(ctx, token, ref, args)
	if err == nil {
		out := Outcome{
			Class:   Success,
			Result:  reply.Result,
			TxID:    reply.TxID,
			TraceID: reply.TraceID,
			Message: "Success.",
		}
		if reply.TxID != "" {
			if tx, e := k.c.GetTransaction(ctx, token, reply.TxID); e == nil {
				out.Cost = &Cost{Gross: tx.Gross, Fee: tx.Fee, Net: tx.Net}
			}
		}
		return out, nil
	}

	ke, ok := client.AsKernelError(err)
	if !ok {
		return Outcome{}, err
	}
	switch ke.Kind {
	case client.KindInvalidInput:
		// 422: input-schema rejection (process never created, no charge).
		// Output-schema violations also surface here; financial outcome is
		// identical (user not charged either way).
		return Outcome{
			Class:   Rejected,
			Message: "Not sent — you were not charged. " + ke.UserMessage(),
			Detail:  ke.KernelMessage,
		}, nil
	case client.KindExecutionFailed, client.KindTimeout, client.KindInvalidState:
		// Execution began, failed, and was fully refunded.
		return Outcome{
			Class:   Failed,
			Message: ke.UserMessage(),
			Detail:  ke.KernelMessage,
		}, nil
	default:
		// Auth, unauthorized, insufficient funds, not found, rate limited:
		// not outcome classes; let the handler map them.
		return Outcome{}, err
	}
}
