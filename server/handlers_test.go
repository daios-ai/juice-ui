package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

// authKernel is a fake kernel sufficient to drive the PKCE login flow and a few
// authenticated reads. The signed-in user is "U1" / "@alice".
func authKernel() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		// Open endpoint; also carries the kernel's own federation identity.
		w.Write([]byte(`{"status":"ok","handle":"@testnet","public_key":"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcd"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"@alice","email":"alice@test.com","available":500,"locked":0,"grants":[{"action":"@sys/gmail-send","scopes":["send"],"created_at":"2024-01-01T00:00:00Z"}]}`))
	})
	mux.HandleFunc("PUT /v1/me", func(w http.ResponseWriter, r *http.Request) {
		var inp struct {
			CurrentPassword string `json:"current_password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&inp)
		if inp.CurrentPassword == "wrongpass" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid current password"}`))
			return
		}
		w.Write([]byte(`{"id":"U1","handle":"@alice","email":"new@test.com","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		// T1: @alice is buyer; T2: @alice is seller. Both reference action "owned".
		w.Write([]byte(`[
			{"id":"T1","action_id":"owned","owner_handle":"@alice","target_handle":"@bob","caller_handle":"@alice","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"},
			{"id":"T2","action_id":"owned","owner_handle":"@bob","target_handle":"@alice","caller_handle":"@bob","status":"success","gross":50,"fee":5,"net":45,"started_at":"2024-01-02T00:00:00Z"}
		]`))
	})
	mux.HandleFunc("GET /v1/stats/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"action_id":"x"}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		// The kernel returns active rows by default; the inactive/private draft
		// (A3) appears only with ?all=1 — so tests that see the draft prove the UI
		// threads all=1 for the owner's own list.
		a1 := `{"id":"A1","action":"@alice/hello","owner_handle":"@alice","name":"hello","kind":"wasm","active":true,"public":true,"price":0}`
		a3 := `{"id":"A3","action":"@alice/draft","owner_handle":"@alice","name":"draft","kind":"wasm","active":false,"public":false,"price":0}`
		if r.URL.Query().Get("all") == "1" {
			w.Write([]byte("[" + a1 + "," + a3 + "]"))
		} else {
			w.Write([]byte("[" + a1 + "]"))
		}
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		// "owned"/"A1" belong to @alice (the signed-in user); anything else to @bob.
		owner := "@bob"
		name := "x"
		if r.PathValue("id") == "owned" || r.PathValue("id") == "A1" {
			owner = "@alice"
		}
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","action":"` + owner + `/` + name +
			`","owner_handle":"` + owner + `","name":"` + name + `","kind":"http",` +
			`"active":true,"public":true,"price":0,` +
			`"input_schema":{"type":"object","properties":{}},"output_schema":{"type":"object"}}`))
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"status":"success","action_id":"A2","action_name":"synthesized","diagnostics":[]},"tx_id":"TX1","trace_id":"TR1"}`))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"T1","action_id":"owned","owner_handle":"@alice","target_handle":"@bob","caller_handle":"@alice","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/enable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"active":true}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/disable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"active":false}`))
	})
	mux.HandleFunc("PUT /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","action":"@alice/x","owner_handle":"@alice","name":"x","kind":"http","active":true,"public":true,"price":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"id":"S1","required_caller_handle":"@alice","action":"@alice/hello","action_id":"A1","price":5,"status":"waiting","created_by":"@alice/dispatch","created_at":"2026-01-01T00:00:00Z"},
			{"id":"S2","required_caller_handle":"@alice","action":"@alice/hello","action_id":"A1","price":3,"status":"waiting","created_by":"@bob/relay","created_at":"2026-01-02T00:00:00Z"}
		]`))
	})
	mux.HandleFunc("GET /v1/steps/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","required_caller_handle":"@alice","action":"@alice/hello","action_id":"A1","price":5,"partial_args":{},"status":"waiting","created_at":"2026-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("POST /v1/users", func(w http.ResponseWriter, r *http.Request) {
		// A "dupe" email/handle simulates the kernel's raw UNIQUE-constraint 500.
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "dupe") {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"code":"internal","error":"create user: constraint failed: UNIQUE constraint failed: users.email (2067)"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"U9","handle":"@new","email":"new@test.com","available":0,"locked":0}`))
	})
	mux.HandleFunc("GET /control/users", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"U1","handle":"@alice","email":"alice@test.com","available":500,"locked":0}]`))
	})
	mux.HandleFunc("GET /control/users/{handle}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"` + r.PathValue("handle") + `","email":"x@test.com","available":500,"locked":0}`))
	})
	mux.HandleFunc("POST /control/users/{handle}/suspend", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /control/users/{handle}/unsuspend", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /control/deposit", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"direction":"credit","amount":100,"operator_handle":"@sys","target_handle":"@alice"}`))
	})
	mux.HandleFunc("POST /control/withdraw", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"direction":"debit","amount":10,"operator_handle":"@sys","target_handle":"@alice"}`))
	})
	mux.HandleFunc("GET /control/peers", func(w http.ResponseWriter, r *http.Request) {
		// With gossip=1, return a friend plus a known-network roster (verified shape).
		if r.URL.Query().Get("gossip") == "1" {
			// Roster carries @kernelc (not a friend) and @peerb (already a friend).
			w.Write([]byte(`{"peers":[{"handle":"@peerb","public_key":"KEYB","available":50,"locked":0}],
			 "roster":[{"public_key":"KEYC","handle":"@kernelc","own":[{"action_id":"a1","name":"@sys/time","description":"","price":0,"uses":3,"rating":1}],
			  "sources":[{"introduced_by":"KEYC","self_reported":true,"actions":[{"action_id":"a2","name":"@sys/make","description":"synth","price":20,"uses":0,"rating":0}]},
			             {"introduced_by":"KEYB","self_reported":false,"actions":[{"action_id":"a1","name":"@sys/time","description":"","price":0,"uses":1,"rating":0}]}]},
			           {"public_key":"KEYB","handle":"@peerb","sources":[{"introduced_by":"KEYB","self_reported":true,"actions":[]}]}]}`))
			return
		}
		w.Write([]byte(`{"peers":[{"handle":"@peerb","public_key":"KEYB","available":50,"locked":0}]}`))
	})
	mux.HandleFunc("GET /control/peers/inspect", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"handle":"@kernelc","public_key":"` + r.URL.Query().Get("key") + `",
		 "actions":[{"action_id":"a2","name":"@sys/make","description":"synth","price":20,"uses":0,"rating":0}],
		 "friends":[{"handle":"@peerb","public_key":"KEYB"}],
		 "reachability":{"path":"direct","rtt_millis":4,"protocols":["/juice/fed/gossip/1"]}}`))
	})
	mux.HandleFunc("POST /control/peers/friend", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /control/peers/unfriend", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /control/identity", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"handle":"@kernel","public_key":"KEY","addrs":[]}`))
	})
	mux.HandleFunc("POST /v1/steps/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{},"tx_id":"T2","trace_id":"TR2","step_id":"` + r.PathValue("id") + `"}`))
	})
	return httptest.NewServer(mux)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// csrfFieldRe matches the hidden CSRF input rendered by csrf.TemplateField. We
