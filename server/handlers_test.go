package server

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
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
		w.Write([]byte(`{"id":"U1","handle":"@alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		// T1: U1 is buyer; T2: U1 is seller. Both reference action "owned".
		w.Write([]byte(`[
			{"id":"T1","action_id":"owned","owner_user_id":"U1","target_user_id":"U2","subject_user_id":"U1","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"},
			{"id":"T2","action_id":"owned","owner_user_id":"U2","target_user_id":"U1","subject_user_id":"U2","status":"success","gross":50,"fee":5,"net":45,"started_at":"2024-01-02T00:00:00Z"}
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
	mux.HandleFunc("POST /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"process_id":"P1","available":20,"locked":0,"status":"open"}`))
	})
	mux.HandleFunc("POST /v1/processes/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/call", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"status":"success","action_id":"A2","action_name":"synthesized","diagnostics":[]},"tx_id":"TX1","trace_id":"TR1"}`))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"T1","action_id":"owned","owner_user_id":"U1","target_user_id":"U2","subject_user_id":"U1","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/enable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"active":true}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/disable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"active":false}`))
	})
	mux.HandleFunc("POST /v1/actions/{id}/grant-all", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/actions/{id}/revoke-all", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
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

func TestDashboardShowsBalancesAndDepositNote(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/")
	if !strings.Contains(body, "500") {
		t.Error("dashboard should show available balance")
	}
	// R3.3.1: must state plainly that top-ups happen out-of-band.
	if !strings.Contains(strings.ToLower(body), "top-up") && !strings.Contains(strings.ToLower(body), "out-of-band") {
		t.Error("dashboard must explain credit top-ups happen out-of-band")
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
	// Inactive pill visible for the draft action.
	if !strings.Contains(body, "inactive") {
		t.Error("studio must show the inactive pill for an inactive action")
	}
	// Toggle buttons for lifecycle management (R6.3.3): active action shows Disable.
	if !strings.Contains(body, "Disable") {
		t.Error("studio action card must show Disable toggle for an active action")
	}
	// Inactive action shows Enable.
	if !strings.Contains(body, "Enable") {
		t.Error("studio action card must show Enable toggle for an inactive action")
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

	// Studio page must show the insufficient-credits warning instead of the form (R6.3.6).
	body := getBody(t, cl, srv.URL+"/studio")
	if strings.Contains(body, `action="/studio/make"`) {
		t.Error("synthesis form must be hidden when credits < 20")
	}
	if !strings.Contains(strings.ToLower(body), "fewer than 20") {
		t.Error("studio must show insufficient-credits message when balance < 20")
	}
}

func TestStudioLifecycleHtmxSwap(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Get CSRF token.
	body := getBody(t, cl, srv.URL+"/studio")
	tok := csrfTokenFrom(t, body)

	// htmx disable request on action A1: must return a card fragment, not a redirect.
	form := url.Values{"gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/A1/disable", strings.NewReader(form.Encode()))
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
		t.Fatalf("lifecycle htmx returned %d, body: %s", resp.StatusCode, frag)
	}
	// Response must be a card fragment (article element), not a redirect page.
	if !strings.Contains(frag, `id="action-A1"`) {
		t.Error("htmx lifecycle response must be a card fragment with the action id")
	}
	// Card must contain toggle buttons (R6.3.3).
	if !strings.Contains(frag, "Enable") && !strings.Contains(frag, "Disable") {
		t.Error("card fragment must contain lifecycle toggle buttons")
	}
}
