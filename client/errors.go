package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Kind is the typed classification of a kernel error. It is derived from the
// HTTP status, then overridden by the machine `code` where the kernel sets one:
// the code is the more specific fact, and since v0.12.14 the kernel classifies
// rather than narrating (a remote failure now answers `execution_failed`, not
// the serving kernel's own error prose), so the code is what the UI keys on and
// the message is for logs and field detail.
type Kind int

const (
	KindUnknown           Kind = iota
	KindUnauthenticated        // 401 (missing/invalid/expired token, suspended user)
	KindUnauthorized           // 403
	KindNotFound               // 404
	KindInvalidInput           // 422 (input validation incl. schema violations)
	KindInvalidState           // 409 (process closed, service not configured, ...)
	KindInsufficientFunds      // 402
	KindExecutionFailed        // 500 (action failed after execution began)
	KindTimeout                // 504
	KindRateLimited            // 429 (per-IP auth/user-create limiting, R5.6)
	KindInternal               // 500 (kernel bug / unrecoverable)
	KindGrantRequired          // 403 code="grant_required": needs a delegated OAuth grant
	KindPeerUnreachable        // 502 code="peer_unreachable": remote peer offline; call refunded
	KindPeerUnfunded           // 402 code="peer_unfunded": the serving kernel will not serve this one on credit (kernel §D14)
	KindTermsChanged           // 409 code="terms_changed": the pinned quote moved; refused BEFORE locking, zero charge (v0.12.16)
	KindRailStopped            // 503 code="rail_stopped": a blocked payment halts outgoing rail work
	KindUnavailable            // transport failure: the kernel gave no HTTP response (down/restarting)
)

// KernelError is the typed error returned by every client call on a non-2xx
// response. It never carries kernel tokens or secrets. The raw HTTP status is
// recorded for logs but is never shown to the user (R7.1, R7.2).
type KernelError struct {
	Kind          Kind
	Status        int
	KernelMessage string // raw {"error":...} text; safe to log, used as form detail
	Field         string // best-effort offending field for 422 validation errors
	Code          string // machine code when the kernel sets one (e.g. "grant_required")
	GrantAction   string // the action ref carried in a grant_required error's meta
	// Meta is the error's structured metadata, kept raw per key so a value's
	// wire type is not guessed. The kernel answers strings today (verified live:
	// terms_changed carries {"price":"500","quote_hash":"…"}), but a numeric
	// price later would silently fail a map[string]string. Read it through
	// MetaString/MetaInt64.
	Meta map[string]json.RawMessage
}

// upstreamStatus is the one fact the kernel relays about an HTTP action's
// refusal: kernel D4 keeps the upstream's body out of every reason, so its
// status is all a caller can act on. The contract test holds this wording
// against the installed kernel.
var upstreamStatus = regexp.MustCompile(`^upstream returned status (\d{3})$`)

// Unanswered reports another kernel that did not answer: what this kernel
// recorded stands, and only its part on that kernel is undone (a peer that
// cannot be reached, or one that did not answer in time).
func (e *KernelError) Unanswered() bool {
	return e.Kind == KindPeerUnreachable || e.Kind == KindTimeout
}

// UpstreamDown reports an HTTP action whose endpoint could not be reached.
func (e *KernelError) UpstreamDown() bool {
	return e.Kind == KindExecutionFailed && e.KernelMessage == "upstream request failed"
}

// RemoteFailed reports an action on another kernel that ran and failed. The
// serving kernel relays no reason and no status across kernels, so a refusal
// and a failure of that kernel's endpoint read the same.
func (e *KernelError) RemoteFailed() bool {
	return e.Kind == KindExecutionFailed && e.KernelMessage == "remote call failed"
}