// read the token from the form field (the no-JS path) because the <meta> token
// is HTML-attribute-escaped; a real browser unescapes meta.content, but a raw
// regex read would not.
var csrfFieldRe = regexp.MustCompile(`name="gorilla.csrf.Token" value="([^"]+)"`)

// loginClient performs a full PKCE login through the BFF handler and returns a
// cookie-jar client that is authenticated for subsequent requests.
func loginClient(t *testing.T, base string) *http.Client {
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

	form := url.Values{"handle": {"@alice"}, "password": {"pw"}, "gorilla.csrf.Token": {m[1]}}
	req, _ := http.NewRequest(http.MethodPost, base+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 { // followed redirect to dashboard
		t.Fatalf("login landed on status %d", resp2.StatusCode)
	}
	return cl
}

func TestLoginTabsAndNoJSFallback(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	// Default: both panels rendered, sign-in active, sign-up hidden.
	def := getBody(t, &http.Client{}, srv.URL+"/login")
	if !strings.Contains(def, `data-panel="signin"`) || !strings.Contains(def, `data-panel="signup"`) {
		t.Fatal("login must render both auth panels")
	}
	if !strings.Contains(def, `data-panel="signup" data-confirm-password hidden`) {
		t.Error("signup panel must be hidden by default (signin active)")
	}
	// Tabs are real ?tab= links (no-JS operable).
	if !strings.Contains(def, `href="/login?tab=signup"`) || !strings.Contains(def, `href="/login?tab=signin"`) {
		t.Error("tabs must be real ?tab= links for the no-JS fallback")
	}
	// ?tab=signup: signup active, signin hidden (server renders the active tab).
	up := getBody(t, &http.Client{}, srv.URL+"/login?tab=signup")
	if !strings.Contains(up, `data-panel="signin" hidden`) {
		t.Error("?tab=signup must hide the signin panel (fallback active tab)")
	}
	if strings.Contains(up, `data-panel="signup" hidden`) {
		t.Error("?tab=signup must show the signup panel")
	}
	// Each auth panel's action is the single neutral .btn (color is money/danger
	// only); a "Sign in" and a "Create account" submit exist.
	if !strings.Contains(def, `class="btn" type="submit">Sign in<`) {
		t.Error("sign-in panel must have a neutral .btn Sign in action")
	}
	if !strings.Contains(def, `class="btn" type="submit">Create account<`) {
		t.Error("signup panel must have a neutral .btn Create account action")
	}
}

// postForm posts a form (with a fresh CSRF token from /login) and returns the
// response body. Used by the signup tests.
func signupPost(t *testing.T, srv *httptest.Server, form url.Values) (int, string) {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	jar, _ := cookiejar.New(nil)
	cl.Jar = jar
	page := getBody(t, cl, srv.URL+"/login?tab=signup")
	form.Set("gorilla.csrf.Token", csrfTokenFrom(t, page))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/signup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSignupDuplicateShowsFriendlyError(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	// "dupe" email → kernel returns a raw UNIQUE-constraint 500.
	_, body := signupPost(t, srv, url.Values{
		"handle": {"@x"}, "email": {"dupe@test.com"},
		"password": {"password1"}, "confirm_password": {"password1"},
	})
	if !strings.Contains(body, "already registered") {
		t.Error("duplicate signup must show a friendly 'already registered' message")
	}
	// Must NOT show the run-outcome text or leak the raw constraint string.
	if strings.Contains(body, "fully refunded") || strings.Contains(body, "action failed to run") {
		t.Error("duplicate signup must not show the run/refund message")
	}
	if strings.Contains(body, "constraint") || strings.Contains(body, "UNIQUE") {
		t.Error("must not leak the raw kernel constraint text")
	}
	// Stays on the create-account tab.
	if !strings.Contains(body, `data-panel="signin" hidden`) {
		t.Error("error should keep the user on the signup tab")
	}
}

func TestSignupPasswordValidation(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	// Mismatch → blocked with a clear message, never reaches the kernel.
	_, mismatch := signupPost(t, srv, url.Values{
		"handle": {"@x"}, "email": {"ok@test.com"},
		"password": {"password1"}, "confirm_password": {"password2"},
	})
	if !strings.Contains(mismatch, "don&#39;t match") && !strings.Contains(mismatch, "don't match") {
		t.Errorf("mismatched passwords must be rejected: %s", firstError(mismatch))
	}
	// Too short → min-length message.
	_, short := signupPost(t, srv, url.Values{
		"handle": {"@x"}, "email": {"ok@test.com"},
		"password": {"short"}, "confirm_password": {"short"},
	})
	if !strings.Contains(short, "at least 8") {
		t.Errorf("short password must be rejected: %s", firstError(short))
	}
}

func firstError(body string) string {
	if i := strings.Index(body, "banner-error"); i >= 0 {
		return body[i:min(i+120, len(body))]
	}
	return "(no error banner)"
}

func getBody(t *testing.T, cl *http.Client, urlStr string) string {
	t.Helper()
	resp, err := cl.Get(urlStr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestOwnerOnlyAffordances(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Owner sees the management controls.
	owned := getBody(t, cl, srv.URL+"/actions/owned")
	if !strings.Contains(owned, ">Manage<") {
		t.Error("owner should see the Manage (owner) section")
	}

	// Non-owner does not.
	other := getBody(t, cl, srv.URL+"/actions/other")
	if strings.Contains(other, ">Manage<") {
		t.Error("non-owner must NOT see the Manage (owner) section")
	}
}

func TestAuthenticatedStateChangeStillNeedsCSRF(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Authenticated but no CSRF token -> rejected (R5.5).
	resp, err := cl.PostForm(srv.URL+"/actions/owned/disable", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("authenticated CSRF-less POST = %d want 403", resp.StatusCode)
	}
}

func TestTransactionsUnifiedRow(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/transactions")
	// Signed, perspective amounts: @alice is buyer on T1 (−gross) and seller on
	// T2 (+net) — the Role/Gross/Fee columns are gone (they live on the detail).
	if !strings.Contains(body, "-100") {
		t.Error("buyer transaction must show the negative gross (−100)")
	}
	if !strings.Contains(body, "+45") || !strings.Contains(body, "sig-credited") {
		t.Error("seller transaction must show the positive net (+45) as credited")
	}
	for _, gone := range []string{"<th>Role</th>", "<th>Fee</th>", "<th>Gross</th>"} {
		if strings.Contains(body, gone) {
			t.Errorf("transactions list must not carry the %q column", gone)
		}
	}
	// The id links to the record; the action ref links to the action (owner
	// emphasized via _ref, so it isn't a contiguous substring).
	if !strings.Contains(body, `href="/transactions/T1"`) {
		t.Error("transaction id must link to the record")
	}
	if !strings.Contains(body, `href="/actions/owned"`) {
		t.Error("action ref must link to the action")
	}
	if !strings.Contains(body, `class="owner">@alice`) {
		t.Error("action ref must render via _ref with owner emphasized")
	}
	// The Amount column is right-aligned, so its header is too (class="money").
	if !strings.Contains(body, `<th class="money">Amount</th>`) {
		t.Error("the Amount header must be right-aligned (class=\"money\") over its values")
	}
}

// The user ledger and the tx index share _tx_row, so the ledger now carries the
// action ref (previously missing) and the same signed amounts.
func TestUserLedgerUsesUnifiedRow(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/user")
	if !strings.Contains(body, `href="/actions/owned"`) {
		t.Error("user ledger must link the action ref (was missing before)")
	}
	if !strings.Contains(body, "-100") || !strings.Contains(body, "+45") {
		t.Error("user ledger must show the signed perspective amounts")
	}
	if strings.Contains(body, "<th>Gross</th>") {
		t.Error("user ledger must not carry the Gross column")
	}
}

func TestTransactionDetailShowsActionAndParties(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/transactions/T1")
	// Action ref with owner emphasis
	if !strings.Contains(body, `class="owner">@alice`) {
		t.Error("transaction detail must show the action ref")
	}
	// Buyer: current user (@alice) is the buyer party
	if !strings.Contains(body, "@alice") {
		t.Error("transaction detail must show the buyer handle when current user is buyer")
	}
}

func TestWalletShowsBalancesAndDepositNote(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/user")
	if !strings.Contains(body, "500") {
		t.Error("wallet should show available balance")
	}
	// R3.3.1: must state plainly that top-ups happen out-of-band.
	if !strings.Contains(strings.ToLower(body), "top-up") && !strings.Contains(strings.ToLower(body), "out-of-band") {
		t.Error("wallet must explain credit top-ups happen out-of-band")
	}
}

// csrfTokenFrom extracts the CSRF token from any page body.
func csrfTokenFrom(t *testing.T, body string) string {
	t.Helper()
	m := csrfFieldRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no csrf token in page")
	}
	return m[1]
}

func TestStudioPageTwoZones(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/actions")
	// Creation zone: AI synthesis form present (R6.3.6).
	if !strings.Contains(body, "Synthesize") {
		t.Error("studio must show AI synthesis creation zone")
	}
	if !strings.Contains(body, "/actions/make") {
		t.Error("studio must have the @sys/make form action")
	}
	// Manual creation form present.
	if !strings.Contains(body, "/actions") {
		t.Error("studio must have the manual creation form")
	}
	// Action management list: both active+public "hello" and inactive+private "draft" appear.
	if !strings.Contains(body, "hello") {
		t.Error("studio must list active public actions in the management zone")
	}
	if !strings.Contains(body, "draft") {
		t.Error("studio must list inactive/private own actions (R10.3 resolved)")
	}
	// Inactive status visible for the draft action (the inactive chip).
	if !strings.Contains(body, "inactive") {
		t.Error("studio must show the inactive status for an inactive action")
	}
}

func TestUserShowsGrants(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	body := getBody(t, cl, srv.URL+"/user")

	if !strings.Contains(body, "Connected accounts") {
		t.Error("/user must show the connected accounts section")
	}
	// The grant from /v1/me renders via _ref with a disconnect form.
	if !strings.Contains(body, `class="owner">@sys`) {
		t.Error("/user must list the grant's action ref")
	}
	if !strings.Contains(body, `action="/grants/disconnect"`) || !strings.Contains(body, `value="@sys/gmail-send"`) {
		t.Error("/user must offer a disconnect for each grant")
	}
}

func TestKernelIdentityShown(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	// Login page (unauthenticated) names the kernel and shows the key fingerprint.
	login := getBody(t, &http.Client{}, srv.URL+"/login")
	if !strings.Contains(login, "Sign in to") || !strings.Contains(login, "@testnet") {
		t.Error("login page must name the kernel it's joining (@testnet)")
	}
	if !strings.Contains(login, "ABCDEFGHIJ…abcd") {
		t.Error("login page must show the kernel key fingerprint for verification")
	}
	// The sidebar brand shows the kernel handle once signed in.
	cl := loginClient(t, srv.URL)
	if !strings.Contains(getBody(t, cl, srv.URL+"/run"), `class="lbl">@testnet`) {
		t.Error("sidebar brand must show the kernel handle")
	}
}

func TestActionCardChips(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	body := getBody(t, cl, srv.URL+"/actions")

	// Ref is the card title/link, rendered via _ref (owner emphasized).
	if !strings.Contains(body, `class="action-card-ref mono" href="/actions/A1"`) {
		t.Error("action card must use the ref as the title link")
	}
	if !strings.Contains(body, `class="owner">@alice`) {
		t.Error("card ref must render via _ref with owner emphasized")
	}
	// Kind chip: both fixtures are wasm → a W chip.
	if !strings.Contains(body, `class="chip" title="WebAssembly action">W`) {
		t.Error("action card must show the kind chip (W for wasm)")
	}
	// A3 (@alice/draft) is private + inactive: lock chip, dimmed card, inactive chip.
	if !strings.Contains(body, `/static/icons.svg#lock`) {
		t.Error("a private action must show the lock chip")
	}
	if !strings.Contains(body, `class="card action-card inactive"`) {
		t.Error("an inactive action's card must be dimmed (inactive class)")
	}
	if !strings.Contains(body, `class="chip tag" title="Inactive — not callable">inactive`) {
		t.Error("an inactive action must show the inactive chip")
	}
	// The old cluttered markup is gone.
	for _, gone := range []string{"action-card-name", "action-card-meta", "remote_proxy"} {
		if strings.Contains(body, gone) {
			t.Errorf("action card must not contain legacy %q", gone)
		}
	}
}

func TestActionsCommandAndFilters(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	body := getBody(t, cl, srv.URL+"/actions")

	// Content-first: no floating command chrome, and color stays money/danger only.
	if strings.Contains(body, `class="toolbar"`) {
		t.Error("actions page must not carry a page-head toolbar")
	}
	if strings.Contains(body, "btn-primary") {
		t.Error("actions page must not use the retired btn-primary tier")
	}

	// The create command is one quiet inline "+ new action" menu trigger opening
	// the four creation methods (each a real ?form= link).
	if !strings.Contains(body, `id="menu-new-action"`) || !strings.Contains(body, "+ new action") {
		t.Error("actions create command must be a quiet + new action menu")
	}
	if !strings.Contains(body, `class="btn-quiet tabs-action" data-menu="menu-new-action"`) {
		t.Error("+ new action must be a quiet menu trigger on the tab row, not a solid button")
	}
	for _, form := range []string{"action-create", "action-make", "action-import", "action-unimport"} {
		if !strings.Contains(body, "?form="+form) {
			t.Errorf("+ new action menu missing the %q method", form)
		}
	}

	// Filters are the unified .tabs view-switch component (not the retired linkbar).
	if strings.Contains(body, "linkbar") {
		t.Error("filters must use .tabs, not the retired .linkbar")
	}
	if !strings.Contains(body, `class="tabs control-tabs"`) {
		t.Error("action filters must render as a .tabs row")
	}
	if !strings.Contains(body, `class="tab active" href="/actions" aria-current="page">all`) {
		t.Error("action filter tabs must mark 'all' active")
	}
	if !strings.Contains(body, `href="/actions?filter=active"`) {
		t.Error("action filter tabs must offer the 'active' filter")
	}
}

// The superuser ownership scope is its own switch (yours | everyone), separate
// from the state-filter row — so the old "all actions" tab that double-lit with
// "all" is gone, and the two axes preserve each other.
func TestActionsScopeSwitch(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "@sys")
	body := getBody(t, cl, srv.URL+"/actions?scope=all")

	if strings.Contains(body, ">all actions<") {
		t.Error("the confusing 'all actions' filter tab must be gone (scope is its own switch)")
	}
	if !strings.Contains(body, `>everyone</a>`) || !strings.Contains(body, `>yours</a>`) {
		t.Error("superuser must get a yours|everyone scope switch")
	}
	// everyone active on ?scope=all…
	if !strings.Contains(body, `class="tab active" href="/actions?scope=all" aria-current="page">everyone`) {
		t.Error("the 'everyone' scope tab must be active on ?scope=all")
	}
	// …and the state-filter tabs preserve the scope (carry scope=all; html/template
	// escapes the & in the href).
	if !strings.Contains(body, `href="/actions?filter=active&amp;scope=all"`) {
		t.Error("filter tabs must preserve the scope (carry scope=all)")
	}
}

func TestStudioMakeActionSuccess(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Get CSRF token from studio page.
	body := getBody(t, cl, srv.URL+"/actions")
	tok := csrfTokenFrom(t, body)

	// POST to /studio/make with htmx headers.
	form := url.Values{"description": {"Return a fixed greeting"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/make", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	fragment, _ := io.ReadAll(resp.Body)
	frag := string(fragment)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("make returned %d, body: %s", resp.StatusCode, frag)
	}
	// Success fragment: link to the created action and its name.
	if !strings.Contains(frag, "synthesized") {
		t.Error("success fragment must include the synthesized action name")
	}
	if !strings.Contains(frag, "/actions/A2") {
		t.Error("success fragment must link to the created action")
	}
}

func TestStudioMakeActionInsufficientCredits(t *testing.T) {
	// Kernel that returns only 5 credits — not enough for @sys/make.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"@alice","available":5,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Studio page shows the synthesis form regardless of balance; the kernel
	// enforces the credit check at run time (v0.3 behaviour).
	body := getBody(t, cl, srv.URL+"/actions")
	if !strings.Contains(body, `action="/actions/make"`) {
		t.Error("synthesis form must always be shown (kernel enforces credit check on run)")
	}
	// The page must surface the user's current balance so they can see they're short.
	if !strings.Contains(body, "5") {
		t.Error("studio must show the user's current available balance")
	}
}

func TestStudioLifecycleRedirect(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Get CSRF token.
	body := getBody(t, cl, srv.URL+"/actions")
	tok := csrfTokenFrom(t, body)

	// Disable action A1: must redirect to the action detail page.
	form := url.Values{"gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/A1/disable", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	noRedirect := &http.Client{
		Jar: cl.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("lifecycle POST returned %d, expected 303: %s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/actions/A1") {
		t.Errorf("lifecycle redirect must go to action detail page, got Location=%q", loc)
	}
}

func TestStepsInboxShowsWaitingStep(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/steps")

	// S1 is waiting and requires U1 (the signed-in user) → must appear in Inbox.
	if !strings.Contains(body, "Waiting for you") {
		t.Error("steps page must have an Inbox section")
	}
	if !strings.Contains(body, `action="/steps/S1/complete"`) {
		t.Error("inbox must contain the step completion form for S1")
	}
}

func TestInboxGroupsByCreatingAction(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/steps")

	// Two waiting steps requested by two different creating actions → the inbox
	// groups them under a heading naming each creating action (not the human caller).
	if strings.Count(body, `class="inbox-group"`) != 2 {
		t.Error("inbox must group waiting steps by creating action when there is more than one")
	}
	// _ref splits owner/name with markup, so assert the distinguishing name fragments.
	if !strings.Contains(body, "/dispatch") || !strings.Contains(body, "/relay") {
		t.Errorf("each group heading must name its creating action: %s", body)
	}
	if strings.Count(body, "Requested by") != 2 {
		t.Errorf("each group renders a 'Requested by <action>' heading: %s", body)
	}
	// Both completion forms are still present, one per step.
	if !strings.Contains(body, `action="/steps/S1/complete"`) || !strings.Contains(body, `action="/steps/S2/complete"`) {
		t.Error("every waiting step keeps its own completion form")
	}
}

func TestCompleteStepHTMX(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/steps")
	tok := csrfTokenFrom(t, body)

	form := url.Values{"raw_args": {"{}"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/steps/S1/complete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	fragment, _ := io.ReadAll(resp.Body)
	frag := string(fragment)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete step returned %d, body: %s", resp.StatusCode, frag)
	}
	if !strings.Contains(frag, "Success") {
		t.Error("complete step HTMX response must show success outcome")
	}
	if strings.Contains(frag, "<html") {
		t.Error("HTMX complete step must return a fragment, not a full page")
	}
}

func TestInboxPageRendersSteps(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/steps")

	// Step S1 (status=waiting, required_caller=@alice=current user) appears as a
	// completable card showing its parked price.
	if !strings.Contains(body, "parked") {
		t.Error("inbox must attribute the parked price on the waiting step card")
	}
	if !strings.Contains(body, "Complete") {
		t.Error("inbox page must show the Complete button for own waiting steps")
	}
	// Old inline step-create endpoint must be absent (creation is a modal to POST /steps).
	if strings.Contains(body, "/steps/create") {
		t.Error("inbox page must not use the old step-create endpoint")
	}
}

func TestWalletShowsLedger(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/user")
	// Ledger must show both transactions from authKernel.
	if !strings.Contains(body, "T1") && !strings.Contains(body, "/transactions/T1") {
		t.Error("wallet ledger must link to transaction T1")
	}
	if !strings.Contains(body, "T2") && !strings.Contains(body, "/transactions/T2") {
		t.Error("wallet ledger must link to transaction T2")
	}
	// Roles are shown on the dedicated transactions ledger (the account page keeps
	// a compact ledger without per-row roles).
	txBody := getBody(t, cl, srv.URL+"/transactions")
	if !strings.Contains(txBody, "buyer") {
		t.Error("transactions ledger must show 'buyer' role for T1")
	}
	if !strings.Contains(txBody, "seller") {
		t.Error("transactions ledger must show 'seller' role for T2")
	}
}

func TestStudioShowsEarnings(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/actions")
	// T2 in authKernel is a seller transaction for U1 (target_user_id=U1, status=success).
	// Earnings card must appear.
	if !strings.Contains(body, "Earnings") {
		t.Error("studio must show Earnings section when seller transactions exist")
	}
	if !strings.Contains(strings.ToLower(body), "net received") {
		t.Error("studio Earnings card must show Net received label")
	}
}

func TestUpdateActionPrice(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/actions/A1")
	tok := csrfTokenFrom(t, body)

	form := url.Values{"gorilla.csrf.Token": {tok}, "price": {"50"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/A1/update-price", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update-price returned %d, want 200 (after redirect)", resp.StatusCode)
	}
	page, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(page), "Price updated") {
		t.Error("update-price must show flash message containing 'Price updated'")
	}
}

// actionDetailKernel serves a single owned action A1 (given as raw JSON) plus the
// endpoints the action page and post-login dashboard need.
func actionDetailKernel(actionJSON string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"@alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/A1", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(actionJSON)) })
	return mux
}

func TestActionDetailShowsHTTPAndEditForm(t *testing.T) {
	kernel := httptest.NewServer(actionDetailKernel(
		`{"id":"A1","owner_user_id":"U1","owner_handle":"@alice","name":"weather","kind":"http",` +
			`"active":true,"public":true,"price":0,"description":"Weather lookup",` +
			`"input_schema":{"type":"object"},"output_schema":{"type":"string"},` +
			`"http":{"method":"GET","url":"https://api.x/{city}","params":[{"name":"city","in":"path"},{"name":"units","in":"query"}]}}`))
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/actions/A1")

	// Decomposed http object is rendered inertly.
	for _, want := range []string{"GET", "https://api.x/{city}", "city:path", "units:query"} {
		if !strings.Contains(page, want) {
			t.Errorf("action page missing %q", want)
		}
	}
	// General edit form: routed to /update, with prefilled schemas + http subsection.
	for _, want := range []string{
		`action="/actions/A1/update"`,
		`name="input_schema"`,
		`name="output_schema"`,
		`name="source"`,
		"Saving deactivates the action",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("edit form missing %q", want)
		}
	}
	// Schema textareas are prefilled from the action's current schemas (indented,
	// with quotes HTML-escaped by the textarea RCDATA context).
	if !strings.Contains(page, `&#34;type&#34;: &#34;object&#34;`) || !strings.Contains(page, `&#34;type&#34;: &#34;string&#34;`) {
		t.Error("schema textareas must be prefilled with the current schemas")
	}
	// Security (§8): the counterparty URL must not become an auto-link.
	if strings.Contains(page, `href="https://api.x`) {
		t.Error("counterparty URL must render inert, never as a link")
	}
}

func TestActionDetailWasmEditFormHasSchemasNoHTTP(t *testing.T) {
	kernel := httptest.NewServer(actionDetailKernel(
		`{"id":"A1","owner_user_id":"U1","owner_handle":"@alice","name":"calc","kind":"wasm",` +
			`"active":true,"public":true,"price":0,"input_schema":{"type":"object"}}`))
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/actions/A1")

	// Schema editing is available for wasm too.
	if !strings.Contains(page, `action="/actions/A1/update"`) || !strings.Contains(page, `name="input_schema"`) {
		t.Error("wasm action must offer the general edit form with schema fields")
	}
	// But no http-only fields (method/params/source) for a non-http action.
	for _, absent := range []string{`name="method"`, `name="param_name"`} {
		if strings.Contains(page, absent) {
			t.Errorf("wasm edit form must not contain http field %q", absent)
		}
	}
}

func TestLoginPageHasNoSidebar(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b := string(body)
	if strings.Contains(b, "<aside") {
		t.Error("login page must not render the sidebar")
	}
	if strings.Contains(b, `class="topbar"`) {
		t.Error("login page must not render the topbar")
	}
}

func TestSettingsPage(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/user")
	if !strings.Contains(body, "alice@test.com") {
		t.Error("settings page must show current email")
	}
	if !strings.Contains(body, "@alice") {
		t.Error("settings page must show handle")
	}
	tok := csrfTokenFrom(t, body)

	// Email update — success path.
	form := url.Values{"gorilla.csrf.Token": {tok}, "email": {"new@test.com"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/user", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings email update: want 200, got %d", resp.StatusCode)
	}

	// Password change — wrong current password shows inline error, no redirect.
	// The new password is valid locally (≥8, confirmed) so it reaches the kernel,
	// which rejects the wrong current_password.
	body2 := getBody(t, cl, srv.URL+"/user")
	tok2 := csrfTokenFrom(t, body2)
	form2 := url.Values{
		"gorilla.csrf.Token": {tok2},
		"current_password":   {"wrongpass"},
		"password":           {"newpassword"},
		"confirm_password":   {"newpassword"},
	}
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/user", strings.NewReader(form2.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := cl.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	page2, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(page2), "incorrect") {
		t.Error("wrong current_password must show 'incorrect' error inline")
	}
}

