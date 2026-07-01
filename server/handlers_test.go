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
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"@alice","email":"alice@test.com","available":500,"locked":0}`))
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
		// T1: U1 is buyer; T2: U1 is seller. Both reference action "owned".
		w.Write([]byte(`[
			{"id":"T1","action_id":"owned","owner_user_id":"U1","target_user_id":"U2","caller_user_id":"U1","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"},
			{"id":"T2","action_id":"owned","owner_user_id":"U2","target_user_id":"U1","caller_user_id":"U2","status":"success","gross":50,"fee":5,"net":45,"started_at":"2024-01-02T00:00:00Z"}
		]`))
	})
	mux.HandleFunc("GET /v1/stats/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"action_id":"x"}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		// Authenticated owner query returns all own actions (active+public and inactive/private).
		w.Write([]byte(`[` +
			`{"id":"A1","owner_user_id":"U1","owner_handle":"@alice","name":"hello","kind":"wasm","active":true,"public":true,"price":0},` +
			`{"id":"A3","owner_user_id":"U1","owner_handle":"@alice","name":"draft","kind":"wasm","active":false,"public":false,"price":0}` +
			`]`))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		// "owned" is owned by U1 (the signed-in user); anything else by U2.
		owner := "U2"
		active := true
		public := true
		if r.PathValue("id") == "owned" || r.PathValue("id") == "A1" {
			owner = "U1"
		}
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","action":"@alice/x","owner_user_id":"` + owner +
			`","owner_handle":"@alice","name":"x","kind":"http",` +
			`"active":` + boolStr(active) + `,"public":` + boolStr(public) + `,"price":0,` +
			`"input_schema":{"type":"object","properties":{}},"output_schema":{"type":"object"}}`))
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"status":"success","action_id":"A2","action_name":"synthesized","diagnostics":[]},"tx_id":"TX1","trace_id":"TR1"}`))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"T1","action_id":"owned","owner_user_id":"U1","target_user_id":"U2","caller_user_id":"U1","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/enable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"active":true}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/disable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"active":false}`))
	})
	mux.HandleFunc("PUT /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","owner_user_id":"U1","owner_handle":"@alice","name":"x","kind":"http","active":true,"public":true,"price":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"S1","required_caller_user_id":"U1","action_id":"A1","price":5,"status":"waiting","created_at":"2026-01-01T00:00:00Z"}]`))
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
	if !strings.Contains(owned, "Manage (owner)") {
		t.Error("owner should see the Manage (owner) section")
	}

	// Non-owner does not.
	other := getBody(t, cl, srv.URL+"/actions/other")
	if strings.Contains(other, "Manage (owner)") {
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

func TestTransactionsShowsBuyerAndSellerRoles(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/transactions")
	if !strings.Contains(body, "buyer") {
		t.Error("transactions page must show 'buyer' role for transactions where I am owner_user_id")
	}
	if !strings.Contains(body, "seller") {
		t.Error("transactions page must show 'seller' role for transactions where I am target_user_id")
	}
	// Action ref should appear (action "owned" has owner_handle "@alice", name "x")
	if !strings.Contains(body, "@alice/x") {
		t.Error("transactions page must show the action ref (@owner/name) for each transaction")
	}
}

func TestTransactionDetailShowsActionAndParties(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/transactions/T1")
	// Action ref
	if !strings.Contains(body, "@alice/x") {
		t.Error("transaction detail must show the action ref")
	}
	// Buyer: current user (U1 = @alice) is the buyer
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

	body := getBody(t, cl, srv.URL+"/wallet")
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

	body := getBody(t, cl, srv.URL+"/studio")
	// Creation zone: AI synthesis form present (R6.3.6).
	if !strings.Contains(body, "Synthesize with AI") {
		t.Error("studio must show AI synthesis creation zone")
	}
	if !strings.Contains(body, "/studio/make") {
		t.Error("studio must have the @sys/make form action")
	}
	// Manual creation form present.
	if !strings.Contains(body, "/studio/create") {
		t.Error("studio must have the manual creation form")
	}
	// Action management list: both active+public "hello" and inactive+private "draft" appear.
	if !strings.Contains(body, "hello") {
		t.Error("studio must list active public actions in the management zone")
	}
	if !strings.Contains(body, "draft") {
		t.Error("studio must list inactive/private own actions (R10.3 resolved)")
	}
	// Inactive status visible for the draft action (shown in card meta line).
	if !strings.Contains(body, "inactive") {
		t.Error("studio must show the inactive status for an inactive action")
	}
}

func TestStudioMakeActionSuccess(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Get CSRF token from studio page.
	body := getBody(t, cl, srv.URL+"/studio")
	tok := csrfTokenFrom(t, body)

	// POST to /studio/make with htmx headers.
	form := url.Values{"description": {"Return a fixed greeting"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/studio/make", strings.NewReader(form.Encode()))
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
	body := getBody(t, cl, srv.URL+"/studio")
	if !strings.Contains(body, `action="/studio/make"`) {
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
	body := getBody(t, cl, srv.URL+"/studio")
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
	if !strings.Contains(body, "Inbox") {
		t.Error("steps page must have an Inbox section")
	}
	if !strings.Contains(body, `action="/steps/S1/complete"`) {
		t.Error("inbox must contain the step completion form for S1")
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

	// Step S1 (status=waiting, required_caller=U1=current user) must appear with complete form.
	if !strings.Contains(body, "waiting") {
		t.Error("inbox page must show steps with status")
	}
	if !strings.Contains(body, "Complete") {
		t.Error("inbox page must show the Complete button for own waiting steps")
	}
	// Create form must be absent.
	if strings.Contains(body, "/steps/create") {
		t.Error("inbox page must not have a step-create form")
	}
}

func TestWalletShowsLedger(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/wallet")
	// Ledger must show both transactions from authKernel.
	if !strings.Contains(body, "T1") && !strings.Contains(body, "/transactions/T1") {
		t.Error("wallet ledger must link to transaction T1")
	}
	if !strings.Contains(body, "T2") && !strings.Contains(body, "/transactions/T2") {
		t.Error("wallet ledger must link to transaction T2")
	}
	// Roles must be present.
	if !strings.Contains(body, "buyer") {
		t.Error("wallet ledger must show 'buyer' role for T1")
	}
	if !strings.Contains(body, "seller") {
		t.Error("wallet ledger must show 'seller' role for T2")
	}
}

func TestStudioShowsEarnings(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/studio")
	// T2 in authKernel is a seller transaction for U1 (target_user_id=U1, status=success).
	// Earnings card must appear.
	if !strings.Contains(body, "Earnings") {
		t.Error("studio must show Earnings section when seller transactions exist")
	}
	if !strings.Contains(body, "Net received") {
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

	body := getBody(t, cl, srv.URL+"/settings")
	if !strings.Contains(body, "alice@test.com") {
		t.Error("settings page must show current email")
	}
	if !strings.Contains(body, "@alice") {
		t.Error("settings page must show handle")
	}
	tok := csrfTokenFrom(t, body)

	// Email update — success path.
	form := url.Values{"gorilla.csrf.Token": {tok}, "email": {"new@test.com"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/settings", strings.NewReader(form.Encode()))
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
	body2 := getBody(t, cl, srv.URL+"/settings")
	tok2 := csrfTokenFrom(t, body2)
	form2 := url.Values{
		"gorilla.csrf.Token": {tok2},
		"current_password":   {"wrongpass"},
		"password":           {"newpass"},
	}
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/settings", strings.NewReader(form2.Encode()))
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

			studioBody := getBody(t, cl, srv.URL+"/studio")
			csrfTok := csrfTokenFrom(t, studioBody)
			tc.fields.Set("gorilla.csrf.Token", csrfTok)

			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/studio/create", strings.NewReader(tc.fields.Encode()))
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

			csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/studio"))
			tc.fields.Set("gorilla.csrf.Token", csrfTok)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/studio/create", strings.NewReader(tc.fields.Encode()))
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
	csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/studio"))
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

	csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/studio"))
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
