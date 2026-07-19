package client

import (
	"context"
	"strings"
	"testing"
)

func TestAdminListUsers(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"id":"U1","handle":"@alice","email":"a@x","available":230,"locked":0}]`
	users, err := f.client().AdminListUsers(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || !strings.HasPrefix(f.lastPath, "/control/users?") || !strings.Contains(f.lastPath, "limit=200") {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
	if f.lastAuth != "Bearer tok" {
		t.Errorf("missing bearer, got %q", f.lastAuth)
	}
	if len(users) != 1 || users[0].Handle != "@alice" || users[0].Available != 230 {
		t.Errorf("decoded users wrong: %+v", users)
	}
}

func TestAdminGetUserAndSuspended(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","handle":"@alice","suspended_at":"2026-07-05T00:00:00Z"}`
	u, err := f.client().AdminGetUser(context.Background(), "tok", "@alice")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/@alice" {
		t.Errorf("path = %q", f.lastPath)
	}
	if !u.Suspended() {
		t.Error("a real suspended_at must report Suspended()")
	}
	// Presence, not value: an older kernel sent the RFC3339 zero value for a
	// suspended user, so that regression case must still report suspended.
	if !(AdminUser{SuspendedAt: "0001-01-01T00:00:00Z"}).Suspended() {
		t.Error("a present suspended_at (even the zero value) must report suspended")
	}
	if (AdminUser{}).Suspended() {
		t.Error("omitted suspended_at must not report suspended")
	}
}

func TestAdminSuspendUnsuspend(t *testing.T) {
	f := newFake()
	defer f.close()
	if err := f.client().AdminSuspendUser(context.Background(), "tok", "@bob"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/control/users/@bob/suspend" {
		t.Errorf("suspend request = %s %s", f.lastMethod, f.lastPath)
	}
	if err := f.client().AdminUnsuspendUser(context.Background(), "tok", "@bob"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/@bob/unsuspend" {
		t.Errorf("unsuspend path = %q", f.lastPath)
	}
}

func TestAdminDepositWithdraw(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"direction":"credit","amount":100,"operator_handle":"@sys","target_handle":"@alice"}`
	adj, err := f.client().AdminDeposit(context.Background(), "tok", "@alice", 100, "grant", "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/control/deposit" {
		t.Errorf("deposit request = %s %s", f.lastMethod, f.lastPath)
	}
	body := string(f.lastBody)
	for _, want := range []string{`"handle":"@alice"`, `"amount":100`, `"reason":"grant"`, `"external_key":"idem-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("deposit body missing %s: %s", want, body)
		}
	}
	if adj.Direction != "credit" || adj.Amount != 100 {
		t.Errorf("adjustment decode wrong: %+v", adj)
	}
	// Withdraw hits its own path; empty reason/key are omitted.
	if _, err := f.client().AdminWithdraw(context.Background(), "tok", "@alice", 5, "", ""); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/withdraw" {
		t.Errorf("withdraw path = %q", f.lastPath)
	}
	if strings.Contains(string(f.lastBody), "reason") || strings.Contains(string(f.lastBody), "external_key") {
		t.Errorf("empty reason/key must be omitted: %s", f.lastBody)
	}
}