func TestParseLookup(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"results": []map[string]any{
			{"action_id": "A1", "name": "hello", "owner_handle": "@alice", "description": "greet", "score": 0.9},
			{"action_id": "A2", "name": "bye", "owner_handle": "@bob", "description": "", "score": 0.5},
		},
	})
	got := parseLookup(raw)
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].Action.ID != "A1" || got[0].Action.Name != "hello" || got[0].Action.OwnerHandle != "@alice" {
		t.Errorf("first result fields wrong: %+v", got[0].Action)
	}
	if got[0].Action.Kind != "" {
		t.Error("parseLookup must leave Kind empty so the goroutine fetches it via GetAction")
	}
}

func TestCatalogAsResults(t *testing.T) {
	actions := []client.Action{
		{ID: "A1", Name: "hello", OwnerHandle: "@alice", Kind: "wasm", Active: true, Public: true, Price: 50},
		{ID: "A2", Name: "goodbye", OwnerHandle: "@bob", Kind: "ai", Active: true, Public: true, Price: 0},
	}
	got := catalogAsResults(actions, "hello")
	if len(got) != 1 {
		t.Fatalf("want 1 filtered result, got %d", len(got))
	}
	if got[0].Action.Price != 50 {
		t.Errorf("catalogAsResults must preserve full Action including Price, got %d", got[0].Action.Price)
	}
	if got[0].Action.Kind != "wasm" {
		t.Errorf("catalogAsResults must preserve Kind, got %q", got[0].Action.Kind)
	}

	all := catalogAsResults(actions, "")
	if len(all) != 2 {
		t.Fatalf("empty query should return all %d actions, got %d", len(actions), len(all))
	}
}

