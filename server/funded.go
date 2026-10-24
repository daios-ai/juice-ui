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
	// Failed: execution or output-schema failure — refunded except for sub-work
	// already delivered (Charged), with a failure transaction normally recorded.
	Failed
	// Success: result plus a gross/fee/net cost breakdown.
	Success
	// GrantRequired: the action needs a delegated OAuth grant — rejected before
	// any charge; the UI offers inline consent.
	GrantRequired
	// PeerUnreachable: a remote-proxy call whose first dispatch provably never
	// reached the peer — settled locally as a full refund (retry when it is back).
	PeerUnreachable
	// PeerUnfunded: the serving kernel will not serve this kernel on credit —
	// its limit is reached, or its provider cannot fund the work. An operator
	// condition, never the caller's own balance; not charged.
	PeerUnfunded
	// TermsChanged: the quote pinned on the run had moved, so the kernel refused
	// BEFORE locking funds — nothing ran and nothing was charged. It is a
	// re-confirmation, not a failure: the buyer is shown the current terms and
	// asked again (requirements.md §6.2).
	TermsChanged
	// Parked: the call was dispatched to a peer and has no signed outcome yet.
	// The funds are RESERVED, not refunded — the kernel keeps them locked and
	// retries until the peer's signed answer settles it; no age expires it.
	// Rendering this as a refund would state a movement that has not happened
	// (§6.2).
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
	// Reserved and ProcessID describe a Parked outcome: the amount still locked,
	// and the process holding it, so the wallet can be reached from the result.
	Reserved  int64
	ProcessID string
	// Charged is what a Failed call drew from the caller — the sub-work already
	// delivered, which stays paid (kernel §D2). Zero is a full refund. TxID names
	// the failure transaction the kernel recorded.
	Charged int64
	// Peer names the kernel that refused a PeerRefused outcome, ready to print
	// (a public key arrives abbreviated). Display only: nothing keys on it.
	Peer string
	// Room is the conversation the run was made from, when it was: a success
	// is then the composer's draft (requirements §6.7).
	Room string
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

// IsFailed reports a call that executed and failed (template helper).
func (o Outcome) IsFailed() bool { return o.Class == Failed }

// IsRejected reports an input-rejected, not-charged outcome (template helper).
func (o Outcome) IsRejected() bool { return o.Class == Rejected }

// IsGrantRequired reports an outcome needing a delegated OAuth grant before the
// action can run (template helper).
func (o Outcome) IsGrantRequired() bool { return o.Class == GrantRequired }

// IsPeerUnreachable reports a remote-proxy call the peer never received —
// fully refunded, retry when the peer is back (template helper).
func (o Outcome) IsPeerUnreachable() bool { return o.Class == PeerUnreachable }

// IsPeerUnfunded reports a remote-proxy call the serving kernel would not
// serve on credit — an operator matter, not the caller's balance, not charged
// (template helper).
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

// CallForm drives the shared _call_form partial for both running an action and
// completing a task — conceptually the same act (execute an action). Locked
// (a task's pre-bound args) renders as the view; Editable is the
// remaining input; the result renders below the form with progress + disable.
type CallForm struct {
	CSRFField  template.HTML
	PostURL    string
	FormURL    string          // the JSON switch's JSON → form conversion (§6.2)
	Verb       string          // "Run" | "Complete"
	Price      int64           // >0 → money button "Verb (N)"; 0 → plain "Verb"
	Locked     json.RawMessage // a task's pre-bound args, shown read-only above the fields
	Editable   FormModel
	IsApproval bool   // no editable input — just acknowledge
	ResultID   string // id of the below-form result target
	Outcome    *Outcome
	OneShot    bool   // completion is spent once (tasks); a run repeats (actions)
	FirstPayee bool   // paid run: the viewer has never paid this owner (anti-phishing hint)
	PayeeOwner string // owner named in the first-payment hint
	// QuoteHash pins the terms this form was RENDERED with (kernel §4). It rides
	// as a hidden field and the handler reads it back from the request — never
	// from a fresh read of the action, which would always match and silently
	// disable the protection while looking correct. If the terms moved between
	// render and submit, the kernel refuses before locking, at zero charge.
	QuoteHash string
	// DeclineID, set on a task the viewer may also decline, puts Decline in
	// the form's own button row, after Complete. The button submits the
	// separate decline form (_task_decline) through the HTML form attribute, so
	// the two commands share one row and keep their own actions.
	DeclineID string
	// Room is the conversation a panel was opened from, which the fill reads
	// and a success answers to. Fill, set when there is a room and the kernel
	// has a planner, offers to prepare the arguments from it (requirements
	// §6.2), at the planner's own price and pin.
	Room    string
	Fill    *plannerTerms
	FillURL string
}

