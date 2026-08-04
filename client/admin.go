package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Admin (superuser) operations. The kernel serves these on the same TCP API as
// everything else, under /control/*, gated by an IsSuperuser check on the sys
// bearer token (API.md C13). A non-superuser bearer is rejected 403
// ("superuser required") → KindUnauthorized. Routes and shapes verified live
// (2026-07-06; re-gated against the v0.12 kernel 2026-08-03). These never touch
// the kernel DB directly; the BFF is an ordinary HTTP client of the control
// surface.

// AdminUser is a user row from the control surface. The kernel signals suspension
// by the PRESENCE of suspended_at: it omits the field when active and includes the
// timestamp when suspended. Email was retired in v0.9 in favour of Description.
type AdminUser struct {
	ID          string `json:"id"`
	Handle      string `json:"handle"`
	Description string `json:"description"`
	Available   int64  `json:"available"`
	Locked      int64  `json:"locked"`
	SuspendedAt string `json:"suspended_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// Suspended reports whether the user is currently suspended. Detection is by
// presence: the kernel omits suspended_at when active and sends the suspension
// timestamp when suspended, so a non-empty string means suspended — robust also to
// an older kernel that sent the RFC3339 zero value.
func (u AdminUser) Suspended() bool { return u.SuspendedAt != "" }

// Adjustment is the audit record returned by deposit/withdraw.
type Adjustment struct {
	Direction      string `json:"direction"` // "credit" | "debit"
	Amount         int64  `json:"amount"`
	Reason         string `json:"reason"`
	ExternalKey    string `json:"external_key,omitempty"`
	CreatedAt      string `json:"created_at"`
	OperatorHandle string `json:"operator_handle"`
	TargetHandle   string `json:"target_handle"`
}

// Peer is a known peer (proxy user) row from the control surface. Available is
// the BILATERAL POSITION under the v0.12 exposure model and may be NEGATIVE:
// positive = the peer prepaid (this kernel owes it service), negative = the
// peer owes this kernel. PeerCredit and LastSeen are the §13 peer-sync cache —
// this kernel's cached credit on the peer, and when we last heard from it.
// SettlementDue is the kernel's Y-trigger signal: information, never authority
// (crossing it stops nothing; only the global cap X refuses calls). SuspendedAt
// is present only on a suspended row under ?all=1 (v0.9 unified moderation).
type Peer struct {
	Handle        string `json:"handle"`
	PublicKey     string `json:"public_key"`
	Available     int64  `json:"available"`
	Locked        int64  `json:"locked"`
	PeerCredit    int64  `json:"peer_credit"`              // our cached credit on the peer
	LastSeen      string `json:"last_seen"`                // RFC3339; "" / null when never seen
	SettlementDue bool   `json:"settlement_due,omitempty"` // gross receivables crossed Y (§13)
	SuspendedAt   string `json:"suspended_at,omitempty"`   // set only on a suspended row under ?all=1
}

// Position is the bilateral position for display: the signed Available.
// Render it with its reading — "we owe them" (positive: the peer prepaid or
// earned here) / "owes us" (negative) — never as a bare negative number
// (requirements.md §6.6).
func (p Peer) Position() int64 { return p.Available }

// Suspended reports whether this peer is frozen (its inbound calls are refused).
// Detection is by presence: the kernel returns suspended_at only on a suspended row
// (verified live 2026-07-19), so a non-empty string means suspended.
func (p Peer) Suspended() bool { return p.SuspendedAt != "" }

// Peers is the /control/peers response. There is no known-network roster: the
// kernel serves no directory of discovered kernels (verified live 2026-08-04 —
// the roster, its introducer attribution, and the --gossip flag were removed
// with subscription). Discovered kernels surface only through sys/lookup.
type Peers struct {
	Peers []Peer `json:"peers"`
}

// PeerAction is one action a peer serves, as carried by an inspection: the
// signed manifest fields plus the peer's own earned Stats for it. Descriptions
// and schemas are the canonical interface an importing kernel would cache.
type PeerAction struct {
	ActionID    string `json:"action_id"`
	OwnerHandle string `json:"owner_handle"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       int64  `json:"price"`
	Kind        string `json:"kind"`
	Stats       Stats  `json:"stats"`
}

// Ref is the action's reference as this kernel would address it: owner/name in
// the serving kernel's own namespace. It is display-only — reaching the action
// requires resolving it from that kernel (§13), never this string alone.
func (a PeerAction) Ref() string { return a.OwnerHandle + "/" + a.Name }

