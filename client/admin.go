package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Admin (superuser) operations. The kernel serves these on the same TCP API as
// everything else, under /v1/admin/*, gated by an IsSuperuser check on the sys
// bearer token (API.md C13). A non-superuser bearer is rejected 403
// ("superuser required") → KindUnauthorized. These never touch the kernel DB
// directly; the BFF is an ordinary HTTP client of the admin surface.
//
// The route says which kind of target it takes, and a target of the other kind
// is refused (verified live, docs/kernel-v0.15-verification.md §2): `users`
// takes an address (handle@kernel, the `@` unescaped) or account id, `peers` a
// petname or public key. So a caller
// must know which namespace it is addressing BEFORE it dials — which the UI
// does, since its own routes are already split the same way. There is no route
// by which a peer is credited.

// AdminUser is one local account as the operator sees it
// (GET /v1/admin/users/{address}). It is a user and nothing else: the kernel
// answers 404 "… is not a user here" for a peer key, so there is no second
// record shape to discriminate.
type AdminUser struct {
	Address     string `json:"address"`
	Description string `json:"description"`
	Available   int64  `json:"available"`
	Locked      int64  `json:"locked"`
	SuspendedAt string `json:"suspended_at,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// Suspended reports whether the user is currently suspended. Detection is by
// presence: the kernel omits suspended_at when active and sends the suspension
// timestamp when suspended, so a non-empty string means suspended — robust also to
// an older kernel that sent the RFC3339 zero value.
func (u AdminUser) Suspended() bool { return u.SuspendedAt != "" }

// Peer is one row of the merged kernel roster: every kernel this one knows,
// keyed by public key and excluding itself. Traded splits the two classes: a
// COUNTERPARTY is a kernel a transaction here names, a DISCOVERED peer is a
// catalog entry the discovery loop learned and nothing more. Either can be
// named and suspended by key.
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
	Petname   string `json:"petname"`  // bound here; resolves; "" until bound
	Nickname  string `json:"nickname"` // self-asserted by the remote; never resolves
	About     string `json:"about"`    // the peer's sys description; counterparty prose
	PublicKey string `json:"public_key"`
	Traded    bool   `json:"traded"`  // counterparty: a transaction here names it; else discovered
	Actions   int    `json:"actions"` // public actions in its gossiped catalog
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

// PeerAction is one action a peer serves, as carried by an inspection. Name is
// the action's address as this kernel would call it (`seller@bee/slow`); the
// projection carries no stats, so it shows no reputation. Price is
// the all-in local price and Indicative says so: it is quoted from the catalog
// and re-quoted authoritatively when the action resolves (kernel §D13).
type PeerAction struct {
	ActionID    string `json:"action_id"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Price       int64  `json:"price"`
	Indicative  bool   `json:"indicative"`
}

// Evidence is one derived metric row from the local evidence cache: what a
// single issuer's signed receipts say about one action (juicedocs §D16) — on a
// peer's inspection, and in an action read's evidence block. RatingCount/RatingMean count only TRADE-BACKED ratings — the
// two-kernel receipt link holds — while UnverifiedRatings counts those retained
// without that link. Equivocating ratings are excluded by the kernel. This
// proves attribution, never honesty, so it is displayed as evidence beside its
// issuer and never folded into an action's first-party Stats. Contradictions
// counts the trades this issuer told two stories about, which count for nothing
// else.
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
	Contradictions   int64   `json:"contradictions"`
}

// Reachability holds the transport diagnostics only. Online/Source are NOT here —
// the kernel returns them at the top level of the inspection (see Inspection).
type Reachability struct {
	Path      string `json:"path"` // "direct" | "relayed" | "unreachable" | …
	RTTMillis int64  `json:"rtt_millis"`
	Version   string `json:"version"`         // the peer's agent string, juice-kernel/<version>
	Error     string `json:"error,omitempty"` // why an unreachable peer could not be dialled
}

