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

// AdminUser is one account as the operator sees it (GET /control/users/{target}).
// The target resolves in EITHER namespace, and the two records are disjoint
// (verified live on v0.12.16): a local user answers with handle/id/description
// and balances; a KERNEL answers with petname/nickname/about/public_key and its
// sync cache, and carries balances only once it holds an account here. IsKernel
// discriminates, so neither is rendered as a half-empty version of the other.
type AdminUser struct {
	ID          string `json:"id"`
	Handle      string `json:"handle"`
	Description string `json:"description"`
	Available   int64  `json:"available"`
	Locked      int64  `json:"locked"`
	SuspendedAt string `json:"suspended_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`

	// Kernel half (§13 petname model): present only when the target is a kernel.
	PublicKey   string `json:"public_key"`
	Petname     string `json:"petname"`
	Nickname    string `json:"nickname"`
	About       string `json:"about"`
	LastSeen    string `json:"last_seen"`
	RailAddress string `json:"rail_address"`
}

// IsKernel reports that this record is a remote kernel rather than a local
// user. A kernel account holds no handle by construction (the schema forbids it
// one, §3), so the public key is the discriminator.
func (u AdminUser) IsKernel() bool { return u.PublicKey != "" }

// Suspended reports whether the user is currently suspended. Detection is by
// presence: the kernel omits suspended_at when active and sends the suspension
// timestamp when suspended, so a non-empty string means suspended — robust also to
// an older kernel that sent the RFC3339 zero value.
func (u AdminUser) Suspended() bool { return u.SuspendedAt != "" }

// Peer is one row of the merged kernel roster: every kernel this one knows,
// keyed by public key and excluding itself. HasAccount splits the two classes —
// a COUNTERPARTY holds an account here, a DISCOVERED peer is a catalog entry the
// discovery loop learned and nothing more, so it has no user row to suspend or
// rename.
//
// A peer holds NO MONEY on any path (kernel §D14): a foreign call is funded by
// the serving kernel and every obligation settles per call on the rail (§P10),
// so this row carries names, reachability and a catalog count — never a balance.
// SuspendedAt is present only on a suspended row under ?all=1.
//
// Naming follows Stiegler's petname model, which the kernel implements (§D15):
// PublicKey is the only global, self-certifying name; Nickname is what the
// kernel calls ITSELF and never resolves (self-asserted, collides freely, and
// is counterparty prose); Petname is the name bound HERE and the only friendly
// one that resolves. An unbound kernel has an empty Petname and stays callable
// by key. Every URL, form and mutation keys on PublicKey — never on a displayed
// name.
type Peer struct {
	Petname    string `json:"petname"`  // bound here; resolves; "" until bound
	Nickname   string `json:"nickname"` // self-asserted by the remote; never resolves
	About      string `json:"about"`    // the peer's sys description; counterparty prose
	PublicKey  string `json:"public_key"`
	HasAccount bool   `json:"has_account"` // counterparty (account here) vs discovered
	Actions    int    `json:"actions"`     // public actions in its gossiped catalog
	// LastSeen and LastContactFailedAt are the kernel's contact cache (§13):
	// when a verified exchange last succeeded, and when one last failed. Raw
	// RFC3339 facts, each "" until it has happened — the kernel states neither
	// as a verdict and neither ever moves backwards, so the UI dates them and
	// draws no conclusion about whether the peer is up.
	LastSeen            string `json:"last_seen"`
	LastContactFailedAt string `json:"last_contact_failed_at"`
	SuspendedAt         string `json:"suspended_at,omitempty"` // set only on a suspended row under ?all=1
}

// Suspended reports whether this peer is frozen (its inbound calls are refused).
// Detection is by presence: the kernel returns suspended_at only on a suspended row
// (verified live 2026-07-19), so a non-empty string means suspended.
func (p Peer) Suspended() bool { return p.SuspendedAt != "" }

