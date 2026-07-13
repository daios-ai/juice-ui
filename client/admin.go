package client

import (
	"context"
	"net/http"
	"net/url"
)

// Admin (superuser) operations. The kernel serves these on the same TCP API as
// everything else, under /control/*, gated by an IsSuperuser check on the @sys
// bearer token (API.md C13). A non-superuser bearer is rejected 403
// ("superuser required") → KindUnauthorized. Routes and shapes verified live
// (2026-07-06). These never touch the kernel DB directly; the BFF is an ordinary
// HTTP client of the control surface.

// AdminUser is a user row from the control surface. The kernel signals the
// suspended (and denied) state by the PRESENCE of suspended_at/denied_at: it omits
// the field when the state is unset and includes the timestamp when set. (An older
// kernel sent the RFC3339 zero value instead of a real time; the presence check
// below handles both.)
type AdminUser struct {
	ID          string `json:"id"`
	Handle      string `json:"handle"`
	Email       string `json:"email"`
	Available   int64  `json:"available"`
	Locked      int64  `json:"locked"`
	SuspendedAt string `json:"suspended_at,omitempty"`
	DeniedAt    string `json:"denied_at,omitempty"` // same presence semantics; add Denied() if ever surfaced
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

// Peer is a friend (proxy user) row from the control surface. Available/Locked are
// the peer's own wallet here; PeerCredit and LastSeen are the §13 friend-sync cache
// — our prepaid credit held on the peer, and when we last heard from it.
type Peer struct {
	Handle     string `json:"handle"`
	PublicKey  string `json:"public_key"`
	Available  int64  `json:"available"`
	Locked     int64  `json:"locked"`
	PeerCredit int64  `json:"peer_credit"`         // our spendable credit on the peer
	LastSeen   string `json:"last_seen"`           // RFC3339; "" / null when never seen
	DeniedAt   string `json:"denied_at,omitempty"` // set only on a denied (unfriended) row under ?all=1
}

// Denied reports whether this peer is deny-listed (unfriended). Detection is by
// presence: the kernel returns denied_at only on a denied row (verified live
// 2026-07-13 via a two-kernel friend→unfriend), so a non-empty string means
// denied — matching AdminUser's presence semantics.
func (p Peer) Denied() bool { return p.DeniedAt != "" }

// Peers is the /control/peers response: friends plus (with gossip) the
// known-network roster.
type Peers struct {
	Peers  []Peer             `json:"peers"`
	Roster []DiscoveredKernel `json:"roster,omitempty"`
}

// ActionStat is one action's advertised/earned figures inside a roster or
// inspection (verified live: action_id/name/description/price/uses/rating).
type ActionStat struct {
	ActionID    string `json:"action_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       int64  `json:"price"`
	Uses        int64  `json:"uses"`
	Rating      int64  `json:"rating"`
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

// Reachability describes how a remote kernel is currently reached.
type Reachability struct {
	Path      string   `json:"path"` // "direct" | "relayed" | …
	RTTMillis int64    `json:"rtt_millis"`
	Protocols []string `json:"protocols"`
	Online    bool     `json:"online"` // false when a friended peer is currently unreachable
	Source    string   `json:"source"` // "live" | "local" (last-known) | "none"
}

// Inspection is GET /control/peers/inspect?key= — a remote kernel's identity,
// public actions, transacted friends, and reachability.
type Inspection struct {
	Handle       string       `json:"handle"`
	PublicKey    string       `json:"public_key"`
	Actions      []ActionStat `json:"actions"`
	Friends      []Peer       `json:"friends"`
	Reachability Reachability `json:"reachability"`
}

// Identity is this kernel's own federation identity (/control/identity).
type Identity struct {
	Handle    string   `json:"handle"`
	PublicKey string   `json:"public_key"`
	Addrs     []string `json:"addrs"`
}

// AdminListUsers returns every user (GET /control/users).
func (c *Client) AdminListUsers(ctx context.Context, token string) ([]AdminUser, error) {
	var out []AdminUser
	err := c.do(ctx, http.MethodGet, "/control/users", token, nil, &out)
	return out, err
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

// AdminListPeers returns friends, optionally with the gossip roster (gossip) and
// optionally including denied/unfriended peers (all) — GET
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

// AdminFriend befriends a kernel by public key (POST /control/peers/friend).
func (c *Client) AdminFriend(ctx context.Context, token, key string) error {
	return c.do(ctx, http.MethodPost, "/control/peers/friend", token, map[string]string{"key": key}, nil)
}

// AdminUnfriend ends a peer relation by handle or key (POST /control/peers/unfriend).
func (c *Client) AdminUnfriend(ctx context.Context, token, handleOrKey string) error {
	return c.do(ctx, http.MethodPost, "/control/peers/unfriend", token, map[string]string{"handle": handleOrKey}, nil)
}

// AdminInspect inspects a remote kernel by key (GET /control/peers/inspect?key=).
func (c *Client) AdminInspect(ctx context.Context, token, key string) (Inspection, error) {
	var out Inspection
	err := c.do(ctx, http.MethodGet, "/control/peers/inspect?key="+url.QueryEscape(key), token, nil, &out)
	return out, err
}