// Evidence is one derived metric row from the local evidence cache: what a
// single issuer's signed receipts say about one action of the inspected kernel
// (juicedocs §13). RatingCount/RatingMean count only TRADE-BACKED ratings — the
// two-kernel receipt link holds — while UnverifiedRatings counts those retained
// without that link. Equivocating ratings are excluded by the kernel. This
// proves attribution, never honesty, so it is displayed as evidence beside its
// issuer and never folded into an action's first-party Stats.
type Evidence struct {
	IssuerPublicKey  string  `json:"issuer_public_key"`
	SubjectActionID  string  `json:"subject_action_id"`
	Uses             int64   `json:"uses"`
	Successes        int64   `json:"successes"`
	Failures         int64   `json:"failures"`
	AvgLatencyMillis int64   `json:"avg_latency_ms"`
	RatingCount      int64   `json:"rating_count"`
	RatingMean       float64 `json:"rating_mean"`
	UnverifiedRating int64   `json:"unverified_ratings"`
}

// Reachability holds the transport diagnostics only. Online/Source are NOT here —
// the kernel returns them at the top level of the inspection (see Inspection).
type Reachability struct {
	Path      string   `json:"path"` // "direct" | "relayed" | "unreachable" | …
	RTTMillis int64    `json:"rtt_millis"`
	Protocols []string `json:"protocols"`
	Error     string   `json:"error,omitempty"` // why an unreachable peer could not be dialled
}

// Inspection is GET /control/peers/inspect?key= — a remote kernel's identity, its
// About (the peer's sys description), the public actions it serves, and the
// evidence this kernel retains about it, plus reachability. Online and Source are
// top-level fields (verified live): Online is true only when the live probe reached
// the peer; Source is "live" | "local" (last-known, online=false) | "none". The
// transacted-peers list ("friends") is GONE as of v0.12 — endorsement by
// association is replaced by signed, per-issuer Evidence (verified live
// 2026-08-04). The wire also carries first-party `users` and the `steps` this peer
// holds for us; neither has a UI surface yet (ROADMAP).
type Inspection struct {
	Handle       string       `json:"handle"`
	PublicKey    string       `json:"public_key"`
	About        string       `json:"about"`
	Actions      []PeerAction `json:"actions"`
	Evidence     []Evidence   `json:"evidence"`
	Online       bool         `json:"online"`
	Source       string       `json:"source"` // "live" | "local" (last-known) | "none"
	Reachability Reachability `json:"reachability"`
}

// EvidenceByIssuer groups the inspection's evidence by issuing kernel, in wire
// order, and resolves each row's subject action to a display name from the
// inspection's own manifest list (evidence carries only the action id). A
// subject absent from that list — retained evidence about an action the kernel
// no longer serves — keeps its raw id, which is still a truthful label.
func (i Inspection) EvidenceByIssuer() []IssuerEvidence {
	names := make(map[string]string, len(i.Actions))
	for _, a := range i.Actions {
		names[a.ActionID] = a.Ref()
	}
	var out []IssuerEvidence
	at := map[string]int{}
	for _, e := range i.Evidence {
		idx, seen := at[e.IssuerPublicKey]
		if !seen {
			out = append(out, IssuerEvidence{Issuer: e.IssuerPublicKey})
			idx = len(out) - 1
			at[e.IssuerPublicKey] = idx
		}
		subject := names[e.SubjectActionID]
		if subject == "" {
			subject = e.SubjectActionID
		}
		out[idx].Rows = append(out[idx].Rows, EvidenceRow{Subject: subject, Evidence: e})
	}
	return out
}

// IssuerEvidence is one issuer's block of evidence rows about the inspected
// kernel; EvidenceRow pairs a row with its resolved subject-action label.
type IssuerEvidence struct {
	Issuer string
	Rows   []EvidenceRow
}

// EvidenceRow is one evidence record with its subject named for display.
type EvidenceRow struct {
	Subject string
	Evidence
}

// Identity is this kernel's own federation identity plus its global exposure
// evidence (/control/identity; exposure fields verified live 2026-08-03):
// ExposureMax is the cap X on gross unsecured receivables across ALL peers,
// GrossReceivables the current G, SettlementTrigger the flag level Y, and
// SettlementQuantum the smallest fee-rational external payment Q (0 = the
// probabilistic residual path is disabled). SettlementDue mirrors G ≥ Y.
type Identity struct {
	Handle            string   `json:"handle"`
	PublicKey         string   `json:"public_key"`
	Addrs             []string `json:"addrs"`
	ExposureMax       int64    `json:"exposure_max"`
	GrossReceivables  int64    `json:"gross_receivables"`
	SettlementTrigger int64    `json:"settlement_trigger"`
	SettlementQuantum int64    `json:"settlement_quantum"`
	SettlementDue     bool     `json:"settlement_due"`
}

