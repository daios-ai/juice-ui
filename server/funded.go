package server

import (
	"context"
	"encoding/json"
	"html/template"
	"time"

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
	// PeerUnfunded: the serving kernel's global exposure cap is exhausted — an
	// operator settlement condition, never the caller's own balance; not charged.
	PeerUnfunded
	// TermsChanged: the quote pinned on the run had moved, so the kernel refused
	// BEFORE locking funds — nothing ran and nothing was charged. It is a
	// re-confirmation, not a failure: the buyer is shown the current terms and
	// asked again (requirements.md §6.2).
	TermsChanged
	// Parked: the call was dispatched to a peer and has no signed outcome yet.
	// The funds are RESERVED, not refunded — the kernel keeps them locked and
	// retries until a receipt arrives or the refund becomes eligible. Rendering
	// this as a refund would state a movement that has not happened (§6.2).
	Parked
	// Refreshed: the provider changed the action's contract, so the peer refused
	// the dispatch and the kernel discarded the cached copy — nothing ran and
	// nothing was charged. A refusal, not a failure: the buyer is re-quoted and
	// one click re-runs (§6.2).
	Refreshed
	// PeerRefused: the serving kernel refused this call outright — it suspended
	// us, or will not serve that action. An operator relationship fact that no
	// retry fixes, and never the buyer's own permissions (§6.2).
	PeerRefused
)

// Cost is the gross/fee/net breakdown of a successful call.
type Cost struct {
	Gross int64
	Fee   int64
	Net   int64
	// Remote marks a call that crossed a kernel boundary, where the same three
	// numbers mean different things: Net is the obligation returned to the caller
	// and Fee the import fee retained here, not the provider's revenue (§6.5).
	// Ticket names the obligation both kernels know it by.
	Remote bool
	Ticket string
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
	// Price and QuoteHash carry the CURRENT terms of a TermsChanged or Refreshed
	// refusal, so the form re-renders and re-pins in one round trip rather than
	// re-reading.
	Price     int64
	QuoteHash string
	// Reserved, ProcessID and RefundEligibleAt describe a Parked outcome: the
	// amount still locked, the process holding it (so the wallet can be reached
	// from the result), and the date from which the automatic full refund
	// applies — an eligibility, never a settlement promise.
	Reserved         int64
	ProcessID        string
	RefundEligibleAt string
	// Peer names the kernel that refused a PeerRefused outcome, ready to print
	// (a public key arrives abbreviated). Display only: nothing keys on it.
	Peer string
}

// IsTermsChanged reports a refused quote (template helper).
func (o Outcome) IsTermsChanged() bool { return o.Class == TermsChanged }

// IsSuccess reports a successful call outcome (template helper).
func (o Outcome) IsSuccess() bool { return o.Class == Success }

// Reason is why a call did not succeed, in the kernel's own words: its raw
// message where there is one, else the sentence the UI derived from the class.
// One definition, because two call sites reading "Detail, else Message" are two
// chances to disagree about what a failure said.
func (o Outcome) Reason() string {
	if o.Detail != "" {
		return o.Detail
	}
	return o.Message
}

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

// IsPeerUnfunded reports a remote-proxy call refused because the serving
// kernel's global exposure cap is exhausted — an operator settlement matter,
// not the caller's balance, not charged (template helper).
func (o Outcome) IsPeerUnfunded() bool { return o.Class == PeerUnfunded }

// IsParked reports a dispatched call awaiting a signed outcome, with the funds
// still reserved — neither a failure nor a refund (template helper).
func (o Outcome) IsParked() bool { return o.Class == Parked }

// IsRefreshed reports a call refused because the provider's contract moved —
// nothing ran, nothing charged, re-quoted (template helper).
func (o Outcome) IsRefreshed() bool { return o.Class == Refreshed }

// IsPeerRefused reports a call the serving kernel refused outright (template
// helper).
func (o Outcome) IsPeerRefused() bool { return o.Class == PeerRefused }

// RefundEligibleDate renders the parked refund-eligibility instant as a plain
// date. The kernel sends RFC 3339; an unparseable value renders empty so the
// template simply omits the clause rather than printing a raw timestamp.
func (o Outcome) RefundEligibleDate() string {
	t, err := time.Parse(time.RFC3339, o.RefundEligibleAt)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2 January 2006")
}

// CallForm drives the shared _call_form partial for both running an action and
// completing a step — conceptually the same act (execute an action). Locked
// (a step's pre-bound args) renders through the result renderer; Editable is the
// remaining input; the result renders below the form with progress + disable.
type CallForm struct {
	CSRFField  template.HTML
	PostURL    string
	Verb       string          // "Run" | "Complete"
	Price      int64           // >0 → money button "Verb (N)"; 0 → plain "Verb"
	Locked     []LockedField   // a step's pre-bound args, shown read-only above the fields
	LockedRaw  json.RawMessage // fallback raw pre-bound args when the schema gave no split
	Editable   FormModel
	IsApproval bool   // no editable input — just acknowledge
	ResultID   string // id of the below-form result target
	Outcome    *Outcome
	OneShot    bool   // completion is spent once (steps); a run repeats (actions)
	FirstPayee bool   // paid run: the viewer has never paid this owner (anti-phishing hint)
	PayeeOwner string // owner named in the first-payment hint
	// IsPayment marks a payment-step completion — a spend of the COMPLETER's
	// own balance (the target action bears effect="transfer", §6.3). It flips
	// the button to money: "Complete ($N)" when Payment names the sealed
	// amount, bare "Complete" (money, wallet-send pattern) when the completer
	// types the amount. PayTo is the sealed recipient for the provenance line.
	// An ordinary step stays the neutral Complete naming no amount.
	IsPayment bool
	Payment   int64
	PayTo     string
	// QuoteHash pins the terms this form was RENDERED with (kernel §4). It rides
	// as a hidden field and the handler reads it back from the request — never
	// from a fresh read of the action, which would always match and silently
	// disable the protection while looking correct. If the terms moved between
	// render and submit, the kernel refuses before locking, at zero charge.
	QuoteHash string
}

