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
		// T1: U1 is buyer; T2: U1 is seller.
		w.Write([]byte(`[
			{"id":"T1","owner_user_id":"U1","target_user_id":"U2","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"},
			{"id":"T2","owner_user_id":"U2","target_user_id":"U1","status":"success","gross":50,"fee":5,"net":45,"started_at":"2024-01-02T00:00:00Z"}
		]`))
	})
	mux.HandleFunc("GET /v1/stats/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"action_id":"x"}`))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		// "owned" is owned by U1 (the signed-in user); anything else by U2.
		owner := "U2"
		if r.PathValue("id") == "owned" {
			owner = "U1"
		}
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","action":"@alice/x","owner_user_id":"` + owner +
			`","owner_handle":"@alice","name":"x","kind":"http","active":true,"public":true,"price":0,` +
			`"input_schema":{"type":"object","properties":{}},"output_schema":{"type":"object"}}`))
	})
	return httptest.NewServer(mux)
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