// PeerAction is one action a peer serves, as carried by an inspection. The name
// is BARE (`register`, not `chat/register`) and the projection carries no owner
// or stats, so it is not an addressable ref and shows no reputation. Price is
// the all-in local price and Indicative says so: it is quoted from the catalog
// and re-quoted authoritatively when the action resolves (kernel §D13).
type PeerAction struct {
	ActionID    string `json:"action_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       int64  `json:"price"`
	Indicative  bool   `json:"indicative"`
}

// Evidence is one derived metric row from the local evidence cache: what a
// single issuer's signed receipts say about one action of the inspected kernel
// (juicedocs §13). RatingCount/RatingMean count only TRADE-BACKED ratings — the
// two-kernel receipt link holds — while UnverifiedRatings counts those retained
// without that link. Equivocating ratings are excluded by the kernel. This
// proves attribution, never honesty, so it is displayed as evidence beside its
// issuer and never folded into an action's first-party Stats.
//
// CorroboratedUses is the same two-kernel link applied to uses rather than
// ratings: how many of this issuer's interactions the subject's own evidence
// confirms. It is meaningful only on a third-party row (an issuer reporting on
// someone else) — a subject's self-reported rows carry 0 by construction.
//
// `avg_latency_ms` is deliberately NOT decoded: the kernel emits it fractional
// (690.5, 90760.5 — verified live 2026-08-06), so an integer field fails the
// whole inspection, and nothing renders it. A field the UI does not show is a
// decode hazard, not documentation.
type Evidence struct {
	IssuerPublicKey  string  `json:"issuer_public_key"`
	SubjectActionID  string  `json:"subject_action_id"`
	Uses             int64   `json:"uses"`
	Successes        int64   `json:"successes"`
	Failures         int64   `json:"failures"`
	CorroboratedUses int64   `json:"corroborated_uses"`
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

// PeerAccount is this kernel's account row for the inspected kernel, served
// inline by the inspection and NULL for a kernel that holds no account here. Its
// presence is what distinguishes a counterparty from a discovered peer on the
// inspect page, so it is a pointer. It carries no balance: a peer account is
// identity, attribution and moderation state, never a wallet (kernel §D14).
type PeerAccount struct {
	Suspended bool `json:"suspended"`
}

// Inspection is GET /control/peers/inspect?key= — a remote kernel's identity, its
// About (the peer's sys description), the public actions it serves, the evidence
// this kernel retains about it, and our account with it, plus reachability.
// Online and Source are top-level fields (verified live): Online is true only
// when the live probe reached the peer; Source is "live" | "local" (last-known,
// online=false) | "none" (a well-formed key this kernel has never learned — a
// usable page, not an error). The transacted-peers list ("friends") is GONE as
// of v0.12 — endorsement by association is replaced by signed, per-issuer
// Evidence. The wire also carries the `steps` this peer holds for us, which has
// no UI surface yet (ROADMAP).
type Inspection struct {
	Petname      string       `json:"petname"`  // bound here; resolves; "" until bound
	Nickname     string       `json:"nickname"` // self-asserted by the remote; never resolves
	PublicKey    string       `json:"public_key"`
	About        string       `json:"about"`
	Actions      []PeerAction `json:"actions"`
	Evidence     []Evidence   `json:"evidence"`
	Account      *PeerAccount `json:"account"` // nil ⇒ no billing account here
	Online       bool         `json:"online"`
	Source       string       `json:"source"` // "live" | "local" (last-known) | "none"
	Reachability Reachability `json:"reachability"`
}

// Discovered reports that this kernel is known through the discovery loop but
// holds no account here. The predicate is POSITIVE on Source so that a future or
// empty value never silently reads as discovered: a stranger key answers
// source="none" with no account and is not part of the known network.
func (i Inspection) Discovered() bool {
	return i.Account == nil && (i.Source == "live" || i.Source == "local")
}

// SelfEvidence is the subject's own execution evidence — the rows it issued
// about itself. Counted alone it is the execution summary: a remote call is
// never double-counted (juicedocs §13). It shares IssuerEvidence with the
// counterparty view so both render through one partial, and carries Self so
// that partial can drop the corroborated column, which only a third party's
// rows can populate.
func (i Inspection) SelfEvidence() IssuerEvidence {
	out := IssuerEvidence{Issuer: i.PublicKey, Self: true}
	for _, e := range i.Evidence {
		if e.IssuerPublicKey == i.PublicKey {
			out.Rows = append(out.Rows, e)
		}
	}
	return out
}

// CounterpartyEvidence is every OTHER issuer's directly-observed experience of
// this kernel, grouped by issuer in wire order. It is shown beside the execution
// summary and never folded into it: attribution is provable, honesty is not.
func (i Inspection) CounterpartyEvidence() []IssuerEvidence {
	var out []IssuerEvidence
	at := map[string]int{}
	for _, e := range i.Evidence {
		if e.IssuerPublicKey == i.PublicKey {
			continue
		}
		idx, seen := at[e.IssuerPublicKey]
		if !seen {
			out = append(out, IssuerEvidence{Issuer: e.IssuerPublicKey})
			idx = len(out) - 1
			at[e.IssuerPublicKey] = idx
		}
		out[idx].Rows = append(out[idx].Rows, e)
	}
	return out
}

// IssuerEvidence is one issuer's block of evidence rows about the inspected
// kernel. Self marks the block the subject issued about itself.
type IssuerEvidence struct {
	Issuer string
	Self   bool
	Rows   []Evidence
}

// Identity is GET /control/identity: who this kernel is, what its money is
// doing, and how much unpaid work it is carrying.
//
// Sys splits the superuser's balance into what it has earned and what is merely
// in transit on the rail (open payouts, deposits nobody has claimed, fuel locks)
// — money held on sys and never spendable (kernel §D3). Solvency is the identity
// every user credit is backed by cash: Liabilities − Vault must be zero, and Gap
// names the difference when a record has been broken by hand. Custody compares
// that vault against the rail's own finalized reads and is reported only at a
// valid cut, so Checked says whether the comparison ran at all. Stop is present
// only while outgoing rail work is halted, and names why.
//
// Exposure is what this kernel has delivered to foreign buyers and not been paid
// for, against the CreditLimit it set — one number for the whole kernel, so
// minting identities cannot multiply it (kernel §D14). Lottery is the largest
// ticket this kernel writes, and the three bps rates are the money rules it
// serves under.
type Identity struct {
	Handle        string   `json:"handle"`
	PublicKey     string   `json:"public_key"`
	About         string   `json:"about"` // sys's description — what the network reads about us
	Addrs         []string `json:"addrs"`
	Network       string   `json:"network"`
	NetworkDigest string   `json:"network_digest"`
	RailAddress   string   `json:"rail_address"` // where this kernel is paid; "" on a world without addresses

	Finalized *RailHoldings `json:"finalized"` // nil until a chain rail has read a block
	Sys       SysFunds      `json:"sys"`
	Solvency  Solvency      `json:"solvency"`
	Custody   Custody       `json:"custody"`
	Stop      *RailStop     `json:"stop"` // nil unless outgoing rail work is halted

	Lottery     int64 `json:"lottery"`
	CreditLimit int64 `json:"credit_limit"`
	Exposure    int64 `json:"exposure"`
	FeeBps      int64 `json:"fee_bps"`
	RemoteBps   int64 `json:"remote_bps"`
	ImportBps   int64 `json:"import_bps"`
}

// RailHoldings is what the rail holds for this kernel at a finalized block.
type RailHoldings struct {
	Token string `json:"token"`
	Gas   string `json:"gas"`
	Block int64  `json:"block"`
}

// SysFunds splits the operator's balance: what it earned, and what is in transit
// on the rail and therefore not spendable.
type SysFunds struct {
	Earnings       int64 `json:"earnings"`
	PendingPayouts int64 `json:"pending_payouts"`
	HeldDeposits   int64 `json:"held_deposits"`
	RefillLocks    int64 `json:"refill_locks"`
}

// Solvency is the backing identity: every user credit against the money that
// crossed in. Gap names the term that differs when it does not hold.
type Solvency struct {
	Liabilities int64 `json:"liabilities"`
	Vault       int64 `json:"vault"`
	Gap         int64 `json:"gap"`
}

// Custody compares the kernel's own vault figure against the rail's finalized
// reads. Checked is false when no valid cut was available, and then Difference
// and OK say nothing.
type Custody struct {
	Checked    bool  `json:"checked"`
	Difference int64 `json:"difference"`
	OK         bool  `json:"ok"`
}

// RailStop is a halt on outgoing rail work: deposits, paid work and reads
// continue, and the halt lifts when the blocked payment clears.
type RailStop struct {
	Reason string `json:"reason"`
	Since  string `json:"since"`
}

// Deposits is GET /control/deposits: the money awaiting the operator. Held are
// payments whose sender nobody has registered; Owed are obligations a buyer says
// it has paid whose money this kernel has not yet seen. Each Owed row is closed
// by recording its payment against the ticket id (§6.6).
type Deposits struct {
	Held []HeldDeposit `json:"deposits"`
	Owed []Obligation  `json:"owed"`
}

// HeldDeposit is a payment booked against no account yet. Only its id is
// contractually named (it is what --ref names to attribute it), so nothing else
// is rendered until a chain world confirms the rest of the shape.
type HeldDeposit struct {
	ID        string `json:"id"`
	Amount    int64  `json:"amount"`
	CreatedAt string `json:"created_at"`
}

// Obligation is one cross-kernel debt owed TO this kernel: the ticket both
// kernels know it by, what the call owed, and what the draw decided must
// actually be paid — Amount is a face value that may exceed Obligation, since a
// small debt pays a larger amount with the probability that makes the expected
// payment the debt (kernel §P10). Peer is the buyer's public key, and TxHash the
// payment the buyer named.
type Obligation struct {
	ID         string `json:"id"`
	Obligation int64  `json:"obligation"`
	Amount     int64  `json:"amount"`
	TxHash     string `json:"tx_hash"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	Peer       string `json:"peer"`
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
	err := c.do(ctx, http.MethodGet, "/control/users/"+url.PathEscape(handle), token, nil, &u)
	return u, err
}