// TestCreateActionAuth verifies that postCreateAction forwards the correct auth
// JSON for each supported scheme, and omits the field when no scheme is chosen.
func TestCreateActionAuth(t *testing.T) {
	cases := []struct {
		name   string
		fields url.Values
		// wantAuth is the expected parsed auth object; nil means auth must be absent.
		wantScheme  string
		wantSecrets map[string]string
		wantConfig  map[string]string
	}{
		{
			name:       "no scheme",
			fields:     url.Values{"name": {"act1"}, "kind": {"http"}, "auth_scheme": {""}},
			wantScheme: "",
		},
		{
			name:        "bearer",
			fields:      url.Values{"name": {"act2"}, "kind": {"http"}, "auth_scheme": {"bearer"}, "auth_bearer_token": {"tok123"}},
			wantScheme:  "bearer",
			wantSecrets: map[string]string{"token": "tok123"},
		},
		{
			name:        "basic",
			fields:      url.Values{"name": {"act3"}, "kind": {"http"}, "auth_scheme": {"basic"}, "auth_basic_username": {"user"}, "auth_basic_password": {"pass"}},
			wantScheme:  "basic",
			wantSecrets: map[string]string{"username": "user", "password": "pass"},
		},
		{
			name:        "header",
			fields:      url.Values{"name": {"act4"}, "kind": {"http"}, "auth_scheme": {"header"}, "auth_param_name": {"X-Api-Key"}, "auth_param_value": {"hval"}},
			wantScheme:  "header",
			wantConfig:  map[string]string{"name": "X-Api-Key"},
			wantSecrets: map[string]string{"value": "hval"},
		},
		{
			name:        "query",
			fields:      url.Values{"name": {"act5"}, "kind": {"http"}, "auth_scheme": {"query"}, "auth_param_name": {"api_key"}, "auth_param_value": {"qval"}},
			wantScheme:  "query",
			wantConfig:  map[string]string{"name": "api_key"},
			wantSecrets: map[string]string{"value": "qval"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"redirect":"?code=CODE"}`))
			})
			mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
			})
			mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"id":"U1","handle":"@alice","available":500,"locked":0}`))
			})
			mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`[]`))
			})
			mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`[]`))
			})
			mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{"id":"A9","owner_user_id":"U1","owner_handle":"@alice","name":"act","kind":"http","active":false,"public":false,"price":0}`))
			})
			kernel := httptest.NewServer(mux)
			defer kernel.Close()
			srv := httptest.NewServer(newTestHandler(t, kernel.URL))
			defer srv.Close()
			cl := loginClient(t, srv.URL)

			studioBody := getBody(t, cl, srv.URL+"/actions")
			csrfTok := csrfTokenFrom(t, studioBody)
			tc.fields.Set("gorilla.csrf.Token", csrfTok)

			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions", strings.NewReader(tc.fields.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := cl.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			if gotBody == nil {
				t.Fatal("kernel POST /v1/actions was not called")
			}

			var parsed struct {
				Scheme  string            `json:"scheme"`
				Config  map[string]string `json:"config"`
				Secrets map[string]string `json:"secrets"`
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(gotBody, &body); err != nil {
				t.Fatalf("kernel received invalid JSON: %v", err)
			}

			authRaw, hasAuth := body["auth"]
			if tc.wantScheme == "" {
				if hasAuth {
					t.Errorf("auth must be absent when no scheme is selected, got: %s", authRaw)
				}
				return
			}
			if !hasAuth {
				t.Fatalf("auth field missing in kernel request for scheme %q", tc.wantScheme)
			}
			if err := json.Unmarshal(authRaw, &parsed); err != nil {
				t.Fatalf("auth JSON invalid: %v", err)
			}
			if parsed.Scheme != tc.wantScheme {
				t.Errorf("scheme: want %q, got %q", tc.wantScheme, parsed.Scheme)
			}
			for k, v := range tc.wantSecrets {
				if parsed.Secrets[k] != v {
					t.Errorf("secrets[%q]: want %q, got %q", k, v, parsed.Secrets[k])
				}
			}
			for k, v := range tc.wantConfig {
				if parsed.Config[k] != v {
					t.Errorf("config[%q]: want %q, got %q", k, v, parsed.Config[k])
				}
			}
		})
	}
}

// actionCreateKernel returns a kernel mux that captures the POST /v1/actions body.
func actionCreateKernel(gotBody *[]byte) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"@alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		*gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"A9","owner_user_id":"U1","owner_handle":"@alice","name":"act","kind":"http"}`))
	})
	return mux
}

