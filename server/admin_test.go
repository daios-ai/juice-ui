package server

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// loginClientAs logs in through the BFF with a chosen handle. The session handle
// is taken from the posted handle (not /v1/me), so this lets tests act as sys.
func loginClientAs(t *testing.T, base, handle string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Get(base + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := csrfFieldRe.FindStringSubmatch(string(body))
	if m == nil {
		t.Fatal("no csrf token on login page")
	}
	form := url.Values{"handle": {handle}, "password": {"pw"}, "gorilla.csrf.Token": {m[1]}}
	req, _ := http.NewRequest(http.MethodPost, base+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("login landed on status %d", resp2.StatusCode)
	}
	return cl
}

func TestAdminHiddenFromNonSuperuser(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	// alice is not the superuser → /admin is a 404 (not advertised).
	cl := loginClientAs(t, srv.URL, "alice")
	resp, err := cl.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-superuser /admin = %d want 404", resp.StatusCode)
	}

	// The nav must not show the admin group for alice.
	home := getBody(t, cl, srv.URL+"/browse")
	if strings.Contains(home, `href="/admin"`) {
		t.Error("non-superuser nav must not link to /admin")
	}
	// Nor should the palette registry carry admin commands.
	if strings.Contains(home, `"admin users"`) {
		t.Error("non-superuser palette registry must not include admin commands")
	}
}

func TestAdminVisibleToSuperuser(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin")
	if !strings.Contains(body, "Users") {
		t.Error("superuser /admin must render the Users table")
	}
	if !strings.Contains(body, "alice") {
		t.Error("superuser /admin must list users from /control/users")
	}
	// The user handle is a doorway to that user's admin page.
	if !strings.Contains(body, `href="/admin/users/alice"`) {
		t.Error("/admin user handle must link to the user's detail page")
	}
	// Admin and Peers are now separate top-level sidebar entries (not a sub-nav).
	if !strings.Contains(body, `href="/admin" title="admin"`) {
		t.Error("superuser sidebar must have an Admin nav entry")
	}
	if !strings.Contains(body, `href="/admin/peers" title="network"`) {
		t.Error("superuser sidebar must have a separate network nav entry")
	}
	if strings.Contains(body, "linkbar") {
		t.Error("/admin must not carry the retired users·peers sub-nav")
	}
	// Federation identity moved to the peers page — no "This kernel" on /admin.
	if strings.Contains(body, "This kernel") {
		t.Error("/admin must not show the 'This kernel' identity (it lives on /admin/peers)")
	}
	// Content-first: no floating command chrome; credits are quiet inline commands.
	if strings.Contains(body, `class="toolbar"`) || strings.Contains(body, "btn-primary") {
		t.Error("/admin must not carry a page-head toolbar or btn-primary")
	}
	for _, form := range []string{"admin-deposit", "admin-withdraw"} {
		if !strings.Contains(body, "?form="+form) {
			t.Errorf("/admin must offer the %q command inline", form)
		}
	}

	// The peers page carries "This kernel" and highlights the peers nav entry.
	pbody := getBody(t, cl, srv.URL+"/admin/peers")
	if !strings.Contains(pbody, "This kernel") {
		t.Error("/admin/peers must render the 'This kernel' identity card")
	}
	if !strings.Contains(pbody, `href="/admin/peers" title="network" aria-current="page"`) {
		t.Error("/admin/peers must mark the network nav entry active")
	}
}

func TestAdminPeersRendersGossipRoster(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	// Friend section.
	if !strings.Contains(body, "peerb") {
		t.Error("peers page must list the friend peerb")
	}
	// Known-network roster: the discovered kernel, the "you" (own) group, the
	// self-reported group, and the via-introducer group.
	for _, want := range []string{"kernelc", "Known network", "you", "self-reported", "via"} {
		if !strings.Contains(body, want) {
			t.Errorf("roster missing %q", want)
		}
	}
	// A non-subscribed roster kernel offers Inspect + Subscribe (prefilled with its key).
	if !strings.Contains(body, "/admin/peers/KEYC") {
		t.Error("roster kernel must link to its inspect page")
	}
	if !strings.Contains(body, `value="KEYC"`) {
		t.Error("non-subscribed roster kernel must offer Subscribe prefilled with its key")
	}
	// An already-subscribed roster kernel (peerb/KEYB) must NOT offer Subscribe in
	// the known-network roster (its Subscribe-confirm must be absent), and should be
	// marked as subscribed instead. (KEYB still appears in the subscriptions table's
	// own unsubscribe/suspend forms, so we key off the roster's confirm text.)
	if strings.Contains(body, "Subscribe to peerb") {
		t.Error("a roster kernel we already subscribe to must not offer Subscribe")
	}
	if !strings.Contains(body, ">subscribed</span>") {
		t.Error("an already-subscribed roster kernel must be marked subscribed")
	}
	// Stats are collapsed into <details> (name-only by default).
	if !strings.Contains(body, `class="card roster-kernel"`) || !strings.Contains(body, "<summary>") {
		t.Error("roster kernels must be collapsible <details> rows")
	}
	// The kernel sends `rating` as a 0–1 float; it must decode and render as a
	// rounded percentage (regression: it was int64 and 400'd the whole page).
	if !strings.Contains(body, "83%") {
		t.Errorf("fractional roster rating must render as a percentage: %s", body)
	}
}

