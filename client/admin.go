package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Admin (superuser) operations. The kernel serves these on the same TCP API as
// everything else, under /control/*, gated by an IsSuperuser check on the @sys
// bearer token (API.md C13). A non-superuser bearer is rejected 403
// ("superuser required") → KindUnauthorized. Routes and shapes verified live
// (2026-07-06). These never touch the kernel DB directly; the BFF is an ordinary
// HTTP client of the control surface.

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

// Peer is a subscribed/known peer (proxy user) row from the control surface.
// Available/Locked are the peer's own wallet here; PeerCredit and LastSeen are the
// §13 peer-sync cache — our prepaid credit held on the peer, and when we last heard
// from it. SuspendedAt is present only on a suspended row under ?all=1 (v0.9 unified
// moderation; denied_at was retired).
type Peer struct {
	Handle      string `json:"handle"`
	PublicKey   string `json:"public_key"`
	Available   int64  `json:"available"`
	Locked      int64  `json:"locked"`
	PeerCredit  int64  `json:"peer_credit"`            // our spendable credit on the peer
	LastSeen    string `json:"last_seen"`              // RFC3339; "" / null when never seen
	SuspendedAt string `json:"suspended_at,omitempty"` // set only on a suspended row under ?all=1
}

// Suspended reports whether this peer is frozen (its inbound calls are refused).
// Detection is by presence: the kernel returns suspended_at only on a suspended row
// (verified live 2026-07-19), so a non-empty string means suspended.
func (p Peer) Suspended() bool { return p.SuspendedAt != "" }

// Peers is the /control/peers response: known peers plus (with gossip) the
// known-network roster.
type Peers struct {
	Peers  []Peer             `json:"peers"`
	Roster []DiscoveredKernel `json:"roster,omitempty"`
}

// ActionStat is one action's advertised/earned figures inside a roster or
// inspection (verified live: action_id/name/description/price/uses/rating).
type ActionStat struct {
	ActionID    string  `json:"action_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       int64   `json:"price"`
	Uses        int64   `json:"uses"`
	Rating      float64 `json:"rating"` // 0–1 mean rating estimate (rendered as a percentage)
}

// RosterSource is one attribution for a discovered kernel: the introducer's key,
// whether it is the kernel's own manifest claim (self_reported) or hearsay via
// the introducer, and the actions seen from that source.
type RosterSource struct {
	IntroducedBy string       `json:"introduced_by"`
	SelfReported bool         `json:"self_reported"`
	Actions      []ActionStat `json:"actions"`
}

// DiscoveredKernel is one entry in the gossip roster: a kernel learned about via
// the DHT directory or a friend's gossip. Own is this operator's first-party
// earned stats for it (the "you" group), distinct from gossiped opinion.
type DiscoveredKernel struct {
	PublicKey string         `json:"public_key"`
	Handle    string         `json:"handle"`
	Own       []ActionStat   `json:"own,omitempty"`
	Sources   []RosterSource `json:"sources"`
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
// About (the peer's @sys description), public actions (each with a Description),
// transacted peers, and reachability. Online and Source are top-level fields
// (verified live): Online is true only when the live probe reached the peer; Source
// is "live" | "local" (last-known, online=false) | "none". The transacted-peers
// field is still wire-named "friends" (verified live 2026-07-19) despite the v0.9
// "peers" terminology.
type Inspection struct {
	Handle       string       `json:"handle"`
	PublicKey    string       `json:"public_key"`
	About        string       `json:"about"`
	Actions      []ActionStat `json:"actions"`
	Peers        []Peer       `json:"friends"`
	Online       bool         `json:"online"`
	Source       string       `json:"source"` // "live" | "local" (last-known) | "none"
	Reachability Reachability `json:"reachability"`
}

// Identity is this kernel's own federation identity (/control/identity).
type Identity struct {
	Handle    string   `json:"handle"`
	PublicKey string   `json:"public_key"`
	Addrs     []string `json:"addrs"`
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
// freeing the old handle for reuse. The kernel rejects renaming @sys.
func (c *Client) AdminRename(ctx context.Context, token, handle, newHandle string) error {
	body := struct {
		NewHandle string `json:"new_handle"`
	}{normalizeHandle(newHandle)}
	return c.do(ctx, http.MethodPost, "/control/users/"+url.PathEscape(normalizeHandle(handle))+"/rename", token, body, nil)
}

// AdminDeposit credits a user's balance (POST /control/deposit).
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

// AdminListPeers returns known peers, optionally with the gossip roster (gossip)
// and optionally including suspended peers (all) — GET
// /control/peers[?gossip=1][&all=1]. The kernel requires the literal value "1"
// ("true" is read as false), so each flag is emitted as "1" or omitted.
func (c *Client) AdminListPeers(ctx context.Context, token string, gossip, all bool) (Peers, error) {
	q := url.Values{}
	if gossip {
		q.Set("gossip", "1")
	}
	if all {
		q.Set("all", "1")
	}
	path := "/control/peers"
	if len(q) > 0 {
		path += "?" + q.Encode()
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

// AdminSubscribe subscribes to a kernel by public key (POST /control/peers/subscribe),
// importing its active public actions as owner-qualified proxies.
func (c *Client) AdminSubscribe(ctx context.Context, token, key string) error {
	return c.do(ctx, http.MethodPost, "/control/peers/subscribe", token, map[string]string{"key": key}, nil)
}

// AdminUnsubscribe stops consuming a peer by handle or key (POST
// /control/peers/unsubscribe), deactivating its imported proxies here.
func (c *Client) AdminUnsubscribe(ctx context.Context, token, handleOrKey string) error {
	return c.do(ctx, http.MethodPost, "/control/peers/unsubscribe", token, map[string]string{"handle": handleOrKey}, nil)
}

// AdminSuspendPeer freezes a peer's inbound calls (POST /control/users/{key}/suspend).
// The moderation route is unified with users (v0.9): it resolves a raw base64url
// public key as well as a handle, so the key is passed unnormalized (a leading @
// would make the kernel read it as a handle).
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