// AdminListUsers returns every user (GET /control/users). The kernel pages every
// list endpoint (default 50, ceiling 200, §R6), so we page through with offset —
// a single unpaged call would silently truncate at 50, and the admin roster must
// be sorted and paginated over the full set.
func (c *Client) AdminListUsers(ctx context.Context, token string) ([]AdminUser, error) {
	const page = 200 // server ceiling
	var out []AdminUser
	for offset := 0; ; offset += page {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(page))
		if offset > 0 {
			q.Set("offset", strconv.Itoa(offset))
		}
		var batch []AdminUser
		if err := c.do(ctx, http.MethodGet, "/control/users?"+q.Encode(), token, nil, &batch); err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
}

// AdminGetUser returns one user by handle (GET /control/users/{handle}).
func (c *Client) AdminGetUser(ctx context.Context, token, handle string) (AdminUser, error) {
	var u AdminUser
	err := c.do(ctx, http.MethodGet, "/control/users/"+url.PathEscape(normalizeHandle(handle)), token, nil, &u)
	return u, err
}

// AdminSuspendUser suspends a user (POST /control/users/{handle}/suspend).
func (c *Client) AdminSuspendUser(ctx context.Context, token, handle string) error {
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(normalizeHandle(handle))+"/suspend", token, nil, nil)
}

// AdminUnsuspendUser lifts a suspension (POST /control/users/{handle}/unsuspend).
func (c *Client) AdminUnsuspendUser(ctx context.Context, token, handle string) error {
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(normalizeHandle(handle))+"/unsuspend", token, nil, nil)
}

// AdminRename changes a user's handle (POST /control/users/{handle}/rename),
// freeing the old handle for reuse. The kernel rejects renaming sys.
func (c *Client) AdminRename(ctx context.Context, token, handle, newHandle string) error {
	body := struct {
		NewHandle string `json:"new_handle"`
	}{normalizeHandle(newHandle)}
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(normalizeHandle(handle))+"/rename", token, body, nil)
}

// AdminDeposit credits a user's balance (POST /control/deposit). The handle may
// be a peer's base64url public key: on a kernel that does not yet know that key,
// the deposit provisions the peer's billing account and funds it in one gesture
// (verified live 2026-08-04) — this is how a peer is brought into being now that
// there is no subscription.
func (c *Client) AdminDeposit(ctx context.Context, token, handle string, amount int64, reason, externalKey string) (Adjustment, error) {
	return c.adjust(ctx, token, "/control/deposit", handle, amount, reason, externalKey)
}

// AdminWithdraw debits a user's balance (POST /control/withdraw).
func (c *Client) AdminWithdraw(ctx context.Context, token, handle string, amount int64, reason, externalKey string) (Adjustment, error) {
	return c.adjust(ctx, token, "/control/withdraw", handle, amount, reason, externalKey)
}

func (c *Client) adjust(ctx context.Context, token, path, handle string, amount int64, reason, externalKey string) (Adjustment, error) {
	body := map[string]any{"handle": normalizeHandle(handle), "amount": amount}
	if reason != "" {
		body["reason"] = reason
	}
	if externalKey != "" {
		body["external_key"] = externalKey
	}
	var a Adjustment
	err := c.do(ctx, http.MethodPost, path, token, body, &a)
	return a, err
}

// AdminListPeers returns known peers, optionally including suspended ones —
// GET /control/peers[?all=1]. The kernel requires the literal value "1" ("true"
// is read as false), so the flag is emitted as "1" or omitted.
func (c *Client) AdminListPeers(ctx context.Context, token string, all bool) (Peers, error) {
	path := "/control/peers"
	if all {
		path += "?all=1"
	}
	var p Peers
	err := c.do(ctx, http.MethodGet, path, token, nil, &p)
	return p, err
}

// AdminIdentity returns this kernel's federation identity (GET /control/identity).
func (c *Client) AdminIdentity(ctx context.Context, token string) (Identity, error) {
	var id Identity
	err := c.do(ctx, http.MethodGet, "/control/identity", token, nil, &id)
	return id, err
}

// There is no subscribe/unsubscribe: consuming a peer's actions needs no
// relationship, and a proxy row is filled by resolve-on-use alone (juicedocs
// §13). A peer account is provisioned by its first signed call or by
// AdminDeposit addressed to its public key; the durable lever is suspend, and
// an uncalled peer lapses through the kernel's retention.

