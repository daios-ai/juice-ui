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
	f.reply = `[{"id":"U1","address":"alice@k","available":230,"locked":0}]`
	users, err := f.client().AdminListUsers(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || !strings.HasPrefix(f.lastPath, "/v1/admin/users?") || !strings.Contains(f.lastPath, "limit=200") {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
	if f.lastAuth != "Bearer tok" {
		t.Errorf("missing bearer, got %q", f.lastAuth)
	}
	if len(users) != 1 || users[0].Address != "alice@k" || users[0].Available != 230 {
		t.Errorf("decoded users wrong: %+v", users)
	}
}

func TestAdminGetUserAndSuspended(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","address":"alice@k","suspended_at":"2026-07-05T00:00:00Z"}`
	u, err := f.client().AdminGetUser(context.Background(), "tok", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/admin/users/alice" {
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
	if f.lastMethod != "POST" || f.lastPath != "/v1/admin/users/bob/suspend" {
		t.Errorf("suspend request = %s %s", f.lastMethod, f.lastPath)
	}
	if err := f.client().AdminUnsuspendUser(context.Background(), "tok", "bob"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/admin/users/bob/unsuspend" {
		t.Errorf("unsuspend path = %q", f.lastPath)
	}
}

func TestAdminDepositNamesThePayment(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"amount":100,"external_key":"attr:rail:ref:pay-1","operator":"sys","from":"sys","to":"alice"}`
	e, err := f.client().AdminDeposit(context.Background(), "tok", "alice", 100, "pay-1", "grant")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/v1/admin/users/alice/deposit" {
		t.Errorf("deposit request = %s %s", f.lastMethod, f.lastPath)
	}
	body := string(f.lastBody)
	// The account is in the PATH now, so it is not in the body at all.
	if strings.Contains(body, `"handle"`) {
		t.Errorf("the account is named by the route, not the body: %s", body)
	}
	for _, want := range []string{`"amount":100`, `"ref":"pay-1"`, `"reason":"grant"`} {
		if !strings.Contains(body, want) {
			t.Errorf("deposit body missing %s: %s", want, body)
		}
	}
	if e.Amount != 100 || e.To != "alice" {
		t.Errorf("the deposit answers with the ledger entry it wrote: %+v", e)
	}
	// The amount-less form — attributing a payment already received whose sender
	// was unknown — sends no amount at all, so the kernel credits what the
	// payment actually carried rather than a figure we made up.
	if _, err := f.client().AdminDeposit(context.Background(), "tok", "alice", 0, "rail:0xabc:0", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), "amount") || strings.Contains(string(f.lastBody), "reason") {
		t.Errorf("a zero amount and an empty reason are omitted: %s", f.lastBody)
	}
	if !strings.Contains(string(f.lastBody), `"ref":"rail:0xabc:0"`) {
		t.Errorf("the held payment's own id is what attributes it: %s", f.lastBody)
	}
}

// The money awaiting the operator: payments nobody has claimed, and obligations
// a buyer says it paid whose money has not arrived (§6.6).
func TestAdminListDeposits(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"deposits":[],"owed":[{"id":"TICKET1","obligation":8,"amount":10,"tx_hash":"manual:TICKET1",
	 "status":"announced","created_at":"2026-09-09T09:30:24Z","peer":"KEYB"},
	 {"id":"TICKET2","obligation":126,"amount":0,"tx_hash":"","status":"","created_at":"2026-09-14T01:13:39Z","peer":"KEYD"}]}`
	d, err := f.client().AdminListDeposits(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/admin/kernel/deposits" {
		t.Errorf("deposits path = %q", f.lastPath)
	}
	if len(d.Owed) != 2 || d.Owed[0].ID != "TICKET1" || d.Owed[0].Peer != "KEYB" {
		t.Errorf("obligation decode wrong: %+v", d.Owed)
	}
	// What was owed and what must be paid are different numbers: the draw pays a
	// larger amount with the probability that makes the expectation the debt.
	if d.Owed[0].Obligation != 8 || d.Owed[0].Amount != 10 {
		t.Errorf("the draw's amount and the debt are both carried: %+v", d.Owed[0])
	}
	// Answered separates a row the buyer has spoken for from one it has not. The
	// second row's empty status and zero amount are ABSENT fields, and a caller
	// that read them as values would state an amount nobody has decided
	// (docs/kernel-v0.15-verification.md §3).
	if !d.Owed[0].Answered() {
		t.Error("a row with a status is answered")
	}
	if d.Owed[1].Answered() {
		t.Errorf("an empty status means the buyer has said nothing: %+v", d.Owed[1])
	}
}

func TestAdminPeersAndIdentity(t *testing.T) {
	f := newFake()
	defer f.close()
	// The merged roster: a counterparty (has_account) beside a discovered kernel
	// that carries a catalog and a last_seen but no account — shapes captured live
	// 2026-08-06 on three throwaway kernels.
	f.reply = `[{"petname":"peerb","nickname":"twin","public_key":"KEY","has_account":true,"actions":13,"last_seen":"2026-07-12T00:00:00Z"},
	 {"nickname":"twin","public_key":"KEY2","has_account":false,"actions":7,"last_seen":"2026-07-13T00:00:00Z"}]`
	p, err := f.client().AdminListPeers(context.Background(), "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.lastPath, "/v1/admin/peers?") || !strings.Contains(f.lastPath, "limit=200") {
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
	// The contact cache — when a verified exchange last succeeded. Both classes
	// carry it; neither carries money.
	if p[0].LastSeen != "2026-07-12T00:00:00Z" || p[1].LastSeen == "" {
		t.Errorf("contact cache decode wrong: %+v", p)
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

// A peer's record is read by KEY on the peer route (never the users route) and
// dates a counterparty's account; shape verified live 2026-10-08 (v0.15.6).
func TestAdminPeerRecord(t *testing.T) {
	f := newFake()
	defer f.close()
	f.replyFn = func(r *http.Request) string {
		return `{"about":"","blockchain_address":"","created_at":"2026-09-23T13:38:52Z","last_seen":"2026-10-07T19:22:58Z","nickname":"arfante","petname":"arfante","public_key":"PKEY","suspended_at":null}`
	}
	rec, err := f.client().AdminPeer(context.Background(), "tok", "PKEY")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/admin/peers/PKEY" {
		t.Errorf("peer record path = %q", f.lastPath)
	}
	if rec.CreatedAt != "2026-09-23T13:38:52Z" || rec.Petname != "arfante" {
		t.Errorf("record = %+v", rec)
	}
}

func TestAdminInspectAndPeerSuspend(t *testing.T) {
	f := newFake()
	defer f.close()
	// Peer verbs live in the PEER namespace: the kernel refuses a peer target on
	// the users route and vice versa, so the client may never mix them. The key
	// travels raw (no @ prefix).
	if err := f.client().AdminSuspendPeer(context.Background(), "tok", "PKEY"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/admin/peers/PKEY/suspend" {
		t.Errorf("peer suspend path = %q (key must not be @-normalized)", f.lastPath)
	}
	if err := f.client().AdminRenamePeer(context.Background(), "tok", "PKEY", "twin"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/v1/admin/peers/PKEY/rename" || !strings.Contains(string(f.lastBody), `"new_name":"twin"`) {
		t.Errorf("peer rename = %s %s", f.lastPath, f.lastBody)
	}
	// online/source are TOP-LEVEL fields of the inspection; reachability holds the
	// transport diagnostics (path/rtt/protocols/error). Decoding online from inside
	// reachability is the always-offline bug this guards against. As of v0.12.8 an
	// action row is {name, description, price} only and the account arrives inline
	// — shapes captured live 2026-08-06.
	f.reply = `{"handle":"k","public_key":"PKEY","about":"a test kernel","online":true,"source":"live",
	 "actions":[{"name":"seller@bee/greet","title":"Greet a person","price":12,"description":"Greets a person."}],
	 "account":{"available":5,"locked":0,"suspended":false},
	 "evidence":[{"issuer_public_key":"ISS1","subject_action_id":"A1","uses":2,"successes":0,"failures":2,
	   "corroborated_uses":1,"avg_latency_ms":90760.5,"rating_count":0,"rating_mean":0,"unverified_ratings":0}],
	 "reachability":{"path":"direct","rtt_millis":7,"protocols":["/juice/fed/gossip/1"],"version":"juice-kernel/dev","error":""}}`
	insp, err := f.client().AdminInspect(context.Background(), "tok", "PKEY")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || f.lastPath != "/v1/admin/peers/PKEY/inspect" {
		t.Errorf("inspect request = %s %s", f.lastMethod, f.lastPath)
	}
	if !insp.Online || insp.Source != "live" {
		t.Errorf("top-level online/source not decoded: online=%v source=%q", insp.Online, insp.Source)
	}
	if insp.Reachability.Path != "direct" || insp.Reachability.RTTMillis != 7 || insp.Reachability.Version != "juice-kernel/dev" {
		t.Errorf("reachability diagnostics not decoded: %+v", insp.Reachability)
	}
	if len(insp.Actions) != 1 || insp.Actions[0].Name != "seller@bee/greet" || insp.Actions[0].Title != "Greet a person" || insp.Actions[0].Price != 12 {
		t.Errorf("peer action not decoded: %+v", insp.Actions)
	}
	// The account arrives inline, so the page needs no roster scan. It is
	// moderation state, never a balance: a peer holds no money (kernel §D14).
	if insp.Account == nil || insp.Account.Suspended {
		t.Errorf("account not decoded: %+v", insp.Account)
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
	// wholly unknown key answers source="none" with no account (verified live).
	f.reply = `{"handle":"k","public_key":"PKEY","online":false,"source":"none","account":null,
	 "reachability":{"path":"unreachable","error":"routing: not found"}}`
	insp2, _ := f.client().AdminInspect(context.Background(), "tok", "PKEY")
	if insp2.Online || insp2.Reachability.Error == "" {
		t.Errorf("unreachable inspection must decode online=false + error: %+v", insp2)
	}
	if insp2.Source != "none" || insp2.Account != nil {
		t.Errorf("a stranger key must decode source=none with no account: %+v", insp2)
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

// A peer row carries no money, and /control/identity carries this kernel's own
// credit position, money rules and rail standing (verified live on v0.14.0).
func TestIdentityCarriesTheCreditAndRailPosition(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"handle":"k","public_key":"PK","addrs":[],"network":"play","network_fingerprint":"ef1f",
	 "blockchain_address":"","finalized":null,
	 "sys":{"earnings":170,"pending_payouts":0,"held_deposits":0,"refill_locks":0},
	 "solvency":{"liabilities":8126,"vault":8126,"gap":0},
	 "custody":{"checked":false,"difference":0,"ok":false},
	 "lottery":10,"credit_limit":1000,"exposure":320,
	 "fee_bps":2000,"remote_bps":500,"import_bps":500}`
	id, err := f.client().AdminIdentity(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if id.Exposure != 320 || id.CreditLimit != 1000 || id.Lottery != 10 {
		t.Errorf("credit position decode wrong: %+v", id)
	}
	if id.FeeBps != 2000 || id.RemoteBps != 500 || id.ImportBps != 500 {
		t.Errorf("money rules decode wrong: %+v", id)
	}
	if id.Sys.Earnings != 170 || id.Solvency.Liabilities != 8126 || id.Solvency.Gap != 0 {
		t.Errorf("operator split or solvency decode wrong: %+v", id)
	}
	// Absent while nothing is wrong: a halt and a chain reading are both optional.
	if id.Stop != nil || id.Finalized != nil || id.Custody.Checked {
		t.Errorf("optional rail state must stay absent: %+v", id)
	}
}

// The client never rewrites a name: an address reaches the kernel's path as
// written, its "@" unescaped — the kernel refuses "%40" in a path (verified
// live 2026-09-26) — and a bare handle is passed through for the kernel to
// refuse, so the browser and a headless call answer alike (requirements.md §4).
func TestAddressIsSentVerbatim(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","address":"alice@k"}`
	for _, name := range []string{"alice@k", "alice"} {
		if _, err := f.client().AdminGetUser(context.Background(), "tok", name); err != nil {
			t.Fatal(err)
		}
		if f.lastPath != "/v1/admin/users/"+name {
			t.Errorf("%q must reach the kernel verbatim: %q", name, f.lastPath)
		}
	}
}

// A deposit credits a user and only a user. The kernel serves it under the
// users namespace, where a peer key is not a user — so there is no peer form to
// send, and a peer account is provisioned by its first signed call instead
// (docs/kernel-v0.15-verification.md §2).
func TestAdminDepositIsUserOnly(t *testing.T) {
	f := newFake()
	defer f.close()
	const key = "tETeglMY_o_yysBZ7oa2wc4-yHDRKw5uHruHcQZT2cA"
	f.status, f.reply = 404, `{"code":"not_found","error":"`+key+` is not a user here"}`
	_, err := f.client().AdminDeposit(context.Background(), "tok", key, 100, "r", "")
	if err == nil {
		t.Fatal("depositing to a peer key must fail")
	}
	if f.lastPath != "/v1/admin/users/"+key+"/deposit" {
		t.Errorf("the target reaches the kernel unchanged, in the path: %q", f.lastPath)
	}
}