// Inspection is GET /v1/admin/peers/{key}/inspect — a remote kernel's identity, its
// About (the peer's sys description), the public actions it serves, the evidence
// this kernel retains about it, plus reachability. It states no suspension: that
// is the kernel row's (PeerRecord), as is everything moderation.
// Online and Source are top-level fields (verified live): Online is true only
// when the live probe reached the peer; Source is "live" | "local" (last-known,
// online=false) | "none" (a well-formed key this kernel has never learned — a
// usable page, not an error). The transacted-peers list ("friends") is GONE as
// of v0.12 — endorsement by association is replaced by signed, per-issuer
// Evidence.
type Inspection struct {
	Petname      string       `json:"petname"`  // bound here; resolves; "" until bound
	Nickname     string       `json:"nickname"` // self-asserted by the remote; never resolves
	PublicKey    string       `json:"public_key"`
	About        string       `json:"about"`
	Actions      []PeerAction `json:"actions"`
	Evidence     []Evidence   `json:"evidence"`
	Online       bool         `json:"online"`
	Source       string       `json:"source"` // "live" | "local" (last-known) | "none"
	Reachability Reachability `json:"reachability"`
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

// IssuerEvidence is one issuer's block of evidence rows. Self marks the block
// the provider issued about its own actions.
type IssuerEvidence struct {
	Issuer string
	Self   bool
	Rows   []Evidence
}

// Identity is GET /v1/admin/kernel: who this kernel is, what its money is
// doing, and how much unpaid work it is carrying.
//
// Sys splits the superuser's balance into what it has earned and what is merely
// in transit on the rail (open payouts, deposits nobody has claimed, fuel locks)
// — money held on sys and never spendable (kernel §D3). Solvency is the identity
// every user credit is backed by cash: Liabilities − Vault must be zero, and Gap
// names the difference when a record has been broken by hand. Stop is present
// only while outgoing rail work is halted, and names why.
//
// Exposure is what this kernel has delivered to foreign buyers and not been paid
// for, against the CreditLimit it set — one number for the whole kernel, so
// minting identities cannot multiply it (kernel §D14). Lottery is the ticket
// this kernel writes and LotteryMax the largest it will accept from a buyer —
// a call drawing above it is refused with a signed rejection, since no retry
// changes it (kernel §P10). The three bps rates are the money rules it serves
// under.
type Identity struct {
	Handle            string   `json:"handle"`
	PublicKey         string   `json:"public_key"`
	About             string   `json:"about"` // sys's description — what the network reads about us
	Addrs             []string `json:"addrs"`
	BlockchainAddress string   `json:"blockchain_address"` // where this kernel is paid; "" on a world without addresses

	Sys      SysFunds  `json:"sys"`
	Solvency Solvency  `json:"solvency"`
	Stop     *RailStop `json:"stop"` // nil unless outgoing rail work is halted

	Lottery     int64 `json:"lottery"`
	LotteryMax  int64 `json:"lottery_max"`
	CreditLimit int64 `json:"credit_limit"`
	Exposure    int64 `json:"exposure"`
	FeeBps      int64 `json:"fee_bps"`
	RemoteBps   int64 `json:"remote_bps"`
	ImportBps   int64 `json:"import_bps"`
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

// RailStop is a halt on outgoing rail work: deposits, paid work and reads
// continue, and the halt lifts when the blocked payment clears.
type RailStop struct {
	Reason string `json:"reason"`
	Since  string `json:"since"`
}

// Deposits is GET /v1/admin/kernel/deposits: what this kernel is waiting on.
// Held are payments whose sender nobody has registered, and the operator
// attributes one to a user. Owed is every open obligation — exposure itemised —
// and carries no operator act at all: an obligation closes when its buyer pays
// (§6.6).
type Deposits struct {
	Held []HeldDeposit `json:"deposits"`
	Owed []Obligation  `json:"owed"`
}

// HeldDeposit is a payment the rail has witnessed whose sender no account has
// registered, so the kernel cannot decide whose it is and holds it on sys until
// the operator says (kernel §D23). Party is the sending address — the fact
// the operator reads to decide, and ID is what names the payment when attributing
// it. Shape and both acts verified live on a chain world (anvil, decimals 6).
type HeldDeposit struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Amount    int64  `json:"amount"`
	Status    string `json:"status"`
	TxHash    string `json:"tx_hash"`
	CreatedAt string `json:"created_at"`
	Party     string `json:"party"`
}

// Obligation is one cross-kernel debt owed TO this kernel: the ticket both
// kernels know it by, and what the call owed. Once its buyer has said how the
// draw came out it also carries Status, the Amount drawn — a face value that
// may exceed Obligation, since a small debt pays a larger amount with the
// probability that makes the expected payment the debt (kernel §P10) — and the
// TxHash named. Peer is the buyer's public key.
//
// A buyer that has said nothing yet is listed with an EMPTY Status and a ZERO
// Amount (verified live, docs/kernel-v0.15-verification.md §3). Those are absent
// fields, not values: rendering them as an empty badge and "0" would state two
// things the kernel has not said.
type Obligation struct {
	ID         string `json:"id"`
	Obligation int64  `json:"obligation"`
	Amount     int64  `json:"amount"`
	TxHash     string `json:"tx_hash"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	Peer       string `json:"peer"`
}

// Answered reports that the buyer has said how the draw came out, which is what
// gives this row a status, an amount and a transaction to name.
func (o Obligation) Answered() bool { return o.Status != "" }

// AdminListUsers returns every user (GET /v1/admin/users). The kernel pages
// every list endpoint (default 50, ceiling 200, §R6), so we page through with
// offset — a single unpaged call would silently truncate at 50, and the admin
// roster must be sorted and paginated over the full set.
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
		if err := c.do(ctx, http.MethodGet, "/v1/admin/users?"+q.Encode(), token, nil, &batch); err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
}

// AdminGetUser returns one user by address (GET /v1/admin/users/{address}).
func (c *Client) AdminGetUser(ctx context.Context, token, address string) (AdminUser, error) {
	var u AdminUser
	err := c.do(ctx, http.MethodGet, "/v1/admin/users/"+url.PathEscape(address), token, nil, &u)
	return u, err
}

// AdminSuspendUser suspends a user (POST /v1/admin/users/{address}/suspend).
// The kernel answers with the row it wrote (R5); the UI re-reads its page, so
// the body is discarded.
func (c *Client) AdminSuspendUser(ctx context.Context, token, address string) error {
	return c.do(ctx, http.MethodPost, "/v1/admin/users/"+url.PathEscape(address)+"/suspend", token, nil, nil)
}

// AdminUnsuspendUser lifts a suspension (POST /v1/admin/users/{address}/unsuspend).
func (c *Client) AdminUnsuspendUser(ctx context.Context, token, address string) error {
	return c.do(ctx, http.MethodPost, "/v1/admin/users/"+url.PathEscape(address)+"/unsuspend", token, nil, nil)
}

// AdminRenameUser renames a local account
// (POST /v1/admin/users/{address}/rename), freeing the old address so every
// reference follows the account. The kernel rejects renaming sys.
//
// The body field is new_name. It was new_handle up to v0.12.13 and renamed in
// v0.12.14; a wrong field name is rejected with a message naming a third field
// ("handle is required"), so this is not guessable from the error and is
// re-verified on each kernel (docs/kernel-v0.15-verification.md §2).
func (c *Client) AdminRenameUser(ctx context.Context, token, address, newName string) error {
	return c.do(ctx, http.MethodPost, "/v1/admin/users/"+url.PathEscape(address)+"/rename", token, renameBody(newName), nil)
}

// AdminDeposit records a payment made outside the system, crediting one USER
// (POST /v1/admin/users/{address}/deposit). Ref names that payment — the
// operator's own record of it where the world has no chain, or the transaction
// that carried it where it has. It is mandatory: the kernel credits nothing it
// cannot name, a second use of the same fact moves no money, and the same fact
// on other terms is refused.
//
// Amount is omitted when zero, which is the amount-less form: attributing a
// payment already received whose sender was unknown.
//
// There is no peer form. A peer holds no account here, and what a
// peer owes closes when it pays — the chain payment where the world has one,
// the buyer's own signed reveal where it has none, recorded by the seller's
// kernel itself with no operator act (kernel §D23/§P10).
func (c *Client) AdminDeposit(ctx context.Context, token, address string, amount int64, ref, reason string) (LedgerEntry, error) {
	body := map[string]any{"ref": ref}
	if amount > 0 {
		body["amount"] = amount
	}
	if reason != "" {
		body["reason"] = reason
	}
	var e LedgerEntry
	err := c.do(ctx, http.MethodPost, "/v1/admin/users/"+url.PathEscape(address)+"/deposit", token, body, &e)
	return e, err
}

// AdminListDeposits returns what this kernel is waiting on
// (GET /v1/admin/kernel/deposits): payments held for a sender nobody has
// registered, and every obligation still open — the work delivered to foreign
// buyers and not paid for, which is what `exposure` is made of, itemised.
func (c *Client) AdminListDeposits(ctx context.Context, token string) (Deposits, error) {
	var d Deposits
	err := c.do(ctx, http.MethodGet, "/v1/admin/kernel/deposits", token, nil, &d)
	return d, err
}

// renameBody is the one rename payload, shared by both namespaces so the field
// name has a single definition.
func renameBody(newName string) any {
	return struct {
		NewName string `json:"new_name"`
	}{newName}
}

// AdminListPeers returns the whole merged roster, optionally including suspended
// counterparties — GET /v1/admin/peers[?all=1]. The kernel requires the literal
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
		if err := c.do(ctx, http.MethodGet, "/v1/admin/peers?"+q.Encode(), token, nil, &batch); err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
}

// AdminIdentity returns this kernel's own identity, rail position and money
// rules (GET /v1/admin/kernel).
func (c *Client) AdminIdentity(ctx context.Context, token string) (Identity, error) {
	var id Identity
	err := c.do(ctx, http.MethodGet, "/v1/admin/kernel", token, nil, &id)
	return id, err
}

// There is no subscribe/unsubscribe: consuming a peer's actions needs no
// relationship, and a proxy row is filled by resolve-on-use alone (juicedocs
// §13). A peer becomes known by its first signed call — never by a
// deposit, which credits only users — and the durable lever is suspend; an
// uncalled peer lapses through the kernel's retention.
//
// Every peer verb below takes a petname or a public key, and refuses a user.
// The UI passes the public key throughout: it is the only global name, and an
// unbound kernel has no petname to pass.

// AdminSuspendPeer freezes a peer's inbound calls
// (POST /v1/admin/peers/{key}/suspend).
func (c *Client) AdminSuspendPeer(ctx context.Context, token, key string) error {
	return c.do(ctx, http.MethodPost, "/v1/admin/peers/"+url.PathEscape(key)+"/suspend", token, nil, nil)
}

// AdminUnsuspendPeer lifts a peer suspension (POST /v1/admin/peers/{key}/unsuspend).
func (c *Client) AdminUnsuspendPeer(ctx context.Context, token, key string) error {
	return c.do(ctx, http.MethodPost, "/v1/admin/peers/"+url.PathEscape(key)+"/unsuspend", token, nil, nil)
}

// AdminRenamePeer binds a kernel's PETNAME, exactly
// (POST /v1/admin/peers/{key}/rename), rejecting an occupied name rather than
// suffixing it. This is the only way to name a kernel this node has merely
// discovered (§13).
func (c *Client) AdminRenamePeer(ctx context.Context, token, key, petname string) error {
	return c.do(ctx, http.MethodPost, "/v1/admin/peers/"+url.PathEscape(key)+"/rename", token, renameBody(petname), nil)
}

// PeerRecord is GET /v1/admin/peers/{key}: this kernel's own row for a peer —
// its names, where it is paid, when it became known here, the contact cache,
// and its moderation. A peer holds no account and no balance here (kernel
// §D14): suspension freezes this row.
type PeerRecord struct {
	Petname           string `json:"petname"`
	Nickname          string `json:"nickname"`
	PublicKey         string `json:"public_key"`
	About             string `json:"about"`
	BlockchainAddress string `json:"blockchain_address"`
	FirstSeen         string `json:"first_seen"`
	SuspendedAt       string `json:"suspended_at"`
}

// Suspended reports whether this kernel is frozen (its inbound calls are
// refused): the kernel sets suspended_at only while it is.
func (p PeerRecord) Suspended() bool { return p.SuspendedAt != "" }

// AdminPeer reads this kernel's record of a peer by key (GET /v1/admin/peers/{key}).
func (c *Client) AdminPeer(ctx context.Context, token, key string) (PeerRecord, error) {
	var out PeerRecord
	err := c.do(ctx, http.MethodGet, "/v1/admin/peers/"+url.PathEscape(key), token, nil, &out)
	return out, err
}

// AdminInspect inspects a remote kernel (GET /v1/admin/peers/{key}/inspect).
func (c *Client) AdminInspect(ctx context.Context, token, key string) (Inspection, error) {
	var out Inspection
	err := c.do(ctx, http.MethodGet, "/v1/admin/peers/"+url.PathEscape(key)+"/inspect", token, nil, &out)
	return out, err
}