// plannerTerms are the kernel's planner as a page offers it: its price and
// the quote that pins it (sys/llm/decide, kernel U46).
type plannerTerms struct {
	Price     int64
	QuoteHash string
}

// Structured reports a form with typed fields, which the JSON toggle swaps for
// the raw editor; an approval or a raw-only form has nothing to swap.
func (f CallForm) Structured() bool { return !f.IsApproval && len(f.Editable.Fields) > 0 }

// runForm builds the CallForm of an action's run (repeatable, no locked
// args) from its view: the pin it was quoted at, the first-payment hint
// (§8.3), and the context a panel was opened with.
func (r *Renderer) runForm(v actionView) CallForm {
	a := v.Action
	return CallForm{
		CSRFField: v.CSRFField, PostURL: "/actions/" + a.ID + "/run", FormURL: "/actions/" + a.ID + "/form", Verb: "Run",
		QuoteHash: a.QuoteHash, Price: a.Price, Editable: v.Form, ResultID: "run-result", Outcome: v.Outcome,
		FirstPayee: v.FirstPayee, PayeeOwner: r.refOf(a.Ref(), "").Owner.Text,
		Room: v.Room, Fill: v.Fill, FillURL: "/actions/" + a.ID + "/fill",
	}
}

// completeForm builds a CallForm for completing a task (one-shot; the sealed args
// render read-only above the editable fields). ResultID is per-task so many
// inbox forms don't collide. locked is the task's partial_args. canDecline puts
// Decline beside Complete.
func completeForm(csrf template.HTML, taskID string, price int64, editable FormModel, isApproval bool, locked json.RawMessage, outcome *Outcome, canDecline bool) CallForm {
	f := CallForm{
		CSRFField: csrf, PostURL: "/inbox/" + taskID + "/complete", FormURL: "/inbox/" + taskID + "/form", Verb: "Complete",
		Price: price, Locked: locked, Editable: editable, IsApproval: isApproval,
		ResultID: "task-result-" + taskID, Outcome: outcome, OneShot: true,
	}
	if canDecline {
		f.DeclineID = taskID
	}
	return f
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
	return k.successOutcome(ctx, token, reply.Result, reply.TxID, reply.ReceiptID, reply.TraceID), nil
}

// CompleteTask completes a waiting task. The outcome classification rules are
// identical to Run: Rejected for input validation failures, Failed for
// execution errors, Success otherwise.
func (k *Caller) CompleteTask(ctx context.Context, token, id string, args json.RawMessage) (Outcome, error) {
	reply, err := k.c.CompleteTask(ctx, token, id, args)
	if err != nil {
		return classifyErr(err)
	}
	return k.successOutcome(ctx, token, reply.Result, reply.TxID, reply.ReceiptID, reply.TraceID), nil
}

// successOutcome builds a Success outcome. The receipt is linked — and its cost
// read — only when the reply names one: a task held on another kernel completes
// there, and its reply carries that kernel's transaction and no receipt here
// (requirements.md §6.3), as does a run whose settlement is deferred.
func (k *Caller) successOutcome(ctx context.Context, token string, result json.RawMessage, txID, receiptID, traceID string) Outcome {
	out := Outcome{
		Class:   Success,
		Result:  result,
		TraceID: traceID,
		Message: "Success.",
	}
	if receiptID == "" {
		return out
	}
	out.TxID = txID
	if tx, e := k.c.GetTransaction(ctx, token, txID); e == nil {
		out.Cost = &Cost{Gross: tx.Gross, Fee: tx.Fee, Net: tx.Net}
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
		// "execution_failed" to a buyer. What the call drew and the transaction
		// it recorded ride in meta.
		charged, _ := ke.MetaInt64("charge")
		return Outcome{
			Class:   Failed,
			Message: ke.UserMessage(),
			TxID:    ke.MetaString("tx_id"),
			Charged: charged,
		}, nil
	case client.KindTimeout:
		// Dispatched, unsettled: the funds are LOCKED, not returned, until the
		// peer's signed answer settles the call. The metadata names the process
		// still holding them. The reserved AMOUNT is not in the metadata; `run`
		// parks exactly the action's price, so the handler fills it from the
		// quote the buyer was shown.
		return Outcome{
			Class:     Parked,
			Message:   ke.UserMessage(),
			ProcessID: ke.MetaString("process_id"),
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