// AdminSuspendUser suspends a user (POST /control/users/{handle}/suspend).
func (c *Client) AdminSuspendUser(ctx context.Context, token, handle string) error {
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(handle)+"/suspend", token, nil, nil)
}

// AdminUnsuspendUser lifts a suspension (POST /control/users/{handle}/unsuspend).
func (c *Client) AdminUnsuspendUser(ctx context.Context, token, handle string) error {
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(handle)+"/unsuspend", token, nil, nil)
}

// AdminRename renames one target (POST /control/users/{target}/rename). The
// target is a user handle, or a kernel's public key — the two namespaces are
// disjoint, so the kernel resolves by shape and refuses an ambiguous bare name.
// A user target renames the handle, freeing the old one and rebinding every
// reference; a kernel target binds its PETNAME, exactly, and is the only way to
// name a kernel this node has merely discovered (§13). The kernel rejects
// renaming sys.
//
// The body field is new_name for BOTH targets. It was new_handle up to
// v0.12.13 and renamed in v0.12.14 (verified live); a wrong field name is
// rejected with a message naming a third field, so this is not guessable from
// the error.
func (c *Client) AdminRename(ctx context.Context, token, target, newName string) error {
	body := struct {
		NewName string `json:"new_name"`
	}{newName}
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(target)+"/rename", token, body, nil)
}

