package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
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
	// Nor should the command registry carry admin commands.
	if strings.Contains(home, `"admin users"`) {
		t.Error("non-superuser command registry must not include admin commands")
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
	if !strings.Contains(body, `href="/admin/users/alice@testnet"`) {
		t.Error("/admin user handle must link to the user's detail page")
	}
	// Admin and Peers are now separate top-level sidebar entries (not a sub-nav).
	if !strings.Contains(body, `href="/admin" title="Admin"`) {
		t.Error("superuser sidebar must have an Admin nav entry")
	}
	if !strings.Contains(body, `href="/admin/peers" title="Network"`) {
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
	if !strings.Contains(body, "?form=admin-deposit") {
		t.Error("/admin must offer the deposit command inline")
	}
	// admin withdraw was retired with the settle surface in 0.7.0: the kernel
	// serves no such verb, and a command entry for one opens a dialog that does
	// not exist.
	if strings.Contains(body, "admin-withdraw") {
		t.Error("/admin must not offer a retired admin withdraw")
	}

	// The peers page leads with this kernel's money and highlights its nav entry.
	pbody := getBody(t, cl, srv.URL+"/admin/peers")
	if !strings.Contains(pbody, `class="stat-row"`) || !strings.Contains(pbody, "owed to us") {
		t.Error("/admin/peers must state this kernel's money through the figures")
	}
	if !strings.Contains(pbody, `href="/admin/peers" title="Network" aria-current="page"`) {
		t.Error("/admin/peers must mark the network nav entry active")
	}
}

// The network page is peers plus the two by-key entries. There is no
// subscription and no known-network roster: a peer arises from use or from a
// deposit addressed to its key, and discovered kernels reach the operator only
// through lookup results.
func TestAdminPeersHasNoSubscriptionOrRoster(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	if !strings.Contains(body, "peerb") {
		t.Error("peers page must list the peer peerb")
	}
	for _, gone := range []string{"Subscribe", "Unsubscribe", "subscribed", "Known network", "self-reported"} {
		if strings.Contains(body, gone) {
			t.Errorf("retired subscription/roster vocabulary still rendered: %q", gone)
		}
	}
	if strings.Contains(body, "/admin/peers/subscribe") || strings.Contains(body, "/admin/peers/unsubscribe") {
		t.Error("subscription routes must not be referenced")
	}
	// The two by-key entries that replace it: inspect (a pure read, so a GET) and
	// funding, which reuses the admin deposit form because a key is a valid handle.
	if !strings.Contains(body, `action="/admin/peers/inspect"`) || !strings.Contains(body, `method="get"`) {
		t.Error("peers page must offer inspect-a-kernel-by-key as a GET form")
	}
	// Funding is reached from the kernel it is about — a discovered card's Fund
	// opener, or the inspect page of one with no account — so the key is prefilled
	// rather than retyped. There is no separate fund-by-key opener.
	if strings.Contains(body, "fund a peer by key") || strings.Contains(body, "fund a kernel by key") {
		t.Error("the page-level fund-by-key opener is retired: inspect \u2192 fund covers an out-of-band key")
	}
	// Suspend is the one durable lever left on a counterparty, on its own page;
	// a peer holds no money here, so there is nothing to settle (kernel §D14).
	if !strings.Contains(getBody(t, cl, srv.URL+"/admin/peers/KEYB"), "/admin/peers/suspend") {
		t.Error("a counterparty's page is missing the suspend lever")
	}
	if strings.Contains(body, "/admin/peers/settle") {
		t.Error("settlement is per call on the rail; the kernel serves no settle route")
	}
}

// Inspecting by key redirects to the canonical inspect URL; an empty key is a
// flash, not a 404 page.
func TestAdminInspectByKeyRedirects(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := cl.Get(srv.URL + "/admin/peers/inspect?key=KEYC")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/peers/KEYC" {
		t.Errorf("inspect-by-key = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp2, err := cl.Get(srv.URL + "/admin/peers/inspect?key=")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if loc := resp2.Header.Get("Location"); loc != "/admin/peers" {
		t.Errorf("empty key must return to the peers page, got %q", loc)
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

// The roster is not polled. Its facts advance once per the kernel's discovery
// interval, while a refresh now costs one pass over the whole known network, so
// it refreshes on navigation and after a mutation. Polling stays on inspect.
func TestAdminPeersIsNotPolled(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	if strings.Contains(body, `hx-get="/admin/peers`) {
		t.Errorf("the roster must not poll itself: %s", body)
	}
	// The inspect status card keeps its poll (it also freshens the sync cache).
	if ibody := getBody(t, cl, srv.URL+"/admin/peers/KEYB"); !strings.Contains(ibody, `hx-get="/admin/peers/KEYB"`) {
		t.Error("the inspect status card must still poll")
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
	// The kernel names an inspected action by its address (4dbfedd), which
	// renders as every address does: the owner emphasized, and unlinked — it is
	// another kernel's, with no local profile or row behind it. The transport
	// path (direct) is folded into the single online status line.
	for _, want := range []string{"kernelc", "direct", `class="owner">synth@kernelc</span>/make`} {
		if !strings.Contains(body, want) {
			t.Errorf("inspect page missing %q", want)
		}
	}
	if strings.Contains(body, `href="/users/synth@kernelc"`) {
		t.Error("another kernel's owner must not link to a local profile")
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
	// Evidence renders in two views that are never folded: the subject's own
	// execution rows, then each other issuer's experience with its corroborated
	// count. A subject is named by its full action id — nothing joins it to a name.
	if !strings.Contains(body, "Evidence") || strings.Contains(body, "Transacted peers") {
		t.Error("inspect must render Evidence, not the retired transacted-peers list")
	}
	for _, want := range []string{"reported by the provider", "issued by", "a2", "corroborated", "trade-backed", "unverified"} {
		if !strings.Contains(body, want) {
			t.Errorf("evidence block missing %q", want)
		}
	}
	// One self block and one third-party block — never merged into one score, and
	// the self block carries no corroborated column (nothing can corroborate it).
	if n := strings.Count(body, "reported by the provider"); n != 1 {
		t.Errorf("the execution summary must be exactly one block, got %d", n)
	}
	if n := strings.Count(body, "issued by"); n != 1 {
		t.Errorf("counterparty experience must group per issuer, got %d blocks", n)
	}
	if n := strings.Count(body, ">corroborated<"); n != 1 {
		t.Errorf("only a third-party block may carry the corroborated column, got %d", n)
	}
}

// A discovered kernel carries no party mark, no position and no funding — a
// peer is never credited (§6.6) — and can still be suspended by key.
func TestAdminInspectDiscoveredKernel(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	disc := getBody(t, cl, srv.URL+"/admin/peers/KEYT1")
	if strings.Contains(disc, `<span class="pill">party</span>`) || !strings.Contains(disc, `action="/admin/peers/suspend"`) || !strings.Contains(disc, "<dt>Known since</dt><dd>2026-09-14</dd>") {
		t.Errorf("a discovered kernel is not a party, is known since its row says, and can be suspended: %s", disc)
	}
	// A key this kernel has never learned has no row: a usable page, no error.
	none := getBody(t, cl, srv.URL+"/admin/peers/KEYNONE")
	if strings.Contains(none, "<h1>Error</h1>") || strings.Contains(none, "<dt>Known since</dt>") || !strings.Contains(none, `action="/admin/peers/suspend"`) {
		t.Errorf("a stranger key renders without a row: %s", none)
	}
	if strings.Contains(disc, "owes us") || strings.Contains(disc, "we owe them") {
		t.Error("a kernel we hold no account for must show no position")
	}
	// Funding a kernel is gone: a peer account holds no money, and a deposit
	// naming a peer is refused unless it names the obligation it closes (§6.6).
	if strings.Contains(disc, "fund this kernel") {
		t.Error("a peer is not funded: its payments close obligations, they do not top up a balance")
	}
}

// A peer account is identity and moderation state, never a wallet (kernel §D14),
// so the inspect page states suspension and never a balance.
func TestAdminInspectStatesModerationNotMoney(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers/KEYB")
	for _, gone := range []string{"we owe them", "owes us", "position", ">Subscribe<", ">Unsubscribe<"} {
		if strings.Contains(body, gone) {
			t.Errorf("retired money or membership vocabulary still rendered: %q", gone)
		}
	}
	// KEYS is suspended in the fake kernel: its row says so, and the page offers
	// the way back.
	susp := getBody(t, cl, srv.URL+"/admin/peers/KEYS")
	if !strings.Contains(susp, `<span class="pill status-danger">suspended</span>`) || !strings.Contains(susp, `action="/admin/peers/unsuspend"`) {
		t.Errorf("a suspended peer must say so and offer Unsuspend: %s", susp)
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

// The network page states this kernel's own credit position — what it has
// delivered to foreign buyers and not been paid for, against the limit it set —
// the rates it serves under, and the obligations awaiting the operator. A peer
// row carries no money at all (kernel §D14/§P10).
func TestNetworkPageStatesCreditAndObligations(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	for _, want := range []string{"owed to us", "320 credits", "1000 credits", "limit", "fee 20%", "import 5%"} {
		if !strings.Contains(body, want) {
			t.Errorf("kernel card missing the credit position or rates: %q", want)
		}
	}
	for _, gone := range []string{"we owe them", "owes us ", "settlement due", "our credit there"} {
		if strings.Contains(body, gone) {
			t.Errorf("a peer holds no money here: found %q", gone)
		}
	}
	// The obligations are exposure itemised: a reading, with no gesture. An
	// obligation closes when its buyer pays, and the kernel records that itself
	// (docs/kernel-v0.15-verification.md §3) — there is nothing here to press.
	if !strings.Contains(body, "Owed to us") || !strings.Contains(body, "TICKET1") {
		t.Errorf("the obligations section must list the owed ticket: %s", body)
	}
	if strings.Contains(body, "Confirm (") || strings.Contains(body, `name="ref" value="TICKET1"`) {
		t.Error("an obligation carries no operator gesture: it closes when its buyer pays")
	}
	// A row whose buyer has not spoken carries an empty status and a zero amount.
	// Both are absent fields: rendering "0 credits" would name an amount nobody
	// has decided, and an empty pill would show a status nobody has stated.
	if !strings.Contains(body, "awaiting the buyer") {
		t.Errorf("an unanswered obligation must say so rather than show an empty badge: %s", body)
	}
	if strings.Contains(body, `<span class="pill status-escrow"></span>`) {
		t.Error("an absent status must not render as an empty pill")
	}
	// A payment whose sender nobody has registered waits for the operator to say
	// whose it is. The address it came from is the fact that decides, and the
	// attribute link carries the reference so it is not retyped.
	if !strings.Contains(body, "Awaiting an owner") || !strings.Contains(body, "0x70997970c518") {
		t.Errorf("a held payment must be listed with the address it came from: %s", body)
	}
	if !strings.Contains(body, "form=admin-deposit&amp;ref=rail%3a0xabc%3a0") {
		t.Errorf("attributing a held payment must prefill the id that names it: %s", body)
	}
}

// §6.6: the value-transfer queue is GONE. The kernel stopped serving
// /control/transfers once value became local to a kernel (404 on v0.12.23), so
// no route, no link, and no client method may remain — a page that cannot load
// is worse than no page.
func TestNoTransferQueueSurface(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	for _, path := range []string{"/admin/transfers", "/admin/transfers/TQ1"} {
		resp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s must not be routed, got %d", path, resp.StatusCode)
		}
	}
	// And nothing may link to it.
	if body := getBody(t, cl, srv.URL+"/admin/peers"); strings.Contains(body, "/admin/transfers") {
		t.Error("the peers page must not link to a removed queue")
	}
}

// A deposit credits a USER and nothing else. The kernel serves it under the
// users namespace and refuses a peer key there, so the form must not invite one:
// a peer account is identity, not a wallet, and what a peer owes closes when it
// pays (docs/kernel-v0.15-verification.md §2).
func TestDepositFormIsUserOnly(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin")
	if strings.Contains(body, "handle or kernel key") || strings.Contains(body, "ticket id") {
		t.Errorf("the deposit form must not invite a peer or a ticket: %s", body)
	}
	// And the request it makes names the account in the path.
	page := getBody(t, cl, srv.URL+"/admin")
	resp := postTo(t, cl, srv.URL+"/admin/deposit", page, url.Values{
		"address": {"alice@testnet"}, "ref": {"R1"}, "amount": {"100"}, "back": {"/admin"},
	})
	resp.Body.Close()
	if lastDeposit != "alice@testnet" {
		t.Errorf("deposit named %q; the kernel takes the user in the path", lastDeposit)
	}
}

// postTo posts a form to urlStr, taking the CSRF token from page (the form's own
// page) and following no redirect, so a test reads the Location the handler chose.
func postTo(t *testing.T, cl *http.Client, urlStr, page string, form url.Values) *http.Response {
	t.Helper()
	form.Set("gorilla.csrf.Token", csrfTokenFrom(t, page))
	req, _ := http.NewRequest(http.MethodPost, urlStr, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	prev := cl.CheckRedirect
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { cl.CheckRedirect = prev }()
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// Rename is one modal on the user's own detail page: it frees the old address
// and carries every reference to the new one, so success lands on the NEW
// address's page and failure stays on the old one (requirements.md §6.6).
func TestAdminRenameUser(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	page := getBody(t, cl, srv.URL+"/admin/users/alice@testnet")
	if !strings.Contains(page, `?form=admin-rename`) || !strings.Contains(page, `id="form-admin-rename"`) {
		t.Error("the user page must carry the rename opener and its modal")
	}

	resp := postTo(t, cl, srv.URL+"/admin/users/alice@testnet/rename", page, url.Values{"new_name": {"alicia@testnet"}})
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != "/admin/users/alicia@testnet" {
		t.Errorf("success must land on the new address's page, got %q", got)
	}
	if lastRename != `alice@testnet {"new_name":"alicia@testnet"}` {
		t.Errorf("kernel saw %q", lastRename)
	}
}

// A rejected rename (the kernel refuses sys) stays on the handle that still
// exists, with the kernel's own reason flashed.
func TestAdminRenameFailureStaysOnOldHandle(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	page := getBody(t, cl, srv.URL+"/admin/users/sys@testnet")
	resp := postTo(t, cl, srv.URL+"/admin/users/sys@testnet/rename", page, url.Values{"new_name": {"sys"}})
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != "/admin/users/sys@testnet" {
		t.Errorf("failure must stay on the old handle, got %q", got)
	}

	// An empty handle never reaches the kernel.
	lastRename = ""
	resp = postTo(t, cl, srv.URL+"/admin/users/alice@testnet/rename", page, url.Values{"new_name": {"  "}})
	resp.Body.Close()
	if lastRename != "" {
		t.Errorf("empty new_name must not reach the kernel, got %q", lastRename)
	}
}

// An unrecognised per-user verb is a 404, never a fallthrough into a destructive
// one: the dispatch switch is exhaustive.
func TestAdminUnknownUserOpIs404(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	page := getBody(t, cl, srv.URL+"/admin/users/alice@testnet")
	// The route pattern only admits the three verbs; anything else never routes.
	resp := postTo(t, cl, srv.URL+"/admin/users/alice@testnet/vaporize", page, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user op = %d, want 404", resp.StatusCode)
	}
}

// A peer's row opens its own page, which holds the one rename modal (§6.6). Both
// key on the PUBLIC KEY: an unbound kernel has no name to key on, and
// advertised names collide across kernels.
func TestPeerRowLinksToItsPage(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	if !strings.Contains(body, `data-href="/admin/peers/KEYB"`) {
		t.Errorf("the peer row must open the peer's page: %s", body)
	}
	if !strings.Contains(getBody(t, cl, srv.URL+"/admin/peers/KEYB"), `/admin/peers/KEYB?form=admin-rename`) {
		t.Error("the peer's page must open its rename modal, keyed on the key")
	}
	// The kernel serves users and peers on separate routes and refuses a target
	// of the other kind, so the UI must never send a key to the users route.
	if strings.Contains(body, `/admin/users/KEYB`) {
		t.Error("a peer is never addressed through the users namespace")
	}
}

// The network list is this kernel, then its counterparties, then the known
// network, each in the kernel's order, sliced into pages after ordering (§6.6).
func TestAdminPeersListOrder(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	last := -1
	for _, mark := range []string{`title="KEY">kernel<`, `"/admin/peers/KEYB"`, `"/admin/peers/KEYD"`, `"/admin/peers/KEYT1"`, `"/admin/peers/KEYT2"`} {
		i := strings.Index(body, mark)
		if i <= last {
			t.Fatalf("%s out of order: %s", mark, body)
		}
		last = i
	}
}

// This kernel's identity is the first row and the figures, so a failed read is
// an error page, never a blank current row (§6.6).
func TestAdminPeersRequiresIdentity(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	target, _ := url.Parse(kernel.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/admin/kernel" {
			http.Error(w, `{"code":"internal","error":"boom"}`, http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer broken.Close()
	srv := httptest.NewServer(newTestHandler(t, broken.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	resp, err := cl.Get(srv.URL + "/admin/peers")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<h1>Error</h1>") || strings.Contains(string(body), "action-list") {
		t.Errorf("a failed identity read must be the error page: %s", body)
	}
}

// Suspending returns to the page the verb lives on: the peer's own.
func TestAdminPeerSuspendReturnsToThePeer(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	page := getBody(t, cl, srv.URL+"/admin/peers/KEYB")
	resp := postTo(t, cl, srv.URL+"/admin/peers/suspend", page, url.Values{"key": {"KEYB"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/peers/KEYB" {
		t.Errorf("suspend = %d to %q, want 303 to /admin/peers/KEYB", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The deposit form's return path is a closed set: a form field must never be able
// to name an arbitrary redirect target, and the two pages that host the form are
// the admin roster and the network page.
func TestAdminDepositBackAllowlist(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	post := func(back string) string {
		tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/admin"))
		form := url.Values{"handle": {"KEYT1"}, "amount": {"5"}, "back": {back}, "gorilla.csrf.Token": {tok}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/deposit", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}
	// Attributing a held payment happens on the network page, and returns there.
	if loc := post("/admin/peers"); loc != "/admin/peers" {
		t.Errorf("a deposit from the network page must return there, got %q", loc)
	}
	for _, hostile := range []string{"https://evil.example/x", "//evil.example", "/admin/peers/KEYT1", ""} {
		if loc := post(hostile); loc != "/admin" {
			t.Errorf("back=%q must fall back to /admin, got %q", hostile, loc)
		}
	}
}

// The shared deposit dialog closes to the page that opened it. An empty Back
// would render href="" on the close and cancel links, which reloads the current
// URL with ?form= still set — reopening the dialog forever on the no-JS path.
func TestDepositDialogClosesToItsOwnPage(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	for _, tc := range []struct{ url, back string }{
		{"/admin?form=admin-deposit", "/admin"},
		{"/admin/peers?form=admin-deposit", "/admin/peers"},
	} {
		body := getBody(t, cl, srv.URL+tc.url)
		dlg := body[strings.Index(body, `id="form-admin-deposit"`):]
		dlg = dlg[:strings.Index(dlg, "</dialog>")]
		if !strings.Contains(dlg, "<dialog") && !strings.Contains(body[:strings.Index(body, `id="form-admin-deposit"`)+40], "open") {
			t.Errorf("%s must render the dialog open on the no-JS path", tc.url)
		}
		if strings.Contains(dlg, `href=""`) {
			t.Errorf("%s: dialog close/cancel must not be an empty href (it reopens itself): %s", tc.url, dlg)
		}
		if !strings.Contains(dlg, `href="`+tc.back+`"`) || !strings.Contains(dlg, `name="back" value="`+tc.back+`"`) {
			t.Errorf("%s: dialog must close and return to %s: %s", tc.url, tc.back, dlg)
		}
	}
}

// §6.6: the kernel's contact cache renders as dated fact on every roster row —
// when an exchange last succeeded, and when one last failed. Muted text, never
// a pill: the kernel states no verdict about whether a peer is up, and neither
// does the UI. Each clause is omitted until that thing has happened.
func TestPeerContactTimestampsRenderAsDatedFacts(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	// Never a status pill — that would read as a verdict on availability.
	for _, banned := range []string{`pill status-danger">offline`, `pill">unreachable`, ">offline<"} {
		if strings.Contains(body, banned) {
			t.Errorf("contact timestamps must not become a status signal: %q", banned)
		}
	}
	// The debtor row has neither timestamp, so both its slots are muted none.
	// Its own name proves the row rendered at all.
	if !strings.Contains(body, ">debtor<") {
		t.Fatal("fixture changed: the debtor row is missing")
	}
	// The discovered rows carry last_seen too (v0.12.24 stamps every row), so
	// catalog freshness appears on both classes.
	for want, n := range map[string]int{`title="Catalog verified`: 3, `title="Catalog never verified"`: 1, `title="Last contact failed"`: 1, `title="No contact has failed"`: 3} {
		if got := strings.Count(body, want); got != n {
			t.Errorf("%s: %d slots, want %d", want, got, n)
		}
	}
}

// The rename form opens prefilled by the kernel's auto-bind preference, past
// every name held here (§6.6): KEYT1 claims "twin", which KEYT2 holds, so it
// is offered twin2; a bound kernel is offered its petname. The roster is read
// only for that form: a plain open renders without it.
func TestInspectPrefillsSuggestion(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	if body := getBody(t, cl, srv.URL+"/admin/peers/KEYT1?form=admin-rename"); !strings.Contains(body, `name="new_name" type="text" class="mono" value="twin2"`) {
		t.Errorf("a discovered twin is offered twin2: %s", body)
	}
	if body := getBody(t, cl, srv.URL+"/admin/peers/KEYB?form=admin-rename"); !strings.Contains(body, `value="kernelc" required`) {
		t.Errorf("a bound kernel is offered its petname: %s", body)
	}

	target, _ := url.Parse(kernel.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/admin/peers" {
			http.Error(w, `{"code":"internal","error":"boom"}`, http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer broken.Close()
	srv2 := httptest.NewServer(newTestHandler(t, broken.URL))
	defer srv2.Close()
	cl2 := loginClientAs(t, srv2.URL, "sys")
	if body := getBody(t, cl2, srv2.URL+"/admin/peers/KEYT1"); !strings.Contains(body, "a peer kernel") || strings.Contains(body, "<dt>Catalog</dt>") {
		t.Errorf("a plain open survives a failed roster read, without a catalog: %s", body)
	}
}

// The catalog's size is the roster's count; the inspection's own list is one
// page, so the page says how many of them it shows (§6.6).
func TestInspectCatalogCountsTheRoster(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers/KEYB")
	for _, want := range []string{"<dt>Catalog</dt><dd>4 public actions</dd>", `<p class="muted small">1 of 4 shown</p>`, "<dt>Known since</dt><dd>2026-09-14</dd>", `<a class="btn" href="/admin/peers/KEYB?form=admin-rename"`} {
		if !strings.Contains(body, want) {
			t.Errorf("kernel page missing %q: %s", want, body)
		}
	}
}

// Work addressed to this kernel reaches its operator's inbox (4dbfedd): the
// peer's page lists none and serves no completion route (§6.6).
func TestInspectListsNoPeerHeldTasks(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	page := getBody(t, cl, srv.URL+"/admin/peers/KEYB")
	if strings.Contains(page, "Waiting for this kernel") || strings.Contains(page, "/steps/") {
		t.Errorf("the peer's page lists no held tasks: %s", page)
	}
	resp := postTo(t, cl, srv.URL+"/admin/peers/KEYB/steps/PS1/complete", page, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("the peer-task completion route is gone, got %d", resp.StatusCode)
	}
}

// fakeConfig is this installation's configuration file at version "v1".
type fakeConfig struct {
	file, schema, wrote string
	err                 error
}

func (f *fakeConfig) Config() ([]byte, []byte, string, error) {
	return []byte(f.file), []byte(f.schema), "v1", nil
}

func (f *fakeConfig) Reconfigure(version string, cfg []byte) error {
	if version != "v1" {
		return errors.New("the configuration changed since this page was loaded; reload it")
	}
	if f.err != nil {
		return f.err
	}
	f.wrote = string(cfg)
	return nil
}

const testConfigFile = `{"$schema":"s.json","kernel_handle":"k","listen_addr":"127.0.0.1:4040","credentials_key":"SECRET-CREDENTIALS-KEY",
"fee_bps":2000,"auth_audience":"","credit_limit":null,"native":{"time":{"price":0},"llm":{"endpoints":{"ollama":{"key":"SECRET-ENDPOINT-KEY"}}}}}`

const testConfigSchema = `{"type":"object","additionalProperties":false,"properties":{
"$schema":{"type":"string"},"kernel_handle":{"type":"string"},"listen_addr":{"type":"string"},
"credentials_key":{"type":"string"},"fee_bps":{"type":"integer","default":2000},
"auth_audience":{"type":"string"},"credit_limit":{"type":["integer","null"]},
"native":{"type":"object","additionalProperties":false,"properties":{
	"time":{"type":"object","additionalProperties":false,"properties":{"price":{"type":"integer"}}},
	"llm":{"type":"object","additionalProperties":false,"properties":{
		"endpoints":{"type":["object","null"],"additionalProperties":{"type":"object"}}}}}}}}`

func configServer(t *testing.T) (*fakeConfig, *httptest.Server, *http.Client) {
	t.Helper()
	kernel := authKernel()
	t.Cleanup(kernel.Close)
	fc := &fakeConfig{file: testConfigFile, schema: testConfigSchema}
	srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) { w.config = fc }))
	t.Cleanup(srv.Close)
	return fc, srv, loginClientAs(t, srv.URL, "sys")
}

// configPost saves the form and returns the flash it lands with.
func configPost(t *testing.T, cl *http.Client, base string, form url.Values) string {
	t.Helper()
	if form.Get("version") == "" {
		form.Set("version", "v1")
	}
	resp := postTo(t, cl, base+"/admin/config", getBody(t, cl, base+"/admin/config"), form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/config" {
		t.Fatalf("save = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, c := range resp.Cookies() {
		if c.Name == "flash" {
			f, _ := url.QueryUnescape(c.Value)
			return f
		}
	}
	return ""
}

// The page is the kernel's schema filled from the file: the first boot's name
// and the listen address read-only, every secret withheld, an endpoint's key
// write-only, and the JSON editor holding the file itself.
func TestAdminConfigPage(t *testing.T) {
	_, srv, cl := configServer(t)
	body := getBody(t, cl, srv.URL+"/admin/config")
	for _, want := range []string{
		`name="f_/fee_bps"`, `value="2000"`, `<div class="box">k</div>`, `name="version" value="v1"`,
		`name="s_native.llm.endpoints.ollama.key" type="password"`, `fee_bps&#34;: 2000`,
		`<button class="btn" type="submit">Save and restart</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, not := range []string{"SECRET-CREDENTIALS-KEY", "SECRET-ENDPOINT-KEY", "credentials_key", `f_/kernel_handle`, `f_/listen_addr`, `f_/$schema`} {
		if strings.Contains(body, not) {
			t.Errorf("page has %q", not)
		}
	}
	if b := getBody(t, cl, srv.URL+"/admin"); !strings.Contains(b, `href="/admin/config"`) || !strings.Contains(b, `"name":"admin config"`) {
		t.Error("admin links the configuration, and the command registry lists it")
	}
}

// A save rewrites the file from the form. What the form cannot carry is kept:
// the fixed keys, a secret left blank, and an empty value a blank input cannot
// tell from absent. Nothing changed writes nothing; a refusal is shown as given.
func TestAdminConfigSave(t *testing.T) {
	fc, srv, cl := configServer(t)
	fields := url.Values{"has_fields": {"1"},
		"t_/fee_bps": {"integer"}, "f_/fee_bps": {"2000"}, "f_/auth_audience": {""}, "f_/credit_limit": {""},
		"t_/native": {"object"}, "t_/native/time": {"object"}, "t_/native/time/price": {"integer"}, "f_/native/time/price": {"0"},
		"t_/native/llm": {"object"}, "t_/native/llm/endpoints": {"object"}, "f_/native/llm/endpoints": {`{"ollama":{}}`},
		"s_native.llm.endpoints.ollama.key": {""}}
	wrote := func(want string) {
		t.Helper()
		var got, w any
		json.Unmarshal([]byte(fc.wrote), &got)
		json.Unmarshal([]byte(want), &w)
		if !reflect.DeepEqual(got, w) {
			t.Errorf("wrote %s\nwant  %s", fc.wrote, want)
		}
		fc.wrote = ""
	}

	if f := configPost(t, cl, srv.URL, fields); f != "info|Nothing changed." || fc.wrote != "" {
		t.Errorf("unchanged form flashed %q, wrote %q", f, fc.wrote)
	}
	shown := `{"$schema":"s.json","kernel_handle":"k","listen_addr":"127.0.0.1:4040","fee_bps":2000,"auth_audience":"","credit_limit":null,"native":{"time":{"price":0},"llm":{"endpoints":{"ollama":{}}}}}`
	if f := configPost(t, cl, srv.URL, url.Values{"raw_args": {shown}}); f != "info|Nothing changed." {
		t.Errorf("unchanged JSON flashed %q", f)
	}

	form := url.Values{}
	for k, v := range fields {
		form[k] = v
	}
	form.Set("f_/fee_bps", "")
	form.Del("f_/native/time/price")
	form.Set("s_native.llm.endpoints.ollama.key", "sk")
	if f := configPost(t, cl, srv.URL, form); f != "success|Saved and restarted." {
		t.Errorf("save flashed %q", f)
	}
	wrote(`{"$schema":"s.json","kernel_handle":"k","listen_addr":"127.0.0.1:4040","credentials_key":"SECRET-CREDENTIALS-KEY",
		"auth_audience":"","credit_limit":null,"native":{"llm":{"endpoints":{"ollama":{"key":"sk"}}}}}`)

	// The JSON editor cannot rename the kernel or drop a secret.
	edited := strings.Replace(strings.Replace(shown, `"k"`, `"x"`, 1), `"fee_bps":2000,`, ``, 1)
	configPost(t, cl, srv.URL, url.Values{"raw_args": {edited}})
	wrote(`{"$schema":"s.json","kernel_handle":"k","listen_addr":"127.0.0.1:4040","credentials_key":"SECRET-CREDENTIALS-KEY",
		"auth_audience":"","credit_limit":null,"native":{"time":{"price":0},"llm":{"endpoints":{"ollama":{"key":"SECRET-ENDPOINT-KEY"}}}}}`)

	stale := url.Values{"raw_args": {edited}, "version": {"v0"}}
	if f := configPost(t, cl, srv.URL, stale); !strings.Contains(f, "changed since this page was loaded") {
		t.Errorf("stale flashed %q", f)
	}
	fc.err = errors.New("the kernel did not start with this configuration; the previous one is restored: bad")
	if f := configPost(t, cl, srv.URL, url.Values{"raw_args": {edited}}); !strings.Contains(f, "previous one is restored") {
		t.Errorf("refusal flashed %q", f)
	}
}

// The page exists for this installation's kernel alone, and for the superuser.
func TestAdminConfigIsLocalAndSuperuserOnly(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")
	resp, _ := cl.Get(srv.URL + "/admin/config")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || strings.Contains(getBody(t, cl, srv.URL+"/admin"), "/admin/config") {
		t.Error("a kernel elsewhere has no configuration page, link or command")
	}

	_, srv2, _ := configServer(t)
	resp, _ = loginClientAs(t, srv2.URL, "alice").Get(srv2.URL + "/admin/config")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-superuser /admin/config = %d want 404", resp.StatusCode)
	}
}
