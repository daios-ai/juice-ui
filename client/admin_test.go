package client

import (
	"context"
	"strings"
	"testing"
)

func TestAdminListUsers(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"id":"U1","handle":"alice","email":"a@x","available":230,"locked":0}]`
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
	if len(users) != 1 || users[0].Handle != "alice" || users[0].Available != 230 {
		t.Errorf("decoded users wrong: %+v", users)
	}
}

func TestAdminGetUserAndSuspended(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","handle":"alice","suspended_at":"2026-07-05T00:00:00Z"}`
	u, err := f.client().AdminGetUser(context.Background(), "tok", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/alice" {
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
	if err := f.client().AdminSuspendUser(context.Background(), "tok", "bob"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/control/users/bob/suspend" {
		t.Errorf("suspend request = %s %s", f.lastMethod, f.lastPath)
	}
	if err := f.client().AdminUnsuspendUser(context.Background(), "tok", "bob"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/bob/unsuspend" {
		t.Errorf("unsuspend path = %q", f.lastPath)
	}
}

func TestAdminDepositWithdraw(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"direction":"credit","amount":100,"operator_handle":"sys","target_handle":"alice"}`
	adj, err := f.client().AdminDeposit(context.Background(), "tok", "alice", 100, "grant", "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/control/deposit" {
		t.Errorf("deposit request = %s %s", f.lastMethod, f.lastPath)
	}
	body := string(f.lastBody)
	for _, want := range []string{`"handle":"alice"`, `"amount":100`, `"reason":"grant"`, `"external_key":"idem-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("deposit body missing %s: %s", want, body)
		}
	}
	if adj.Direction != "credit" || adj.Amount != 100 {
		t.Errorf("adjustment decode wrong: %+v", adj)
	}
	// Withdraw hits its own path; empty reason/key are omitted.
	if _, err := f.client().AdminWithdraw(context.Background(), "tok", "alice", 5, "", ""); err != nil {
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
	f.reply = `{"peers":[{"handle":"peerb","public_key":"KEY","available":10,"locked":0,"peer_credit":250,"last_seen":"2026-07-12T00:00:00Z"}]}`
	p, err := f.client().AdminListPeers(context.Background(), "tok", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers?gossip=1" {
		t.Errorf("peers path = %q", f.lastPath)
	}
	if len(p.Peers) != 1 || p.Peers[0].Handle != "peerb" {
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
	f.reply = `{"peers":[{"handle":"peerb","public_key":"KEY","available":0,"locked":0,"suspended_at":"2026-07-19T17:50:24Z"}]}`
	dp, err := f.client().AdminListPeers(context.Background(), "tok", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dp.Peers) != 1 || !dp.Peers[0].Suspended() {
		t.Errorf("suspended peer must decode suspended_at and report Suspended(): %+v", dp.Peers)
	}
	// An active row (no suspended_at) is not suspended.
	if (Peer{Handle: "x"}).Suspended() {
		t.Error("a peer without suspended_at must not be Suspended()")
	}
	f.reply = `{"handle":"k","public_key":"PK","addrs":["/ip4/127.0.0.1/tcp/1"]}`
	id, err := f.client().AdminIdentity(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if id.Handle != "k" || id.PublicKey != "PK" || len(id.Addrs) != 1 {
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
	if err := f.client().AdminUnsubscribe(context.Background(), "tok", "peerb"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers/unsubscribe" || !strings.Contains(string(f.lastBody), `"handle":"peerb"`) {
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
	f.reply = `{"handle":"k","public_key":"PKEY","online":true,"source":"live",
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
	f.reply = `{"handle":"k","public_key":"PKEY","online":false,"source":"none",
	 "reachability":{"path":"unreachable","error":"routing: not found"}}`
	insp2, _ := f.client().AdminInspect(context.Background(), "tok", "PKEY")
	if insp2.Online || insp2.Reachability.Error == "" {
		t.Errorf("unreachable inspection must decode online=false + error: %+v", insp2)
	}
}

// v0.12: the bilateral position may be negative and carries the Y-trigger flag;
// /control/identity carries the global exposure figures (verified live 2026-08-03).
func TestAdminPeersBilateralAndExposure(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"peers":[{"handle":"peerb","public_key":"KEY","available":-320,"locked":0,"settlement_due":true}]}`
	p, err := f.client().AdminListPeers(context.Background(), "tok", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Peers[0].Position() != -320 || !p.Peers[0].SettlementDue {
		t.Errorf("bilateral decode wrong: %+v", p.Peers[0])
	}
	f.reply = `{"handle":"k","public_key":"PK","addrs":[],"exposure_max":1000,"gross_receivables":620,
	 "settlement_trigger":500,"settlement_quantum":3000,"settlement_due":true}`
	id, err := f.client().AdminIdentity(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if id.ExposureMax != 1000 || id.GrossReceivables != 620 || id.SettlementTrigger != 500 ||
		id.SettlementQuantum != 3000 || !id.SettlementDue {
		t.Errorf("exposure decode wrong: %+v", id)
	}
}

// v0.12: the value-transfer queue — list (status filter), show, and retry (the
// only mutation, POST .../retry).
func TestAdminTransfers(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"id":"T1","status":"quarantined","amount":300,"created_at":"2026-08-01T00:00:00Z"}]`
	ts, err := f.client().AdminListTransfers(context.Background(), "tok", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || f.lastPath != "/control/transfers" {
		t.Errorf("transfers list = %s %s", f.lastMethod, f.lastPath)
	}
	if len(ts) != 1 || ts[0].Status != "quarantined" || ts[0].Amount != 300 {
		t.Errorf("transfers decode wrong: %+v", ts)
	}
	if _, err := f.client().AdminListTransfers(context.Background(), "tok", "settled", 20, 20); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/transfers?limit=20&offset=20&status=settled" {
		t.Errorf("transfers filter path = %q", f.lastPath)
	}
	f.reply = `{"id":"T1","status":"pending","created_at":"2026-08-01T00:00:00Z"}`
	tr, err := f.client().AdminGetTransfer(context.Background(), "tok", "T1")
	if err != nil || tr.ID != "T1" {
		t.Fatalf("get transfer = %+v %v", tr, err)
	}
	if f.lastPath != "/control/transfers/T1" {
		t.Errorf("get transfer path = %q", f.lastPath)
	}
	if _, err := f.client().AdminRetryTransfer(context.Background(), "tok", "T1"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/control/transfers/T1/retry" {
		t.Errorf("retry = %s %s", f.lastMethod, f.lastPath)
	}
}

// v0.12: settlement over HTTP — POST /control/peers/settle {handle}, and the
// --cash leg with settlement_id (route + body verified live 2026-08-03).
func TestAdminSettle(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"handle":"peerb","settlement_id":"SID1","mode":"exact","status":"exact","amount":700,"message":"pay 700 on the rail"}`
	r, err := f.client().AdminSettle(context.Background(), "tok", "peerb", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/control/peers/settle" {
		t.Errorf("settle = %s %s", f.lastMethod, f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"handle":"peerb"`) || strings.Contains(string(f.lastBody), "settlement_id") {
		t.Errorf("settle body = %s", f.lastBody)
	}
	if r.SettlementID != "SID1" || r.Mode != "exact" || r.Message == "" {
		t.Errorf("settle result = %+v", r)
	}
	if _, err := f.client().AdminSettle(context.Background(), "tok", "peerb", "SID1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.lastBody), `"settlement_id":"SID1"`) {
		t.Errorf("cash body = %s", f.lastBody)
	}
}

// Migration tolerance: a sigiled handle from a form or a pre-0.4 session still
// reaches the kernel bare (requirements.md §4).
func TestAdminHandleSigilTolerated(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","handle":"alice"}`
	if _, err := f.client().AdminGetUser(context.Background(), "tok", "@alice"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/alice" {
		t.Errorf("sigiled input must canonicalize bare: %q", f.lastPath)
	}
}
