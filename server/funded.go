package server

import (
	"context"
	"encoding/json"
	"html/template"

	"github.com/daios-ai/juice-ui/client"
)

// OutcomeClass is one of the call result classes.
type OutcomeClass int

const (
	// Rejected: input-schema rejection before any charge — no transaction.
	Rejected OutcomeClass = iota
	// Failed: execution, output-schema, timeout, or service-state failure —
	// fully refunded, with a failure transaction normally recorded.
	Failed
	// Success: result plus a gross/fee/net cost breakdown.
	Success
	// GrantRequired: the action needs a delegated OAuth grant — rejected before
	// any charge; the UI offers inline consent.
	GrantRequired
	// PeerUnreachable: a remote-proxy call whose first dispatch provably never
	// reached the peer — settled locally as a full refund (retry when it is back).
	PeerUnreachable
	// PeerUnfunded: this kernel's prepaid credit on the peer is exhausted — an
	// operator top-up condition, never the caller's own balance; not charged.
	PeerUnfunded
)

// Cost is the gross/fee/net breakdown of a successful call.
type Cost struct {
	Gross int64
	Fee   int64
	Net   int64
}

// Outcome is the rendered result of a call through POST /v1/run.
type Outcome struct {
	Class     OutcomeClass
	Result    json.RawMessage // success result
	TxID      string          // success transaction
	TraceID   string
	Cost      *Cost         // success only
	Message   string        // user-facing explanation
	Detail    string        // raw kernel error message (for diagnostics; never includes secrets)
	ActionRef string        // the action ref, for a GrantRequired outcome's Connect affordance
	CSRFField template.HTML // injected by the handler for the inline rating form
}

// IsSuccess reports a successful call outcome (template helper).
func (o Outcome) IsSuccess() bool { return o.Class == Success }

// IsFailed reports a failed-but-refunded call outcome (template helper).
func (o Outcome) IsFailed() bool { return o.Class == Failed }

// IsRejected reports an input-rejected, not-charged outcome (template helper).
func (o Outcome) IsRejected() bool { return o.Class == Rejected }

// IsGrantRequired reports an outcome needing a delegated OAuth grant before the
// action can run (template helper).
func (o Outcome) IsGrantRequired() bool { return o.Class == GrantRequired }

// IsPeerUnreachable reports a remote-proxy call the peer never received —
// fully refunded, retry when the peer is back (template helper).
func (o Outcome) IsPeerUnreachable() bool { return o.Class == PeerUnreachable }

// IsPeerUnfunded reports a remote-proxy call blocked because this kernel's
// prepaid credit on the peer is exhausted — not the caller's balance, not
// charged (template helper).
func (o Outcome) IsPeerUnfunded() bool { return o.Class == PeerUnfunded }

// CallForm drives the shared _call_form partial for both running an action and
// completing a step — conceptually the same act (execute an action). Locked
// (a step's pre-bound args) renders through the result renderer; Editable is the
// remaining input; the result renders below the form with progress + disable.
type CallForm struct {
	CSRFField  template.HTML
	PostURL    string
	Verb       string          // "Run" | "Complete"
	Price      int64           // >0 → money button "Verb (N)"; 0 → plain "Verb"
	Locked     json.RawMessage // pre-bound args (steps); nil for a plain run
	Editable   FormModel
	IsApproval bool   // no editable input — just acknowledge
	ResultID   string // id of the below-form result target
	Outcome    *Outcome
	OneShot    bool // completion is spent once (steps); a run repeats (actions)
}

// runForm builds a CallForm for running an action (repeatable, no locked args).
func runForm(csrf template.HTML, actionID string, price int64, editable FormModel, outcome *Outcome) CallForm {
	return CallForm{
		CSRFField: csrf, PostURL: "/actions/" + actionID + "/run", Verb: "Run",
		Price: price, Editable: editable, ResultID: "run-result", Outcome: outcome,
	}
}

// completeForm builds a CallForm for completing a step (one-shot; locked args on
// top). ResultID is per-step so many inbox forms don't collide.
func completeForm(csrf template.HTML, stepID string, price int64, editable FormModel, isApproval bool, locked json.RawMessage, outcome *Outcome) CallForm {
	return CallForm{
		CSRFField: csrf, PostURL: "/steps/" + stepID + "/complete", Verb: "Complete",
		Price: price, Locked: locked, Editable: editable, IsApproval: isApproval,
		ResultID: "step-result-" + stepID, Outcome: outcome, OneShot: true,
	}
}

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
		out, oerr := classifyErr(err)
		// The action being run is known here; use it if the error's metadata
		// didn't carry the ref for the Connect affordance.
		if oerr == nil && out.Class == GrantRequired && out.ActionRef == "" {
			out.ActionRef = ref
		}
		return out, oerr
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
	case client.KindGrantRequired:
		return Outcome{
			Class:     GrantRequired,
			ActionRef: ke.GrantAction,
			Message:   "This action needs a connected account before it can run.",
		}, nil
	case client.KindPeerUnreachable:
		return Outcome{
			Class:   PeerUnreachable,
			Message: ke.UserMessage(),
			Detail:  ke.KernelMessage,
		}, nil
	case client.KindPeerUnfunded:
		return Outcome{
			Class:   PeerUnfunded,
			Message: ke.UserMessage(),
			Detail:  ke.KernelMessage,
		}, nil
	default:
		return Outcome{}, err
	}
}