func TestAdminPeersAndIdentity(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"peers":[{"handle":"@peerb","public_key":"KEY","available":10,"locked":0,"peer_credit":250,"last_seen":"2026-07-12T00:00:00Z"}]}`
	p, err := f.client().AdminListPeers(context.Background(), "tok", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers?gossip=1" {
		t.Errorf("peers path = %q", f.lastPath)
	}
	if len(p.Peers) != 1 || p.Peers[0].Handle != "@peerb" {
		t.Errorf("peers decode wrong: %+v", p)
	}
	// The §13 friend-sync cache — our credit on the peer and when last seen.
	if p.Peers[0].PeerCredit != 250 || p.Peers[0].LastSeen != "2026-07-12T00:00:00Z" {
		t.Errorf("peer sync-cache decode wrong: credit=%d last_seen=%q", p.Peers[0].PeerCredit, p.Peers[0].LastSeen)
	}
	// gossip+all → both flags emitted as literal "1" (kernel reads "true" as false).
	if _, err := f.client().AdminListPeers(context.Background(), "tok", true, true); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers?all=1&gossip=1" {
		t.Errorf("peers all path = %q", f.lastPath)
	}
	// Neither flag → bare path, no query.
	if _, err := f.client().AdminListPeers(context.Background(), "tok", false, false); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers" {
		t.Errorf("peers bare path = %q", f.lastPath)
	}
	// ?all=1 surfaces suspended peers, marked by presence of suspended_at (verified live).
	f.reply = `{"peers":[{"handle":"@peerb","public_key":"KEY","available":0,"locked":0,"suspended_at":"2026-07-19T17:50:24Z"}]}`
	dp, err := f.client().AdminListPeers(context.Background(), "tok", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dp.Peers) != 1 || !dp.Peers[0].Suspended() {
		t.Errorf("suspended peer must decode suspended_at and report Suspended(): %+v", dp.Peers)
	}
	// An active row (no suspended_at) is not suspended.
	if (Peer{Handle: "@x"}).Suspended() {
		t.Error("a peer without suspended_at must not be Suspended()")
	}
	f.reply = `{"handle":"@k","public_key":"PK","addrs":["/ip4/127.0.0.1/tcp/1"]}`
	id, err := f.client().AdminIdentity(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if id.Handle != "@k" || id.PublicKey != "PK" || len(id.Addrs) != 1 {
		t.Errorf("identity decode wrong: %+v", id)
	}
}

func TestAdminSubscribeUnsubscribeInspect(t *testing.T) {
	f := newFake()
	defer f.close()
	if err := f.client().AdminSubscribe(context.Background(), "tok", "PKEY"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers/subscribe" || !strings.Contains(string(f.lastBody), `"key":"PKEY"`) {
		t.Errorf("subscribe request = %s body=%s", f.lastPath, f.lastBody)
	}
	if err := f.client().AdminUnsubscribe(context.Background(), "tok", "@peerb"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers/unsubscribe" || !strings.Contains(string(f.lastBody), `"handle":"@peerb"`) {
		t.Errorf("unsubscribe request = %s body=%s", f.lastPath, f.lastBody)
	}
	// Peer suspend uses the unified user route with the RAW key (no @ prefix).
	if err := f.client().AdminSuspendPeer(context.Background(), "tok", "PKEY"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/PKEY/suspend" {
		t.Errorf("peer suspend path = %q (key must not be @-normalized)", f.lastPath)
	}
	// online/source are TOP-LEVEL fields of the inspection; reachability holds the
	// transport diagnostics (path/rtt/protocols/error). Decoding online from inside
	// reachability is the always-offline bug this guards against.
	f.reply = `{"handle":"@k","public_key":"PKEY","online":true,"source":"live",
	 "reachability":{"path":"direct","rtt_millis":7,"protocols":["/juice/fed/gossip/1"],"error":""}}`
	insp, err := f.client().AdminInspect(context.Background(), "tok", "PKEY")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || f.lastPath != "/control/peers/inspect?key=PKEY" {
		t.Errorf("inspect request = %s %s", f.lastMethod, f.lastPath)
	}
	if !insp.Online || insp.Source != "live" {
		t.Errorf("top-level online/source not decoded: online=%v source=%q", insp.Online, insp.Source)
	}
	if insp.Reachability.Path != "direct" || insp.Reachability.RTTMillis != 7 {
		t.Errorf("reachability diagnostics not decoded: %+v", insp.Reachability)
	}
	// An unreachable peer carries a reason in reachability.error.
	f.reply = `{"handle":"@k","public_key":"PKEY","online":false,"source":"none",
	 "reachability":{"path":"unreachable","error":"routing: not found"}}`
	insp2, _ := f.client().AdminInspect(context.Background(), "tok", "PKEY")
	if insp2.Online || insp2.Reachability.Error == "" {
		t.Errorf("unreachable inspection must decode online=false + error: %+v", insp2)
	}
}