// AdminSuspendPeer freezes a peer's inbound calls (POST /control/users/{key}/suspend).
// The moderation route is unified with users (v0.9): the three account forms are
// disjoint syntactic productions (bare handle / base64url key / UUID), so the
// key is passed through untouched and the kernel disambiguates by shape.
func (c *Client) AdminSuspendPeer(ctx context.Context, token, key string) error {
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(key)+"/suspend", token, nil, nil)
}

// AdminUnsuspendPeer lifts a peer suspension (POST /control/users/{key}/unsuspend).
func (c *Client) AdminUnsuspendPeer(ctx context.Context, token, key string) error {
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(key)+"/unsuspend", token, nil, nil)
}

// AdminInspect inspects a remote kernel by key (GET /control/peers/inspect?key=).
func (c *Client) AdminInspect(ctx context.Context, token, key string) (Inspection, error) {
	var out Inspection
	err := c.do(ctx, http.MethodGet, "/control/peers/inspect?key="+url.QueryEscape(key), token, nil, &out)
	return out, err
}

// PendingTransfer is one buyer-side value-transfer record from the v0.12
// transfer queue (GET /control/transfers). Status is "pending" (no settleable
// receipt yet), "quarantined" (a receipt that failed validation — evidence
// insufficient, reconciled out of band), or terminal "settled"/"refunded".
// Field set kept lenient: shapes gated against the running kernel in the
// admin handler tests; unknown fields are ignored by encoding/json.
type PendingTransfer struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Peer      string `json:"peer,omitempty"` // the serving peer (handle or key)
	StepID    string `json:"step_id,omitempty"`
	Amount    int64  `json:"amount,omitempty"`  // the delivered value
	Reserve   int64  `json:"reserve,omitempty"` // total locked from the buyer
	Reason    string `json:"reason,omitempty"`  // why quarantined, when it is
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// AdminListTransfers lists value-transfer records (GET /control/transfers).
// An empty status lists the kernel's default: the unresolved set (pending +
// quarantined); a specific status ("pending", "quarantined", "settled",
// "refunded") filters to it. Paged like every kernel list.
func (c *Client) AdminListTransfers(ctx context.Context, token, status string, limit, offset int) ([]PendingTransfer, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	path := "/control/transfers"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []PendingTransfer
	err := c.do(ctx, http.MethodGet, path, token, nil, &out)
	return out, err
}

// AdminGetTransfer reads one value-transfer record (GET /control/transfers/{id}).
func (c *Client) AdminGetTransfer(ctx context.Context, token, id string) (PendingTransfer, error) {
	var t PendingTransfer
	err := c.do(ctx, http.MethodGet, "/control/transfers/"+url.PathEscape(id), token, nil, &t)
	return t, err
}

// AdminRetryTransfer re-presents a transfer's stored signed completion and
// settles strictly on receipt evidence (POST /control/transfers/{id}/retry).
// Retry is the ONLY mutation on this resource — it never picks an outcome
// (juicedocs §13): success settles, failure refunds, no receipt stays pending,
// an invalid receipt stays quarantined.
func (c *Client) AdminRetryTransfer(ctx context.Context, token, id string) (PendingTransfer, error) {
	var t PendingTransfer
	err := c.do(ctx, http.MethodPost, "/control/transfers/"+url.PathEscape(id)+"/retry", token, nil, &t)
	return t, err
}

// SettleResult is the kernel's settlement outcome (shapes verified live
// 2026-08-03). status "creditor": the peer owes us — its operator initiates;
// status/mode "exact": pay Amount on the rail and record it with the named
// settlement id; the probabilistic path reports its clear/pay outcome the same
// way. Message is the kernel's own operator instruction, shown verbatim
// (requirements.md §6.6 — the UI never computes or predicts an outcome).
type SettleResult struct {
	Handle       string `json:"handle,omitempty"`
	SettlementID string `json:"settlement_id,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Status       string `json:"status,omitempty"`
	Amount       int64  `json:"amount,omitempty"`
	Message      string `json:"message,omitempty"`
}

// AdminSettle settles the bilateral position with a peer (POST
// /control/peers/settle, body {"handle": ...} — route and body verified live
// 2026-08-03). With a settlementID it records the external rail payment for a
// paid probabilistic outcome (the CLI --cash leg, body field "settlement_id").
// Both move or record REAL external money; the UI routes each through a modal
// confirm naming the peer and amounts (requirements.md §6.6).
func (c *Client) AdminSettle(ctx context.Context, token, handleOrKey, settlementID string) (SettleResult, error) {
	body := map[string]string{"handle": handleOrKey}
	if settlementID != "" {
		body["settlement_id"] = settlementID
	}
	var r SettleResult
	err := c.do(ctx, http.MethodPost, "/control/peers/settle", token, body, &r)
	return r, err
}