func TestCreateActionHTTPParams(t *testing.T) {
	cases := []struct {
		name       string
		fields     url.Values
		wantMethod string
		wantParams []client.HTTPParam
	}{
		{
			name:       "method and two params",
			fields:     url.Values{"name": {"weather"}, "kind": {"http"}, "source": {"https://api.x/{city}"}, "method": {"GET"}, "param_name": {"city", "units"}, "param_in": {"path", "query"}},
			wantMethod: "GET",
			wantParams: []client.HTTPParam{{Name: "city", In: "path"}, {Name: "units", In: "query"}},
		},
		{
			name:       "blank name row skipped, in defaults to body",
			fields:     url.Values{"name": {"w2"}, "kind": {"http"}, "method": {"POST"}, "param_name": {"", "note"}, "param_in": {"path", ""}},
			wantMethod: "POST",
			wantParams: []client.HTTPParam{{Name: "note", In: "body"}},
		},
		{
			name:       "no rows no method omits both",
			fields:     url.Values{"name": {"plain"}, "kind": {"http"}},
			wantMethod: "",
			wantParams: nil,
		},
		{
			name:       "wasm ignores method and params",
			fields:     url.Values{"name": {"w"}, "kind": {"wasm"}, "method": {"GET"}, "param_name": {"x"}, "param_in": {"query"}},
			wantMethod: "",
			wantParams: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			kernel := httptest.NewServer(actionCreateKernel(&gotBody))
			defer kernel.Close()
			srv := httptest.NewServer(newTestHandler(t, kernel.URL))
			defer srv.Close()
			cl := loginClient(t, srv.URL)

			csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/actions"))
			tc.fields.Set("gorilla.csrf.Token", csrfTok)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions", strings.NewReader(tc.fields.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := cl.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			if gotBody == nil {
				t.Fatal("kernel POST /v1/actions was not called")
			}
			var sent client.CreateActionInput
			if err := json.Unmarshal(gotBody, &sent); err != nil {
				t.Fatalf("kernel received invalid JSON: %v", err)
			}
			if sent.Method != tc.wantMethod {
				t.Errorf("method: want %q, got %q", tc.wantMethod, sent.Method)
			}
			if !reflect.DeepEqual(sent.Params, tc.wantParams) {
				t.Errorf("params: want %+v, got %+v", tc.wantParams, sent.Params)
			}
		})
	}
}

