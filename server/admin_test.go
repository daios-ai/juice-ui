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
	// A counterparty row keeps exactly the two levers that remain.
	for _, want := range []string{"/admin/peers/settle", "/admin/peers/suspend"} {
		if !strings.Contains(body, want) {
			t.Errorf("peer row missing %q", want)
		}
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
	// The kernel serves an action as {name, description, price}: no owner to
	// emphasize and no id to link, so the name renders plain. The transport path
	// (direct) is folded into the single online status line.
	for _, want := range []string{"kernelc", "direct", "synth/make"} {
		if !strings.Contains(body, want) {
			t.Errorf("inspect page missing %q", want)
		}
	}
	if strings.Contains(body, `class="owner">`) {
		t.Error("a peer action carries no owner segment — none is served")
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
	for _, want := range []string{"as executed here", "issued by", "a2", "corroborated", "trade-backed", "unverified"} {
		if !strings.Contains(body, want) {
			t.Errorf("evidence block missing %q", want)
		}
	}
	// One self block and one third-party block — never merged into one score, and
	// the self block carries no corroborated column (nothing can corroborate it).
	if n := strings.Count(body, "as executed here"); n != 1 {
		t.Errorf("the execution summary must be exactly one block, got %d", n)
	}
	if n := strings.Count(body, "issued by"); n != 1 {
		t.Errorf("counterparty experience must group per issuer, got %d blocks", n)
	}
	if n := strings.Count(body, ">corroborated<"); n != 1 {
		t.Errorf("only a third-party block may carry the corroborated column, got %d", n)
	}
}

// A kernel with no account here reads as discovered and offers the one gesture
// that promotes it: fund, with its key prefilled. A stranger key (source "none")
// is not part of the known network and must not claim to be.
func TestAdminInspectDiscoveredAndStranger(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	disc := getBody(t, cl, srv.URL+"/admin/peers/KEYT1")
	if !strings.Contains(disc, "known network") {
		t.Errorf("an account-less known kernel must carry the known-network chip: %s", disc)
	}
	if strings.Contains(disc, "owes us") || strings.Contains(disc, "we owe them") {
		t.Error("a kernel we hold no account for must show no position")
	}
	if !strings.Contains(disc, "form=admin-deposit") || !strings.Contains(disc, `value="KEYT1"`) {
		t.Errorf("the fund gesture must be offered with the key prefilled: %s", disc)
	}
	stranger := getBody(t, cl, srv.URL+"/admin/peers/KEYNONE")
	if strings.Contains(stranger, "known network") {
		t.Error(`a source="none" key must not read as part of the known network`)
	}
}

// A kernel we have transacted with shows the bilateral position in place of the
// retired membership badge — the honest fact, since there is nothing to join.
func TestAdminInspectShowsPosition(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	// KEYB is a peer with available=50 in the fake /control/peers.
	body := getBody(t, cl, srv.URL+"/admin/peers/KEYB")
	if !strings.Contains(body, "we owe them") {
		t.Errorf("a peer's inspect must state the bilateral position: %s", body)
	}
	for _, gone := range []string{">Subscribe<", ">Unsubscribe<", ">subscribed</span>"} {
		if strings.Contains(body, gone) {
			t.Errorf("retired affordance still rendered: %q", gone)
		}
	}
}

// A suspended peer still holds a position, and this is the page an operator reads
// before unsuspending — so the peer lookup behind it must include suspended rows.
func TestAdminInspectSuspendedPeerShowsPosition(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	// KEYS appears only under ?all=1 in the fake kernel.
	body := getBody(t, cl, srv.URL+"/admin/peers/KEYS")
	if !strings.Contains(body, "owes us") {
		t.Errorf("a suspended peer's position must still show: %s", body)
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

// The peers page sends the operator to the deposit form to fund a peer, so that
// form must accept a key — the kernel disambiguates handle / key / id by shape,
// and a deposit to an unknown key provisions the peer account.
func TestDepositFormAcceptsPeerKey(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin")
	if !strings.Contains(body, "handle or kernel key") {
		t.Errorf("the deposit form must say it takes a key: %s", body)
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

// Rename is one modal on the user's own detail page: it frees the old handle and
// carries every reference to the new one, so success lands on the NEW handle's
// page and failure stays on the old one (requirements.md §6.6).
func TestAdminRenameUser(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	page := getBody(t, cl, srv.URL+"/admin/users/alice")
	if !strings.Contains(page, `?form=admin-rename`) || !strings.Contains(page, `id="form-admin-rename"`) {
		t.Error("the user page must carry the rename opener and its modal")
	}
	if !strings.Contains(page, "Frees the old handle; references use the new one.") {
		t.Error("rename must disclose that references follow the account")
	}

	resp := postTo(t, cl, srv.URL+"/admin/users/alice/rename", page, url.Values{"new_handle": {"alicia"}})
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != "/admin/users/alicia" {
		t.Errorf("success must land on the new handle's page, got %q", got)
	}
	if lastRename != `alice {"new_handle":"alicia"}` {
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

	page := getBody(t, cl, srv.URL+"/admin/users/sys")
	resp := postTo(t, cl, srv.URL+"/admin/users/sys/rename", page, url.Values{"new_handle": {"sys"}})
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != "/admin/users/sys" {
		t.Errorf("failure must stay on the old handle, got %q", got)
	}

	// An empty handle never reaches the kernel.
	lastRename = ""
	resp = postTo(t, cl, srv.URL+"/admin/users/alice/rename", page, url.Values{"new_handle": {"  "}})
	resp.Body.Close()
	if lastRename != "" {
		t.Errorf("empty new_handle must not reach the kernel, got %q", lastRename)
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

	page := getBody(t, cl, srv.URL+"/admin/users/alice")
	// The route pattern only admits the three verbs; anything else never routes.
	resp := postTo(t, cl, srv.URL+"/admin/users/alice/vaporize", page, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user op = %d, want 404", resp.StatusCode)
	}
}

// A peer's local alias is an ordinary handle here, so the alias itself is the way
// into the one rename modal — no fourth command in the peer card's foot (§6.6).
func TestPeerAliasLinksToRename(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	body := getBody(t, cl, srv.URL+"/admin/peers")
	if !strings.Contains(body, `/admin/users/peerb?form=admin-rename`) {
		t.Errorf("the peer alias must link to the rename modal: %s", body)
	}
	if strings.Contains(body, `action="/admin/peers/rename"`) {
		t.Error("rename must not become a peer-card command")
	}
}

// The roster is one kernel list the UI partitions: the kernel pages before this
// split, so the tab filters locally and the page slices what remains. An
// unrecognised tab falls back to counterparties rather than showing nothing.
func TestAdminPeersTabPartition(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "sys")

	counter := getBody(t, cl, srv.URL+"/admin/peers")
	if !strings.Contains(counter, "peerb") || !strings.Contains(counter, "debtor") {
		t.Error("counterparties tab must list the account-holding kernels")
	}
	if strings.Contains(counter, "KEYT1") {
		t.Error("a discovered kernel must not appear among counterparties")
	}
	known := getBody(t, cl, srv.URL+"/admin/peers?tab=known")
	if !strings.Contains(known, "KEYT1") || !strings.Contains(known, "KEYT2") {
		t.Error("known-network tab must list the discovered kernels")
	}
	if strings.Contains(known, "KEYB") {
		t.Error("a counterparty must not appear among discovered kernels")
	}
	if bogus := getBody(t, cl, srv.URL+"/admin/peers?tab=nonsense"); !strings.Contains(bogus, "peerb") {
		t.Error("an unrecognised tab must fall back to counterparties")
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
	// Funding a kernel lands on the roster, where the promoted row now appears.
	if loc := post("/admin/peers"); loc != "/admin/peers" {
		t.Errorf("funding from the network page must return there, got %q", loc)
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
		{"/admin/peers/KEYT1?form=admin-deposit", "/admin/peers"},
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