// AdminDeposit records a payment made outside the system (POST /control/deposit).
// Ref names that payment — the operator's own record of it where the world has no
// chain, the transaction that carried it where it has, or, when the target is a
// PEER, the ticket id of the obligation the payment closes. It is mandatory: the
// kernel credits nothing it cannot name, a second use of the same fact moves no
// money, and the same fact on other terms is refused.
//
// Amount is omitted when zero, which is how the two amount-less forms are sent:
// attributing a payment already received, and closing a peer's obligation for the
// amount the draw decided. A peer's payment reaches the seller it is owed to — a
// peer account is identity, never a wallet (kernel §D14).
func (c *Client) AdminDeposit(ctx context.Context, token, handle string, amount int64, ref, reason string) (LedgerEntry, error) {
	body := map[string]any{"handle": handle, "ref": ref}
	if amount > 0 {
		body["amount"] = amount
	}
	if reason != "" {
		body["reason"] = reason
	}
	var e LedgerEntry
	err := c.do(ctx, http.MethodPost, "/control/deposit", token, body, &e)
	return e, err
}

// AdminListDeposits returns the money awaiting the operator: payments held for a
// sender nobody has registered, and obligations a buyer says it has paid whose
// money this kernel has not yet seen (GET /control/deposits).
func (c *Client) AdminListDeposits(ctx context.Context, token string) (Deposits, error) {
	var d Deposits
	err := c.do(ctx, http.MethodGet, "/control/deposits", token, nil, &d)
	return d, err
}

// AdminListPeers returns the whole merged roster, optionally including suspended
// counterparties — GET /control/peers[?all=1]. The kernel requires the literal
// value "1" ("true" is read as false), so the flag is emitted as "1" or omitted.
//
// It pages to exhaustion like AdminListUsers: the roster is no longer bounded by
// trade but by the size of the known network, so a single unpaged call would
// silently truncate at the kernel's default 50.
func (c *Client) AdminListPeers(ctx context.Context, token string, all bool) ([]Peer, error) {
	const page = 200 // server ceiling
	var out []Peer
	for offset := 0; ; offset += page {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(page))
		if offset > 0 {
			q.Set("offset", strconv.Itoa(offset))
		}
		if all {
			q.Set("all", "1")
		}
		var batch []Peer
		if err := c.do(ctx, http.MethodGet, "/control/peers?"+q.Encode(), token, nil, &batch); err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
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