// runForm builds a CallForm for running an action (repeatable, no locked args).
// firstPayee/payeeOwner drive the first-payment hint (§8.3) on a paid run.
func runForm(csrf template.HTML, actionID string, price int64, editable FormModel, outcome *Outcome, firstPayee bool, payeeOwner, quoteHash string) CallForm {
	return CallForm{
		CSRFField: csrf, PostURL: "/actions/" + actionID + "/run", Verb: "Run",
		QuoteHash: quoteHash,
		Price:     price, Editable: editable, ResultID: "run-result", Outcome: outcome,
		FirstPayee: firstPayee, PayeeOwner: payeeOwner,
	}
}

// completeForm builds a CallForm for completing a step (one-shot; the sealed args
// render read-only above the editable fields). ResultID is per-step so many
// inbox forms don't collide. locked is the structured split; lockedRaw is the
// raw fallback when the schema produced no structured fields.
func completeForm(csrf template.HTML, stepID string, price int64, editable FormModel, isApproval bool, locked []LockedField, lockedRaw json.RawMessage, outcome *Outcome, isPayment bool, payment int64, payTo string) CallForm {
	return CallForm{
		CSRFField: csrf, PostURL: "/inbox/" + stepID + "/complete", Verb: "Complete",
		Price: price, Locked: locked, LockedRaw: lockedRaw, Editable: editable, IsApproval: isApproval,
		ResultID: "step-result-" + stepID, Outcome: outcome, OneShot: true,
		IsPayment: isPayment, Payment: payment, PayTo: payTo,
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
// quoteHash, when non-empty, pins the terms the buyer was shown; the kernel
// refuses with terms_changed before locking anything if they have moved.
func (k *Caller) Run(ctx context.Context, token, ref string, args json.RawMessage, quoteHash string) (Outcome, error) {
	reply, err := k.c.Run(ctx, token, ref, args, quoteHash)
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
		// The headline states "not charged"; the message says what to fix. It
		// must not repeat the headline back at the reader.
		return Outcome{
			Class:   Rejected,
			Message: ke.UserMessage(),
			Detail:  ke.KernelMessage,
		}, nil
	case client.KindExecutionFailed:
		// A marked contract change never executed and was never charged, so it is
		// a refusal the buyer can accept again — not a failure with a refund.
		if ke.IsRefreshable() {
			return Outcome{
				Class:   Refreshed,
				Message: ke.UserMessage(),
			}, nil
		}
		// Otherwise the kernel answers a failure CLASS, not prose, so the
		// sentence is ours and the class is never shown: Detail would print
		// "execution_failed" to a buyer.
		return Outcome{
			Class:   Failed,
			Message: ke.UserMessage(),
		}, nil
	case client.KindTimeout:
		// Dispatched, unsettled: the funds are LOCKED, not returned. The metadata
		// is what makes the sentence honest — the process still holding them and
		// the date the automatic refund becomes eligible. The reserved AMOUNT is
		// not in the metadata; `run` parks exactly the action's price, so the
		// handler fills it from the quote the buyer was shown.
		return Outcome{
			Class:            Parked,
			Message:          ke.UserMessage(),
			ProcessID:        ke.MetaString("process_id"),
			RefundEligibleAt: ke.MetaString("refund_eligible_at"),
		}, nil
	case client.KindUnauthorized:
		// Only a peer's refusal is an outcome; an ordinary 403 stays an error for
		// the generic handler, which renders the not-permitted page.
		if p := ke.PeerDisplay(); p != "" {
			// The card's headline names the peer, so the supporting sentence must
			// not name it again — one fact, one place. UserMessage() keeps its
			// named form for the surfaces that render a sentence alone (flashes).
			return Outcome{
				Class:   PeerRefused,
				Peer:    p,
				Message: "The kernel serving this action refused the call. Nothing was charged, and retrying won't help.",
			}, nil
		}
		return Outcome{}, err
	case client.KindInvalidState:
		// A 409 is refused BEFORE execution: nothing ran and nothing was
		// charged, so it must not read as a failure with a refund. The kernel's
		// own message is human-readable here ("action is inactive") and is the
		// only class where echoing it informs rather than leaks a code.
		return Outcome{
			Class:   Rejected,
			Message: ke.UserMessage(),
		}, nil
	case client.KindTermsChanged:
		// The pinned quote moved: refused before locking, zero charge. This is a
		// re-confirmation, not a failure — the buyer is shown what it costs now.
		o := Outcome{Class: TermsChanged, Message: ke.UserMessage()}
		if p, ok := ke.MetaInt64("price"); ok {
			o.Price = p // the template names it in the kernel's own unit (§7)
		}
		o.QuoteHash = ke.MetaString("quote_hash")
		return o, nil
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
