package server

import (
	"context"
	"encoding/json"

	"github.com/daios-ai/juice-ui/client"
)

// OutcomeClass is one of the three call result classes (R6.5.5).
type OutcomeClass int

const (
	// Rejected: input-schema rejection before any charge — no transaction.
	Rejected OutcomeClass = iota
	// Failed: execution, output-schema, timeout, or service-state failure —
	// fully refunded, with a failure transaction normally recorded.
	Failed
	// Success: result plus a gross/fee/net cost breakdown and a receipt link.
	Success
)

// Cost is the gross/fee/net breakdown of a successful call. The fee applies to
// value added (R6.5.5).
type Cost struct {
	Gross int64
	Fee   int64
	Net   int64
}

// Outcome is the rendered result of a call through the funded-session model.
type Outcome struct {
	Class       OutcomeClass
	Result      json.RawMessage // success result
	TxID        string          // success transaction
	TraceID     string
	Cost        *Cost  // success only
	FailureTxID string // failed only, best-effort
	Message     string // user-facing explanation
}

// IsSuccess reports a successful call outcome (template helper).
func (o Outcome) IsSuccess() bool { return o.Class == Success }

// IsFailed reports a failed-but-refunded call outcome (template helper).
func (o Outcome) IsFailed() bool { return o.Class == Failed }

// IsRejected reports an input-rejected, not-charged outcome (template helper).
func (o Outcome) IsRejected() bool { return o.Class == Rejected }

// Caller executes actions through the funded-session abstraction (R6.5).
type Caller struct {
	c *client.Client
}

// NewCaller returns a Caller backed by the kernel client.
func NewCaller(c *client.Client) *Caller { return &Caller{c: c} }

// CallEphemeral runs a price-0 action (e.g. @sys/lookup, @sys/llm/chat) in a
// zero-funded ephemeral process that is created, used, and ended (R6.5.1). The
// process is hidden from the run UI but still exists and surfaces as a closed
// process in history — callers must not imply no process occurred.
func (k *Caller) CallEphemeral(ctx context.Context, token, ref string, args json.RawMessage) (Outcome, error) {
	p, err := k.c.StartProcess(ctx, token, 0)
	if err != nil {
		return Outcome{}, err
	}
	pid := p.Ident()
	// Best-effort end; the process persists in history regardless.
	defer func() { _ = k.c.EndProcess(ctx, token, pid) }()
	return k.callIn(ctx, token, pid, ref, args)
}

// CallFunded runs an action drawing from an existing open funded session
// (R6.5.2). The caller owns the session lifecycle (open/fund/end).
func (k *Caller) CallFunded(ctx context.Context, token, processID, ref string, args json.RawMessage) (Outcome, error) {
	return k.callIn(ctx, token, processID, ref, args)
}

// callIn performs the call and classifies the result into one of the three
// outcome classes, returning other errors (auth, insufficient funds, not found,
// rate limited) to the caller for generic handling.
func (k *Caller) callIn(ctx context.Context, token, processID, ref string, args json.RawMessage) (Outcome, error) {
	reply, err := k.c.Call(ctx, token, client.CallRequest{
		ProcessID: processID,
		Action:    ref,
		Args:      args,
	})
	if err == nil {
		return k.success(ctx, token, reply), nil
	}

	ke, ok := client.AsKernelError(err)
	if !ok {
		return Outcome{}, err
	}
	switch ke.Kind {
	case client.KindInvalidInput:
		// Rejected before lock: not sent, not charged, no transaction.
		return Outcome{
			Class:   Rejected,
			Message: "Not sent — you were not charged. " + ke.UserMessage(),
		}, nil
	case client.KindExecutionFailed, client.KindTimeout, client.KindInvalidState:
		// Failure after execution began (or a non-operational service): the
		// kernel records a failure transaction and fully refunds.
		out := Outcome{
			Class:   Failed,
			Message: ke.UserMessage(),
		}
		out.FailureTxID = k.findFailureTx(ctx, token, processID)
		return out, nil
	default:
		// Auth, unauthorized, insufficient funds, not found, rate limited:
		// these are not call-outcome classes; let the handler map them.
		return Outcome{}, err
	}
}

// success builds a Success outcome, enriching it with the cost breakdown from
// the recorded transaction (R6.5.5). If the transaction can't be fetched, the
// result is still returned without the breakdown rather than failing.
func (k *Caller) success(ctx context.Context, token string, reply client.CallReply) Outcome {
	out := Outcome{
		Class:   Success,
		Result:  reply.Result,
		TxID:    reply.TxID,
		TraceID: reply.TraceID,
		Message: "Success.",
	}
	if reply.TxID != "" {
		if tx, err := k.c.GetTransaction(ctx, token, reply.TxID); err == nil {
			out.Cost = &Cost{Gross: tx.Gross, Fee: tx.Fee, Net: tx.Net}
		}
	}
	return out
}

// findFailureTx returns the id of the most recent failure transaction in the
// process, best-effort, so a Failed outcome can link to it (R6.5.5, R7.1).
func (k *Caller) findFailureTx(ctx context.Context, token, processID string) string {
	txs, err := k.c.ListTransactions(ctx, token, processID)
	if err != nil {
		return ""
	}
	id := ""
	for _, tx := range txs {
		if tx.Status == "failure" {
			id = tx.ID // last failure wins; lists are oldest-first in practice
		}
	}
	return id
}
