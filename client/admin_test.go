package client

import (
	"context"
	"net/http"
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
	// The merged roster: a counterparty (has_account) beside a discovered kernel
	// that carries a catalog and a last_seen but no account — shapes captured live
	// 2026-08-06 on three throwaway kernels.
	f.reply = `[{"petname":"peerb","nickname":"twin","public_key":"KEY","has_account":true,"actions":13,"available":10,"locked":0,"peer_credit":250,"last_seen":"2026-07-12T00:00:00Z"},
	 {"nickname":"twin","public_key":"KEY2","has_account":false,"actions":7,"available":0,"locked":0,"last_seen":"2026-07-13T00:00:00Z"}]`
	p, err := f.client().AdminListPeers(context.Background(), "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.lastPath, "/control/peers?") || !strings.Contains(f.lastPath, "limit=200") {
		t.Errorf("peers path = %q (the roster is unbounded and must page)", f.lastPath)
	}
	if len(p) != 2 || p[0].Petname != "peerb" {
		t.Errorf("peers decode wrong: %+v", p)
	}
	// Only the petname resolves; the nickname is the kernel's own claim and may
	// repeat across distinct keys, so it is decoded but never used to address.
	if p[0].Nickname != "twin" || p[1].Nickname != "twin" || p[1].Petname != "" {
		t.Errorf("petname/nickname decode wrong: %+v", p)
	}
	// The two classes, and the advertised name repeating across distinct keys.
	if !p[0].HasAccount || p[0].Actions != 13 || p[1].HasAccount || p[1].Actions != 7 {
		t.Errorf("has_account/actions decode wrong: %+v", p)
	}
	if p[0].PublicKey == p[1].PublicKey {
		t.Error("two rows sharing an advertised handle must stay key-distinct")
	}
	// The §13 peer-sync cache — our credit on the peer and when last seen.
	if p[0].PeerCredit != 250 || p[0].LastSeen != "2026-07-12T00:00:00Z" {
		t.Errorf("peer sync-cache decode wrong: credit=%d last_seen=%q", p[0].PeerCredit, p[0].LastSeen)
	}
	// A discovered row carries catalog freshness but no credit: absent, not zero.
	if p[1].LastSeen == "" || p[1].PeerCredit != 0 {
		t.Errorf("discovered row sync fields wrong: %+v", p[1])
	}
	// all → the flag is emitted as literal "1" (the kernel reads "true" as false).
	if _, err := f.client().AdminListPeers(context.Background(), "tok", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastPath, "all=1") {
		t.Errorf("peers all path = %q", f.lastPath)
	}
	// ?all=1 surfaces suspended peers, marked by presence of suspended_at (verified live).
	f.reply = `[{"handle":"peerb","public_key":"KEY","available":0,"locked":0,"suspended_at":"2026-07-19T17:50:24Z"}]`
	dp, err := f.client().AdminListPeers(context.Background(), "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dp) != 1 || !dp[0].Suspended() {
		t.Errorf("suspended peer must decode suspended_at and report Suspended(): %+v", dp)
	}
	// An active row (no suspended_at) is not suspended.
	if (Peer{Petname: "x"}).Suspended() {
		t.Error("a peer without suspended_at must not be Suspended()")
	}
	f.reply = `{"handle":"k","public_key":"PK","about":"my kernel","addrs":["/ip4/127.0.0.1/tcp/1"]}`
	id, err := f.client().AdminIdentity(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if id.Handle != "k" || id.PublicKey != "PK" || id.About != "my kernel" || len(id.Addrs) != 1 {
		t.Errorf("identity decode wrong: %+v", id)
	}
}

// The roster is bounded by the size of the known network, not by trade, so the
// client pages to exhaustion: a full page must be followed by the next offset.
func TestAdminListPeersPagesToExhaustion(t *testing.T) {
	f := newFake()
	defer f.close()
	var paths []string
	f.replyFn = func(r *http.Request) string {
		paths = append(paths, r.URL.RequestURI())
		if strings.Contains(r.URL.RawQuery, "offset=200") {
			return `[{"petname":"tail","public_key":"K2"}]`
		}
		rows := make([]string, 200)
		for i := range rows {
			rows[i] = `{"petname":"p","public_key":"K1"}`
		}
		return "[" + strings.Join(rows, ",") + "]"
	}
	p, err := f.client().AdminListPeers(context.Background(), "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 201 || p[200].Petname != "tail" {
		t.Errorf("paging lost rows: got %d", len(p))
	}
	if len(paths) != 2 || !strings.Contains(paths[1], "offset=200") {
		t.Errorf("second page not requested: %v", paths)
	}
}

func TestAdminInspectAndPeerSuspend(t *testing.T) {
	f := newFake()
	defer f.close()
	// Peer suspend uses the unified user route with the RAW key (no @ prefix).
	if err := f.client().AdminSuspendPeer(context.Background(), "tok", "PKEY"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/PKEY/suspend" {
		t.Errorf("peer suspend path = %q (key must not be @-normalized)", f.lastPath)
	}
	// online/source are TOP-LEVEL fields of the inspection; reachability holds the
	// transport diagnostics (path/rtt/protocols/error). Decoding online from inside
	// reachability is the always-offline bug this guards against. As of v0.12.8 an
	// action row is {name, description, price} only and the account arrives inline
	// — shapes captured live 2026-08-06.
	f.reply = `{"handle":"k","public_key":"PKEY","about":"a test kernel","online":true,"source":"live",
	 "actions":[{"name":"greet","price":12,"description":"Greets a person."}],
	 "account":{"available":5,"locked":0,"suspended":false},
	 "evidence":[{"issuer_public_key":"ISS1","subject_action_id":"A1","uses":2,"successes":0,"failures":2,
	   "corroborated_uses":1,"avg_latency_ms":90760.5,"rating_count":0,"rating_mean":0,"unverified_ratings":0}],
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
	if len(insp.Actions) != 1 || insp.Actions[0].Name != "greet" || insp.Actions[0].Price != 12 {
		t.Errorf("peer action not decoded: %+v", insp.Actions)
	}
	// The account arrives inline, so the page needs no roster scan to state a position.
	if insp.Account == nil || insp.Account.Available != 5 || insp.Discovered() {
		t.Errorf("account not decoded, or a counterparty read as discovered: %+v", insp.Account)
	}
	// avg_latency_ms is FRACTIONAL on the wire (690.5, 90760.5 — verified live).
	// Decoding it into an integer field failed the whole inspection and rendered
	// the generic error page, so it is not decoded at all; this fixture keeps a
	// fractional value so re-adding it as an int fails here.
	if len(insp.Evidence) != 1 || insp.Evidence[0].IssuerPublicKey != "ISS1" ||
		insp.Evidence[0].Failures != 2 || insp.Evidence[0].CorroboratedUses != 1 {
		t.Errorf("evidence not decoded: %+v", insp.Evidence)
	}
	// An unreachable peer carries a reason in reachability.error. A well-formed but
	// wholly unknown key answers source="none" with no account (verified live) — a
	// stranger, NOT part of the known network.
	f.reply = `{"handle":"k","public_key":"PKEY","online":false,"source":"none","account":null,
	 "reachability":{"path":"unreachable","error":"routing: not found"}}`
	insp2, _ := f.client().AdminInspect(context.Background(), "tok", "PKEY")
	if insp2.Online || insp2.Reachability.Error == "" {
		t.Errorf("unreachable inspection must decode online=false + error: %+v", insp2)
	}
	if insp2.Discovered() {
		t.Error(`source="none" must not read as discovered (positive predicate on source)`)
	}
	// A known kernel with no account here IS discovered — live or last-known alike.
	for _, src := range []string{"live", "local"} {
		if !(Inspection{Source: src}).Discovered() {
			t.Errorf("account-less source=%q must read as discovered", src)
		}
	}
}

// Evidence splits into two views that are never folded: the subject's own
// execution rows, and every other issuer's experience of it, grouped in wire
// order. Both legs were reproduced live on one subject action (2026-08-06).
func TestEvidenceViews(t *testing.T) {
	insp := Inspection{
		PublicKey: "SUBJ",
		Evidence: []Evidence{
			{IssuerPublicKey: "SUBJ", SubjectActionID: "A1", Uses: 3},
			{IssuerPublicKey: "ISS1", SubjectActionID: "A1", Uses: 2, CorroboratedUses: 1, RatingCount: 1, RatingMean: 1},
			{IssuerPublicKey: "ISS2", SubjectActionID: "A1", Uses: 5, UnverifiedRating: 3},
			{IssuerPublicKey: "ISS1", SubjectActionID: "A2", Uses: 1},
		},
	}
	self := insp.SelfEvidence()
	if !self.Self || len(self.Rows) != 1 || self.Rows[0].Uses != 3 {
		t.Errorf("execution summary must be the subject's own rows alone: %+v", self)
	}
	got := insp.CounterpartyEvidence()
	if len(got) != 2 || got[0].Issuer != "ISS1" || got[1].Issuer != "ISS2" {
		t.Fatalf("grouping wrong (wire order, subject excluded): %+v", got)
	}
	if len(got[0].Rows) != 2 || got[0].Rows[0].CorroboratedUses != 1 {
		t.Errorf("issuer rows or corroborated count lost: %+v", got[0].Rows)
	}
	if got[1].Rows[0].UnverifiedRating != 3 {
		t.Errorf("unverified count lost: %+v", got[1].Rows[0])
	}
	// No evidence → no rows either side (the template shows its own empty state).
	if g := (Inspection{}).CounterpartyEvidence(); g != nil {
		t.Errorf("empty evidence must group to nil, got %+v", g)
	}
	if s := (Inspection{}).SelfEvidence(); s.Rows != nil {
		t.Errorf("empty evidence must yield no self rows, got %+v", s)
	}
}

// v0.12: the bilateral position may be negative and carries the Y-trigger flag;
// /control/identity carries the global exposure figures (verified live 2026-08-03).
func TestAdminPeersBilateralAndExposure(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"handle":"peerb","public_key":"KEY","available":-320,"locked":0,"settlement_due":true}]`
	p, err := f.client().AdminListPeers(context.Background(), "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if p[0].Available != -320 || !p[0].SettlementDue {
		t.Errorf("bilateral decode wrong: %+v", p[0])
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

// The client never rewrites a handle: "@" now separates a kernel, so a sigiled
// handle is ill-formed input the kernel rejects (verified live 2026-08-05), not
// a spelling of the bare one. Passing it through verbatim is what makes the
// browser and a headless call answer alike (requirements.md §4).
func TestHandleIsSentVerbatim(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","handle":"alice"}`
	if _, err := f.client().AdminGetUser(context.Background(), "tok", "@alice"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/users/@alice" {
		t.Errorf("handle must reach the kernel verbatim: %q", f.lastPath)
	}
}

// A peer is provisioned by funding its key: the deposit body must carry the raw
// base64url key untouched, since the kernel disambiguates handle / key / id by
// shape and an @-normalized key would name nothing.
func TestAdminDepositByPeerKey(t *testing.T) {
	f := newFake()
	defer f.close()
	const key = "tETeglMY_o_yysBZ7oa2wc4-yHDRKw5uHruHcQZT2cA"
	f.reply = `{"direction":"credit","amount":100,"operator_handle":"sys","target_handle":"k-tETeglMY"}`
	if _, err := f.client().AdminDeposit(context.Background(), "tok", key, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.lastBody), `"handle":"`+key+`"`) {
		t.Errorf("peer key must reach the kernel unchanged: %s", f.lastBody)
	}
}
