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
	if err != nil {
		return classifyErr(err)
	}
	return k.successOutcome(ctx, token, reply.Result, reply.TxID, reply.TraceID), nil
}

// CompleteStep completes a waiting step. The outcome classification rules are
// identical to Run: Rejected for input validation failures, Failed for
// execution errors, Success otherwise.
func (k *Caller) CompleteStep(ctx context.Context, token, id string, args json.RawMessage) (Outcome, error) {
	reply, err := k.c.CompleteStep(ctx, token, id, args)
	if err != nil {
		return classifyErr(err)
	}
	return k.successOutcome(ctx, token, reply.Result, reply.TxID, reply.TraceID), nil
}

// successOutcome builds a Success outcome and enriches it with transaction cost
// details when a TxID is available.
func (k *Caller) successOutcome(ctx context.Context, token string, result json.RawMessage, txID, traceID string) Outcome {
	out := Outcome{
		Class:   Success,
		Result:  result,
		TxID:    txID,
		TraceID: traceID,
		Message: "Success.",
	}
	if txID != "" {
		if tx, e := k.c.GetTransaction(ctx, token, txID); e == nil {
			out.Cost = &Cost{Gross: tx.Gross, Fee: tx.Fee, Net: tx.Net}
		}
	}
	return out
}

// classifyErr maps a kernel error to an Outcome or propagates non-outcome
// errors (auth, rate-limit, not-found, etc.) to the caller.
func classifyErr(err error) (Outcome, error) {
	ke, ok := client.AsKernelError(err)
	if !ok {
		return Outcome{}, err
	}
	switch ke.Kind {
	case client.KindInvalidInput:
		return Outcome{
			Class:   Rejected,
			Message: "Not sent — you were not charged. " + ke.UserMessage(),
			Detail:  ke.KernelMessage,
		}, nil
	case client.KindExecutionFailed, client.KindTimeout, client.KindInvalidState:
		return Outcome{
			Class:   Failed,
			Message: ke.UserMessage(),
			Detail:  ke.KernelMessage,
		}, nil
	default:
		return Outcome{}, err
	}
}