// updateActionKernel drives login and captures the PUT /v1/actions/A1 body
// (nil until the kernel is called).
func updateActionKernel(gotBody *[]byte) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"@alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("PUT /v1/actions/A1", func(w http.ResponseWriter, r *http.Request) {
		*gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"id":"A1","owner_handle":"@alice","name":"weather","kind":"http","active":false}`))
	})
	return mux
}

func TestUpdateAction(t *testing.T) {
	var gotBody []byte
	kernel := httptest.NewServer(updateActionKernel(&gotBody))
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// A CSRF token is valid session-wide; any page with a form yields one.
	csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/actions"))
	fields := url.Values{
		"gorilla.csrf.Token": {csrfTok},
		"description":        {"Weather lookup"},
		"input_schema":       {`{"type":"object"}`},
		"output_schema":      {`{"type":"string"}`},
		"source":             {"https://api.x/{city}"},
		"method":             {"PUT"},
		"param_name":         {"city", "units"},
		"param_in":           {"path", "query"},
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/A1/update", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotBody == nil {
		t.Fatal("kernel PUT /v1/actions/A1 was not called")
	}
	var sent client.UpdateActionInput
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("kernel received invalid JSON: %v", err)
	}
	if sent.Description == nil || *sent.Description != "Weather lookup" {
		t.Errorf("description = %v", sent.Description)
	}
	if string(sent.InputSchema) != `{"type":"object"}` || string(sent.OutputSchema) != `{"type":"string"}` {
		t.Errorf("schemas = %s / %s", sent.InputSchema, sent.OutputSchema)
	}
	if sent.Source == nil || *sent.Source != "https://api.x/{city}" {
		t.Errorf("source = %v", sent.Source)
	}
	if sent.Method != "PUT" {
		t.Errorf("method = %q", sent.Method)
	}
	want := []client.HTTPParam{{Name: "city", In: "path"}, {Name: "units", In: "query"}}
	if !reflect.DeepEqual(sent.Params, want) {
		t.Errorf("params: want %+v, got %+v", want, sent.Params)
	}
}

func TestUpdateActionInvalidSchemaRejected(t *testing.T) {
	var gotBody []byte
	kernel := httptest.NewServer(updateActionKernel(&gotBody))
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/actions"))
	fields := url.Values{
		"gorilla.csrf.Token": {csrfTok},
		"input_schema":       {`{not json`},
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/A1/update", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if gotBody != nil {
		t.Error("invalid schema must not reach the kernel")
	}
	if !strings.Contains(string(page), "not valid JSON") {
		t.Error("invalid schema must surface a flash error")
	}
}
