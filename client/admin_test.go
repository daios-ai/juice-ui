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
	if f.lastMethod != "GET" || f.lastPath != "/control/users" {
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
		t.Error("non-zero suspended_at must report Suspended()")
	}
	// A zero timestamp is not suspended.
	if (AdminUser{SuspendedAt: "0001-01-01T00:00:00Z"}).Suspended() {
		t.Error("zero suspended_at must not report suspended")
	}
	if (AdminUser{}).Suspended() {
		t.Error("empty suspended_at must not report suspended")
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
	p, err := f.client().AdminListPeers(context.Background(), "tok", true)
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
	f.reply = `{"handle":"@k","public_key":"PK","addrs":["/ip4/127.0.0.1/tcp/1"]}`
	id, err := f.client().AdminIdentity(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if id.Handle != "@k" || id.PublicKey != "PK" || len(id.Addrs) != 1 {
		t.Errorf("identity decode wrong: %+v", id)
	}
}

func TestAdminFriendUnfriendInspect(t *testing.T) {
	f := newFake()
	defer f.close()
	if err := f.client().AdminFriend(context.Background(), "tok", "PKEY"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers/friend" || !strings.Contains(string(f.lastBody), `"key":"PKEY"`) {
		t.Errorf("friend request = %s body=%s", f.lastPath, f.lastBody)
	}
	if err := f.client().AdminUnfriend(context.Background(), "tok", "@peerb"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/control/peers/unfriend" || !strings.Contains(string(f.lastBody), `"handle":"@peerb"`) {
		t.Errorf("unfriend request = %s body=%s", f.lastPath, f.lastBody)
	}
	f.reply = `{"ok":true}`
	if _, err := f.client().AdminInspect(context.Background(), "tok", "PKEY"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || f.lastPath != "/control/peers/inspect?key=PKEY" {
		t.Errorf("inspect request = %s %s", f.lastMethod, f.lastPath)
	}
}