// htmxGet issues a GET carrying the htmx request header, without following any
// redirect, so a test can inspect an HX-Redirect header or a bare fragment body.
func htmxGet(t *testing.T, cl *http.Client, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("HX-Request", "true")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestAdminPeersHTMXReturnsFragment checks item 1: an htmx poll of /admin/peers
// gets just the Friends-table fragment, not the whole page (no identity card, no
// layout chrome) — so the swap replaces only the polled region.
func TestAdminPeersHTMXReturnsFragment(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	resp := htmxGet(t, cl, srv.URL+"/admin/peers")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := string(body)
	if !strings.Contains(s, "peerb") {
		t.Errorf("htmx peers fragment must carry the friends table: %s", s)
	}
	if strings.Contains(s, "This kernel") || strings.Contains(s, `hx-get="/inbox/badge"`) {
		t.Errorf("htmx peers fragment must be bare (no identity card, no layout chrome): %s", s)
	}
}

// TestAdminInspectHTMXReturnsStatusFragment checks item 2: an htmx poll of an
// inspect page gets just the reachability status card.
func TestAdminInspectHTMXReturnsStatusFragment(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	resp := htmxGet(t, cl, srv.URL+"/admin/peers/KEYC")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := string(body)
	if !strings.Contains(s, "direct") {
		t.Errorf("htmx inspect fragment must carry reachability: %s", s)
	}
	if strings.Contains(s, "Transacted friends") || strings.Contains(s, `hx-get="/inbox/badge"`) {
		t.Errorf("htmx inspect fragment must be bare (status card only): %s", s)
	}
}

// TestAdminPollHTMXNonSuperuserRedirects checks the cross-cutting guard: a demoted
// session polling an admin fragment gets an HX-Redirect (real navigation), never a
// full 404 page swapped into the polled element.
func TestAdminPollHTMXNonSuperuserRedirects(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "alice")

	resp := htmxGet(t, cl, srv.URL+"/admin/peers")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("htmx non-superuser status = %d want 200 (HX-Redirect, not a page)", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got != "/" {
		t.Errorf("htmx non-superuser must get HX-Redirect / got %q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "Page not found") {
		t.Error("htmx non-superuser must not swap the 404 page body into the fragment")
	}
}

func TestAdminInspectRenders(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers/KEYC")
	// action ref renders through _ref (owner segment in its own span); the transport
	// path (direct) is folded into the single online status line.
	for _, want := range []string{"kernelc", "direct", `class="owner">sys`} {
		if !strings.Contains(body, want) {
			t.Errorf("inspect page missing %q", want)
		}
	}
	// online/source are top-level in the response; a reachable peer must show the
	// "online" pill (regression guard for the always-offline decode bug).
	if !strings.Contains(body, `class="pill status-credited">online`) {
		t.Errorf("reachable peer must render the online pill: %s", body)
	}
	if strings.Contains(body, `status-escrow">offline`) {
		t.Error("reachable peer must not render the offline pill")
	}
	// The redundant standalone "live" source and separate Reachability row are gone.
	if strings.Contains(body, "Reachability</dt>") {
		t.Error("the separate Reachability row should be folded into Status")
	}
	// A stranger (KEYC is not subscribed) offers Subscribe and carries no subscribed badge.
	if !strings.Contains(body, `action="/admin/peers/subscribe"`) || !strings.Contains(body, ">Subscribe<") {
		t.Error("a non-subscribed inspect must offer Subscribe")
	}
	if strings.Contains(body, ">subscribed</span>") {
		t.Error("a non-subscribed peer must not show the subscribed badge")
	}
}

// TestAdminInspectSubscribedShowsBadgeAndUnsubscribe checks the relationship UI: an
// already-subscribed kernel shows the subscribed badge in the header and offers the
// symmetric Unsubscribe action (not Subscribe).
func TestAdminInspectSubscribedShowsBadgeAndUnsubscribe(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	// KEYB is a peer in the fake /control/peers, so IsSubscribed is true.
	body := getBody(t, cl, srv.URL+"/admin/peers/KEYB")
	if !strings.Contains(body, ">subscribed</span>") {
		t.Error("a subscribed kernel must show the subscribed badge (in the header)")
	}
	if !strings.Contains(body, `action="/admin/peers/unsubscribe"`) || !strings.Contains(body, ">Unsubscribe<") {
		t.Error("a subscribed kernel must offer the symmetric Unsubscribe action")
	}
	if strings.Contains(body, ">Subscribe<") {
		t.Error("a subscribed peer must not offer Subscribe")
	}
}

func TestAdminDepositFlow(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")
	// Don't follow the redirect so we can assert on it.
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/admin"))
	form := url.Values{"handle": {"alice"}, "amount": {"100"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/deposit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin" {
		t.Errorf("deposit = %d loc=%q want 303 /admin", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestAdminDepositRejectsNonSuperuser(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "alice")

	// Even with a valid CSRF token, a non-superuser deposit is 404 (route gated).
	tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/browse"))
	form := url.Values{"handle": {"alice"}, "amount": {"100"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/deposit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-superuser deposit = %d want 404", resp.StatusCode)
	}
}

// v0.12 §6.6: the peers page renders the SIGNED bilateral position with its
// reading (never a bare negative), the settlement_due pill, the global exposure
// evidence on the kernel card, and the Settle affordance.
func TestAdminPeersBilateralPosition(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	if !strings.Contains(body, "owes us $0.320") {
		t.Errorf("negative position must read 'owes us', not a bare negative: %s", body)
	}
	if !strings.Contains(body, "we owe them $0.050") {
		t.Error("positive position must read 'we owe them' (earnings accrue on the row; 'prepaid' misreads it)")
	}
	if strings.Contains(body, "-$0.320") {
		t.Error("a bare negative amount must never render on the peers page")
	}
	if !strings.Contains(body, "settlement due") {
		t.Error("a settlement_due peer must carry the pill")
	}
	// Kernel card: exposure evidence from /control/identity.
	for _, want := range []string{"exposure", "$0.320", "$1.000", "trigger", "$0.500"} {
		if !strings.Contains(body, want) {
			t.Errorf("kernel card missing exposure evidence %q", want)
		}
	}
	// Settle is present and routed through a confirm (data-confirm) — external
	// money is never a bare click.
	if !strings.Contains(body, `action="/admin/peers/settle"`) || !strings.Contains(body, "Settle with") {
		t.Error("peer rows must offer Settle behind a confirm")
	}
	// The retired prepaid-credit gating copy must be gone.
	if strings.Contains(body, "Calls stay rejected until the peer holds credit") {
		t.Error("retired prepaid-gating copy must not render")
	}
}

// The settle POST flashes the kernel's own outcome message verbatim — the UI
// never computes or predicts a settlement outcome (§6.6).
func TestAdminSettleFlashesKernelMessage(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	page := getBody(t, cl, srv.URL+"/admin/peers")
	form := url.Values{"peer": {"debtor"}, "gorilla.csrf.Token": {csrfTokenFrom(t, page)}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/peers/settle", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "pay 320 on the rail") {
		t.Errorf("settle must flash the kernel's outcome message verbatim: %s", body)
	}
}

// v0.12 §6.6: the transfer queue — unresolved default, status filter, detail,
// and Retry as the ONLY mutation (no refund/force-settle affordance anywhere).
func TestAdminTransfersQueue(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/transfers")
	for _, want := range []string{"TQ1", "TP1", "quarantined", "pending", "Retry", "$0.300"} {
		if !strings.Contains(body, want) {
			t.Errorf("transfers list missing %q", want)
		}
	}
	// Retry is the only mutation: no refund/force-settle/edit/delete affordance
	// may exist in the DOM (§6.6 — quarantine is "evidence insufficient").
	for _, banned := range []string{"Refund", "Force", "force-settle", "Delete", "Edit"} {
		if strings.Contains(body, banned) {
			t.Errorf("transfer queue must not offer %q", banned)
		}
	}
	// Status filter tabs are real links.
	if !strings.Contains(body, `href="/admin/transfers?status=quarantined"`) {
		t.Error("status filter tabs must be real links")
	}
	// Detail page.
	detail := getBody(t, cl, srv.URL+"/admin/transfers/TQ1")
	for _, want := range []string{"TQ1", "receipt hash mismatch", "$0.330", "Retry"} {
		if !strings.Contains(detail, want) {
			t.Errorf("transfer detail missing %q", want)
		}
	}
	// Settled filter shows the terminal record without a Retry button.
	settled := getBody(t, cl, srv.URL+"/admin/transfers?status=settled")
	if !strings.Contains(settled, "TSET") || strings.Contains(settled, "Retry") {
		t.Errorf("settled records must render without a Retry affordance: %s", settled)
	}
}

// The transfer queue is superuser-only, like every /admin surface.
func TestAdminTransfersHiddenFromNonSuperuser(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "alice")
	resp, err := cl.Get(srv.URL + "/admin/transfers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-superuser must get 404, got %d", resp.StatusCode)
	}
}