// UpstreamStatus is the status an HTTP action's endpoint answered, for an
// execution_failed the kernel reports as such, and 0 for anything else.
func (e *KernelError) UpstreamStatus() int {
	if e.Kind != KindExecutionFailed {
		return 0
	}
	m := upstreamStatus.FindStringSubmatch(e.KernelMessage)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// MetaString reads a metadata value as a string, tolerating both a JSON string
// and a bare number.
func (e *KernelError) MetaString(key string) string {
	raw, ok := e.Meta[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// MetaInt64 reads a metadata value as an integer, tolerating either encoding.
// Returns 0 and false when absent or not a number.
func (e *KernelError) MetaInt64(key string) (int64, bool) {
	s := e.MetaString(key)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// IsRefreshable reports the kernel's own marker that the call never executed
// because the provider's contract had moved: the cached proxy was discarded, the
// caller was charged nothing, and the next call re-resolves. It is the ONLY
// signal that distinguishes this from a call that genuinely executed and failed,
// so a retry may be offered here and nowhere else in the class
// (requirements.md §6.2).
func (e *KernelError) IsRefreshable() bool {
	return e.Kind == KindExecutionFailed && e.MetaString("retry") == "refresh"
}

// Peer names the remote kernel a federation error is about, exactly as the
// kernel sent it. Every failure to reach a peer carries it as of kernel
// v0.12.24 — a petname where the failing path knows one, the public key
// otherwise (both name a kernel, kernel §D15). PeerDisplay renders it.
func (e *KernelError) Peer() string { return e.MetaString("peer") }

// PeerDisplay is the peer name as prose. Which name the kernel had to hand
// varies by failing path, so this abbreviates a KEY and leaves a petname whole:
// a petname is already what a person recognises the kernel by, however long it
// is, while 43 characters of base64url in a sentence are unreadable.
func (e *KernelError) PeerDisplay() string { return ShortKey(e.Peer()) }

func (e *KernelError) Error() string {
	return fmt.Sprintf("kernel error kind=%s status=%d msg=%q", e.Kind, e.Status, e.KernelMessage)
}

// Is matches sentinel errors by Kind so handlers can write
// errors.Is(err, ErrNotFound) regardless of the status/message detail.
func (e *KernelError) Is(target error) bool {
	t, ok := target.(*KernelError)
	if !ok {
		return false
	}
	return t.Kind == e.Kind
}

// Sentinel errors for errors.Is matching.
var (
	ErrUnauthenticated   = &KernelError{Kind: KindUnauthenticated}
	ErrUnauthorized      = &KernelError{Kind: KindUnauthorized}
	ErrNotFound          = &KernelError{Kind: KindNotFound}
	ErrInvalidInput      = &KernelError{Kind: KindInvalidInput}
	ErrInvalidState      = &KernelError{Kind: KindInvalidState}
	ErrInsufficientFunds = &KernelError{Kind: KindInsufficientFunds}
	ErrExecutionFailed   = &KernelError{Kind: KindExecutionFailed}
	ErrTimeout           = &KernelError{Kind: KindTimeout}
	ErrRateLimited       = &KernelError{Kind: KindRateLimited}
	ErrInternal          = &KernelError{Kind: KindInternal}
	ErrGrantRequired     = &KernelError{Kind: KindGrantRequired}
	ErrPeerUnreachable   = &KernelError{Kind: KindPeerUnreachable}
	ErrPeerUnfunded      = &KernelError{Kind: KindPeerUnfunded}
	ErrRailStopped       = &KernelError{Kind: KindRailStopped}
	ErrUnavailable       = &KernelError{Kind: KindUnavailable}
)

func (k Kind) String() string {
	switch k {
	case KindUnauthenticated:
		return "unauthenticated"
	case KindUnauthorized:
		return "unauthorized"
	case KindNotFound:
		return "not_found"
	case KindInvalidInput:
		return "invalid_input"
	case KindInvalidState:
		return "invalid_state"
	case KindInsufficientFunds:
		return "insufficient_funds"
	case KindExecutionFailed:
		return "execution_failed"
	case KindTimeout:
		return "timeout"
	case KindRateLimited:
		return "rate_limited"
	case KindInternal:
		return "internal"
	case KindGrantRequired:
		return "grant_required"
	case KindPeerUnreachable:
		return "peer_unreachable"
	case KindPeerUnfunded:
		return "peer_unfunded"
	case KindRailStopped:
		return "rail_stopped"
	case KindUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// fieldRe extracts the offending field from kernel validation messages such as
// "field #.query: required field missing" (verified live).
var fieldRe = regexp.MustCompile(`field #\.([^:\s]+)`)

// classify maps an HTTP status and raw error body to a *KernelError. It returns
// nil for 2xx responses. Body is the raw response payload; the kernel encodes
// errors as {"error":"<message>"}.
func classify(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	var wire struct {
		Error string                     `json:"error"`
		Code  string                     `json:"code"`
		Meta  map[string]json.RawMessage `json:"meta"`
	}
	_ = json.Unmarshal(body, &wire)
	msg := strings.TrimSpace(wire.Error)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}

	e := &KernelError{Status: status, KernelMessage: msg, Code: wire.Code, Meta: wire.Meta}
	switch status {
	case http.StatusUnauthorized:
		e.Kind = KindUnauthenticated
	case http.StatusPaymentRequired:
		e.Kind = KindInsufficientFunds
	case http.StatusForbidden:
		e.Kind = KindUnauthorized
		if wire.Code == "grant_required" {
			e.Kind = KindGrantRequired
			e.GrantAction = e.MetaString("action")
		}
	case http.StatusNotFound:
		e.Kind = KindNotFound
	case http.StatusConflict:
		e.Kind = KindInvalidState
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		e.Kind = KindInvalidInput
		if m := fieldRe.FindStringSubmatch(msg); m != nil {
			e.Field = m[1]
		}
	case http.StatusTooManyRequests:
		e.Kind = KindRateLimited
	case http.StatusGatewayTimeout:
		e.Kind = KindTimeout
	case http.StatusInternalServerError:
		e.Kind = KindExecutionFailed
	default:
		e.Kind = KindInternal
	}
	// A remote-proxy call carries a machine code that overrides the status-derived
	// kind, so the outcome names the real cause (an unreachable peer, or the
	// serving kernel's exhausted exposure cap — not the caller's own balance).
	switch wire.Code {
	case "peer_unreachable":
		e.Kind = KindPeerUnreachable
	case "peer_unfunded":
		e.Kind = KindPeerUnfunded
	case "terms_changed":
		e.Kind = KindTermsChanged
	case "rail_stopped":
		// A blocked payment halts outgoing rail work; deposits, paid work and
		// reads continue (kernel §D23). A plain 503 stays internal.
		e.Kind = KindRailStopped
	}
	return e
}

// UserMessage returns an actionable, user-facing message. It never includes the
// raw HTTP status (R7.1). For input and state errors the kernel's own message
// is surfaced because it is human-readable and contains no secrets (e.g. "name
// is required", "lookup requires an embedding service"); other classes get a
// fixed actionable message and the kernel detail is confined to logs (R7.2).
func (e *KernelError) UserMessage() string {
	switch e.Kind {
	case KindUnauthenticated:
		return "Your session has expired. Please sign in again."
	case KindUnauthorized:
		// A peer's refusal arrives in this class but is an operator relationship
		// fact — the peer suspended us, or will not serve that action — and must
		// never read as the user's own lack of permission (v0.12.23).
		if p := e.PeerDisplay(); p != "" {
			return "Kernel " + p + " declined this call. Nothing was charged, and retrying won't help."
		}
		return "You don't have permission to do that."
	case KindNotFound:
		return "That item could not be found."
	case KindInvalidInput:
		if e.KernelMessage != "" {
			return "Please check your input: " + e.KernelMessage
		}
		return "Please check your input and try again."
	case KindInvalidState:
		if e.KernelMessage != "" {
			return e.KernelMessage
		}
		return "This action isn't available right now."
	case KindInsufficientFunds:
		return "Not enough available balance for this."
	case KindExecutionFailed:
		// A provider that changed the action's contract answers the same class
		// but marks it: nothing executed and nothing was charged, so it must not
		// read as a failure with a refund (verified live on v0.12.23).
		if e.IsRefreshable() {
			return "The provider changed this action — you were not charged."
		}
		// Otherwise the kernel answers a CLASS, not prose (v0.12.14: a remote
		// failure reads "remote call failed", and the stored reason is the bare
		// "execution_failed"), so the sentence is ours to write. Never echo it.
		// Sub-work already delivered stays paid, and the kernel says how much
		// the call drew in meta.charge; only a zero draw is a full refund.
		if c, _ := e.MetaInt64("charge"); c > 0 {
			return "The action failed to run. Work it had already delivered was charged; the rest was refunded."
		}
		return "The action failed to run. You were refunded in full."
	case KindTimeout:
		// NOT a refund: the call was dispatched, has no signed outcome yet, and
		// the kernel keeps the funds locked while it retries until the peer's
		// signed answer arrives; no age expires it. The server words the
		// reserved amount, which it can format as money.
		return "Result pending — your funds stay reserved until this settles."
	case KindRateLimited:
		return "Too many attempts. Please wait a moment and try again."
	case KindGrantRequired:
		return "This action needs a connected account. Connect it and try again."
	case KindTermsChanged:
		// Refused before anything was locked, so nothing moved. The new price
		// rides in Meta and is worded by the server, which owns money formatting.
		return "The terms changed while this page was open. Nothing was charged."
	case KindPeerUnreachable:
		if p := e.PeerDisplay(); p != "" {
			return "This action runs on " + p + ", which is currently unreachable. Your funds were not spent — try again when it's back."
		}
		return "This action runs on another kernel that's currently unreachable. Your funds were not spent — try again when it's back."
	case KindPeerUnfunded:
		// The serving kernel will not serve this kernel on credit: its limit is
		// reached, or its own provider cannot fund the work (kernel §D14). An
		// operator condition, never the buyer's own balance.
		if p := e.PeerDisplay(); p != "" {
			return "Kernel " + p + " cannot serve this call on credit right now. That is between the kernels' operators, not your balance."
		}
		return "The kernel serving this action cannot serve this call on credit right now. That is between the kernels' operators, not your balance."
	case KindRailStopped:
		return "Payments out are paused while a rail payment is blocked. Deposits and calls continue."
	case KindUnavailable:
		return "The Juice kernel is unreachable — it may be down or restarting. Please try again shortly."
	default:
		return "Something went wrong. Please try again."
	}
}

// AsKernelError extracts a *KernelError from err, if present.
func AsKernelError(err error) (*KernelError, bool) {
	ke, ok := err.(*KernelError)
	return ke, ok
}
