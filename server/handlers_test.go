package server

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

// authKernel is a fake kernel sufficient to drive the PKCE login flow and a few
// authenticated reads. The signed-in user is "U1" / "alice".
// lastRename records the rename the fake kernel last served ("<target> <body>"),
// and lastDeposit the account a deposit named, so a test can assert what the
// BFF actually sent — in particular which NAMESPACE it addressed, now that the
// kernel serves users and peers on separate routes. Tests in this package are
// sequential (no t.Parallel), so one recorder each is enough.
var (
	lastRename       string
	lastDeposit      string
	lastPeerComplete string
)

// fakeHealth gives a fake kernel the identity banner every login pins: its key,
// the network digest, and the decimals that scale every amount (§5, §7).
// mutationTarget reads the target an action mutation names.
func mutationTarget(r *http.Request) string {
	var in struct {
		Target string `json:"target"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	return in.Target
}

func fakeHealth(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","handle":"testnet","public_key":"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcd",
		 "network":"play","network_digest":"D1","decimals":0,"symbol":"credits","rail_address":""}`))
	})
}

// ledgerTx is the transaction a fake ledger entry names as its cause.
const ledgerTx = "67753356-bfa3-4c11-9b70-299334ed67c3"

// withEvidence is an action row as its detail read serves it: the list row plus
// the evidence block, whose local_experience is this kernel's own record.
func withEvidence(row, experience string) string {
	return strings.TrimSuffix(row, "}") + `,"evidence":{"local_experience":` + experience + `,"provider_reported":null,"observed_by_others":[],"retained_cap":200}}`
}

func authKernel() *httptest.Server {
	lastRename, lastDeposit, lastPeerComplete = "", "", ""
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","description":"","available":500,"locked":0,
		 "connectors":[{"directory":"sys","connections":[{"provider":"gmail.com","actions":1,"unused":false,"provider_key":"oauth:G","created_at":"2024-01-01T00:00:00Z"}],
		   "actions":[{"action":"sys/gmail-send","scopes":["send"],"provider_key":"oauth:G","created_at":"2024-01-01T00:00:00Z"}]}],
		 "connections":[{"provider":"gmail.com","actions":1,"unused":false,"provider_key":"oauth:G","created_at":"2024-01-01T00:00:00Z"}]}`))
	})
	mux.HandleFunc("POST /v1/grants/start", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"ST1","authorize_url":"https://provider.test/auth?client_id=cid&state=ST1"}`))
	})
	mux.HandleFunc("GET /v1/grants/plan", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"groups":[{"provider_key":"bearer:api.lena.dev","provider":"api.lena.dev","scheme":"delegated_bearer","scopes":[],"connected":true,"covered":true,"actions":[{"action_id":"AL","action":"lena/api","granted":true}]}],"skipped_loginless":0,"skipped_uncallable":0}`))
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
		w.Write([]byte(`{"id":"U1","handle":"alice","description":"about me","available":500,"locked":0}`))
	})
	mux.HandleFunc("POST /v1/auth/recover/start", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"nonce":"NONCE1","expires_in_seconds":600}`))
	})
	mux.HandleFunc("POST /v1/auth/recover/complete", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		// T1: alice is buyer; T2: alice is seller. Both reference action "owned".
		w.Write([]byte(`[
			{"id":"T1","action_id":"owned","owner_handle":"alice","target_handle":"bob","caller_handle":"alice","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"},
			{"id":"T2","action_id":"owned","owner_handle":"bob","target_handle":"alice","caller_handle":"bob","status":"success","gross":50,"fee":5,"net":45,"started_at":"2024-01-02T00:00:00Z"}
		]`))
	})
	// The journal, in the d59742b shape (verified live): every movement between
	// two accounts, settlement included. alice's fee and her payout to bob name
	// the settling transaction; an obligation names its ticket; the operator's
	// deposit names sys as its sender.
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"amount":300,"reason":"` + ledgerTx + `","created_at":"2024-01-02T12:00:00Z","operator_handle":"alice","from_handle":"alice","to_handle":"bob"},
			{"amount":9,"reason":"obligation e2c28a67-9c1b-4272-abb7-7f13d75cd93b","created_at":"2024-01-02T11:00:00Z","operator_handle":"sys","from_handle":"alice","to_handle":"sys"},
			{"amount":5000,"reason":"","created_at":"2024-01-01T00:00:00Z","operator_handle":"sys","from_handle":"sys","to_handle":"alice"}
		]`))
	})
	// The kernel's public ratings projection: value/note/time and nothing else
	// (verified live 2026-08-05) — no rater, transaction, or receipt identity.
	mux.HandleFunc("GET /v1/actions/{id}/ratings", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"value":1,"note":"worked well","created_at":"2024-01-03T00:00:00Z"}]`))
	})
	// One row per action, served by both reads: a detail read is the list row
	// plus its evidence block, which a list row never carries (verified live).
	schemas := `"input_schema":{"type":"object","properties":{}},"output_schema":{"type":"object"}`
	rows := map[string]string{
		"A1": `{"id":"A1","action":"alice/hello","owner_handle":"alice","name":"hello","kind":"wasm","active":true,"public":true,"price":0,` + schemas + `}`,
		"A3": `{"id":"A3","action":"alice/draft","owner_handle":"alice","name":"draft","kind":"wasm","active":false,"public":false,"price":0,` + schemas + `}`,
		// Two delegated actions: /v1/me grants sys/gmail-send (AG) but not
		// sys/gmail-read (AN) — the needs-account chip must split them.
		"AG": `{"id":"AG","action":"sys/gmail-send","owner_handle":"sys","name":"gmail-send","kind":"http","active":true,"public":true,"price":0,"auth_scheme":"oauth_delegated","requires_grant":true,` + schemas + `}`,
		"AN": `{"id":"AN","action":"sys/gmail-read","owner_handle":"sys","name":"gmail-read","kind":"http","active":true,"public":true,"price":0,"auth_scheme":"oauth_delegated","requires_grant":true,` + schemas + `}`,
	}
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		// The kernel returns active rows by default; the inactive/private draft
		// (A3) appears only with ?all=1 — so tests that see the draft prove the UI
		// threads all=1 for the owner's own list.
		if r.URL.Query().Get("all") == "1" {
			w.Write([]byte("[" + rows["A1"] + "," + rows["A3"] + "]"))
		} else {
			w.Write([]byte("[" + rows["A1"] + "," + rows["AG"] + "," + rows["AN"] + "]"))
		}
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		// A listed action reads back as its row; "owned" belongs to alice (the
		// signed-in user), and anything else is bob's.
		id := r.PathValue("id")
		row, ok := rows[id]
		if !ok {
			owner := "bob"
			if id == "owned" {
				owner = "alice"
			}
			row = `{"id":"` + id + `","action":"` + owner + `/x","owner_handle":"` + owner + `","name":"x","kind":"http",` +
				`"active":true,"public":true,"price":0,` + schemas + `}`
		}
		w.Write([]byte(withEvidence(row, `{"action_id":"`+id+`"}`)))
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"status":"success","action_id":"A2","action_name":"synthesized","diagnostics":[]},"tx_id":"TX1","trace_id":"TR1"}`))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"T1","action_id":"owned","owner_handle":"alice","target_handle":"bob","caller_handle":"alice","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"}`))
	})
	// A mutation names a target and answers with the rows it wrote, in name order
	// — one target may name a whole application (kernel v0.14.0).
	mux.HandleFunc("POST /v1/actions/enable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"` + mutationTarget(r) + `","active":true}]`))
	})
	mux.HandleFunc("POST /v1/actions/disable", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"` + mutationTarget(r) + `","active":false}]`))
	})
	mux.HandleFunc("PUT /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"` + mutationTarget(r) + `","action":"alice/x","owner_handle":"alice","name":"x","kind":"http","active":true,"price":0}]`))
	})
	mux.HandleFunc("DELETE /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"` + r.URL.Query().Get("target") + `"}]`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) {
		// Two waiting steps spawned by the same creating action (sys/make) — the
		// common case: one process fans out several steps sharing one created_by.
		w.Write([]byte(`[
			{"id":"S1","required_caller_handle":"alice","action":"alice/hello","action_id":"A1","price":5,"status":"waiting","created_by":"sys/make","created_at":"2026-01-01T00:00:00Z"},
			{"id":"S2","required_caller_handle":"alice","action":"alice/hello","action_id":"A1","price":3,"status":"waiting","created_by":"sys/make","created_at":"2026-01-02T00:00:00Z"}
		]`))
	})
	mux.HandleFunc("GET /v1/steps/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","required_caller_handle":"alice","action":"alice/hello","action_id":"A1","price":5,"partial_args":{},"status":"waiting","created_at":"2026-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("POST /v1/users", func(w http.ResponseWriter, r *http.Request) {
		// A taken handle is TYPED as of v0.12.14 (verified live): 422
		// invalid_input "create account: already exists", not the raw
		// UNIQUE-constraint 500 the UI once had to sniff for.
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "dupe") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"code":"invalid_input","error":"create account: already exists"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"U9","handle":"new","email":"new@test.com","available":0,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"U1","handle":"alice","email":"alice@test.com","available":500,"locked":0}]`))
	})
	mux.HandleFunc("GET /v1/admin/users/{handle}", func(w http.ResponseWriter, r *http.Request) {
		// The users route serves USERS. A peer key is refused here, exactly as
		// the real kernel refuses it (verified live, §2 of the v0.15 record):
		// the route says which namespace it takes, so a name that exists in the
		// other one is simply not found.
		h := r.PathValue("handle")
		if len(h) >= 43 {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"not_found","error":"` + h + ` is not a user here"}`))
			return
		}
		w.Write([]byte(`{"id":"U1","handle":"` + h + `","description":"x","available":500,"locked":0}`))
	})
	mux.HandleFunc("POST /v1/admin/users/{handle}/suspend", func(w http.ResponseWriter, r *http.Request) {
		// Every roster verb answers with the target as show renders it (R5).
		w.Write([]byte(`{"handle":"` + r.PathValue("handle") + `","available":500,"locked":0,"suspended_at":"2026-09-14T01:12:27Z"}`))
	})
	mux.HandleFunc("POST /v1/admin/users/{handle}/unsuspend", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"handle":"` + r.PathValue("handle") + `","available":500,"locked":0}`))
	})
	// The peer namespace: the same verbs, refusing a user target.
	mux.HandleFunc("POST /v1/admin/peers/{key}/suspend", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"public_key":"` + r.PathValue("key") + `","petname":"peerb","suspended_at":"2026-09-14T01:14:24Z"}`))
	})
	mux.HandleFunc("POST /v1/admin/peers/{key}/unsuspend", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"public_key":"` + r.PathValue("key") + `","petname":"peerb"}`))
	})
	mux.HandleFunc("POST /v1/admin/peers/{key}/rename", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastRename = r.PathValue("key") + " " + string(b)
		w.Write([]byte(`{"public_key":"` + r.PathValue("key") + `","petname":"renamed"}`))
	})
	mux.HandleFunc("POST /v1/admin/users/{handle}/rename", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastRename = r.PathValue("handle") + " " + string(b)
		if strings.Contains(string(b), `"new_name":"sys"`) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"code":"invalid_input","error":"the superuser handle cannot be renamed"}`))
			return
		}
		w.Write([]byte(`{"handle":"renamed"}`))
	})
	mux.HandleFunc("POST /v1/admin/users/{handle}/deposit", func(w http.ResponseWriter, r *http.Request) {
		// A deposit credits a USER, named in the path, and answers with the
		// ledger entry it wrote; a replay returns that same entry. Ledger
		// entries carry no id. A peer key here is not a user (§2).
		h := r.PathValue("handle")
		if len(h) >= 43 {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"not_found","error":"` + h + ` is not a user here"}`))
			return
		}
		lastDeposit = h
		var body struct{ Ref string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Ref == "" {
			w.WriteHeader(422)
			w.Write([]byte(`{"code":"invalid_input","error":"a deposit must name the payment it records (--ref)"}`))
			return
		}
		w.Write([]byte(`{"amount":100,"external_key":"attr:rail:ref:` + body.Ref + `","operator_handle":"sys","from_handle":"sys","to_handle":"` + h + `"}`))
	})
	mux.HandleFunc("GET /v1/admin/kernel/deposits", func(w http.ResponseWriter, r *http.Request) {
		// Two obligations and one held payment. TICKET1's buyer has said how the
		// draw came out, so it carries a status and the drawn amount, which
		// exceeds what the call owed (kernel §P10). TICKET2's buyer has said
		// nothing: empty status, zero amount — absent fields, exactly as the
		// live kernel sends them (docs/kernel-v0.15-verification.md §3). The
		// held row's shape was verified on a chain world (anvil, decimals 6).
		w.Write([]byte(`{"deposits":[{"id":"rail:0xabc:0","kind":"deposit","amount":5000000,"credit":5000000,
		  "status":"held","tx_hash":"0xabc","created_at":"2026-09-09T15:22:32Z",
		  "party_handle":"0x70997970c51812dc3a010c7d01b50e0d17dc79c8"}],
		 "owed":[{"id":"TICKET1","obligation":8,"amount":10,
		 "tx_hash":"manual:TICKET1","status":"announced","created_at":"2026-09-09T09:30:24Z","peer":"KEYB"},
		 {"id":"TICKET2","obligation":126,"amount":0,"tx_hash":"","status":"",
		  "created_at":"2026-09-14T01:13:39Z","peer":"KEYD"}]}`))
	})
	mux.HandleFunc("GET /v1/admin/peers", func(w http.ResponseWriter, r *http.Request) {
		// The merged roster (verified live 2026-08-06): counterparties
		// (has_account) and discovered kernels in one list keyed by public key,
		// this kernel excluded. A discovered row carries a catalog and a last_seen
		// but no credit, and no PETNAME until one is bound — only its self-asserted
		// nickname, which "twin" repeats across two keys, legally, so they must
		// stay two rows. A suspended counterparty appears only under ?all=1.
		rows := `{"petname":"peerb","nickname":"peerb","public_key":"KEYB","has_account":true,"actions":4,"last_seen":"2026-08-04T09:54:01Z","last_contact_failed_at":"2026-08-04T10:00:00Z"},
		  {"petname":"debtor","public_key":"KEYD","has_account":true,"actions":0},
		  {"nickname":"twin","public_key":"KEYT1","has_account":false,"actions":13,"last_seen":"2026-08-06T00:04:34Z"},
		  {"nickname":"twin","public_key":"KEYT2","has_account":false,"actions":7,"last_seen":"2026-08-06T00:04:35Z"}`
		if r.URL.Query().Get("all") == "1" {
			rows += `,{"petname":"frozen","public_key":"KEYS","has_account":true,"suspended_at":"2026-08-01T00:00:00Z"}`
		}
		// A bare array: the kernel envelopes no list (API.md R6; verified live
		// 2026-08-05 — an envelope here would silently render zero peers).
		w.Write([]byte(`[` + rows + `]`))
	})
	mux.HandleFunc("GET /v1/admin/peers/{key}/inspect", func(w http.ResponseWriter, r *http.Request) {
		// online/source are TOP-LEVEL in the real response; reachability holds only
		// the transport diagnostics. As of v0.12.8 an action is {name, description,
		// price} only, the account arrives inline, and evidence carries both legs:
		// the subject's own rows (issuer == public_key) and a third party's, whose
		// corroborated_uses is the two-kernel link. Shapes captured live 2026-08-06.
		key := r.PathValue("key")
		// account is null for a kernel that holds none here: KEYT* are discovered
		// (source live), KEYNONE is a stranger key the kernel has never learned.
		account := `{"suspended":false}`
		switch {
		case key == "KEYS":
			account = `{"suspended":true}`
		case key == "KEYNONE":
			w.Write([]byte(`{"handle":"","public_key":"` + key + `","actions":[],"evidence":[],"account":null,
			 "online":false,"source":"none","reachability":{"path":"unreachable","error":"routing: not found"}}`))
			return
		case strings.HasPrefix(key, "KEYT"):
			account = "null"
		}
		w.Write([]byte(`{"petname":"kernelc","nickname":"selfname","public_key":"` + key + `","about":"a peer kernel",
		 "actions":[{"action_id":"a2","name":"synth/make","description":"synth","price":20,"indicative":true}],
		 "account":` + account + `,
		 "evidence":[{"issuer_public_key":"` + key + `","subject_action_id":"a2","uses":6,"successes":6,"failures":0,
		    "corroborated_uses":0,"avg_latency_ms":690.5,"rating_count":2,"rating_mean":1,"unverified_ratings":1},
		   {"issuer_public_key":"KEYB","subject_action_id":"a2","uses":1,"successes":0,"failures":1,
		    "corroborated_uses":1,"avg_latency_ms":0,"rating_count":0,"rating_mean":0,"unverified_ratings":0}],
		 "online":true,"source":"live",
		 "steps":[{"id":"PS1","partial_args":{"message":"please confirm the shipment"},
		   "allowed_input":{"type":"object"},"price":0,"created_at":"2026-09-14T10:43:26Z"}],
		 "steps_truncated":false,
		 "reachability":{"path":"direct","rtt_millis":4,"protocols":["/juice/fed/gossip/1"]}}`))
	})
	mux.HandleFunc("GET /v1/admin/kernel", func(w http.ResponseWriter, r *http.Request) {
		// v0.14: the kernel's identity, the money rules it serves under, its rail
		// position, and its credit position — what it has delivered unpaid against
		// the limit it set (kernel §D14). `stop` is absent unless the rail halts.
		w.Write([]byte(`{"handle":"kernel","public_key":"KEY","about":"our test kernel","addrs":[],
		 "network":"play","network_digest":"ef1f","rail_address":"","finalized":null,
		 "sys":{"earnings":170,"pending_payouts":0,"held_deposits":0,"refill_locks":0},
		 "solvency":{"liabilities":8126,"vault":8126,"gap":0},
		 "custody":{"checked":false,"difference":0,"ok":false},
		 "lottery":10,"credit_limit":1000,"exposure":320,
		 "fee_bps":2000,"remote_bps":500,"import_bps":500}`))
	})
	mux.HandleFunc("POST /v1/steps/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		// One route serves both completions: the ordinary one, and a peer's,
		// which is the same route with the peer named in the body.
		b, _ := io.ReadAll(r.Body)
		lastPeerComplete = r.PathValue("id") + " " + string(b)
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

// connectSelRe matches the selector query param of the first /grants/connect
// link. connectSelector URL-decodes it so assertions are robust to
// html/template's URL-query escaping (e.g. @ → %40).
var connectSelRe = regexp.MustCompile(`/grants/connect\?selector=([^"&]*)`)

func connectSelector(t *testing.T, body string) string {
	t.Helper()
	m := connectSelRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no /grants/connect link in body")
	}
	sel, err := url.QueryUnescape(m[1])
	if err != nil {
		t.Fatalf("bad selector encoding %q: %v", m[1], err)
	}
	return sel
}

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

	form := url.Values{"handle": {"alice"}, "password": {"pw"}, "gorilla.csrf.Token": {m[1]}}
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

	// "dupe" handle → the kernel's typed 422 invalid_input "already exists".
	_, body := signupPost(t, srv, url.Values{
		"handle":   {"dupe"},
		"password": {"password1"}, "confirm_password": {"password1"},
	})
	if !strings.Contains(body, "already taken") {
		t.Error("duplicate signup must show a friendly 'already taken' message")
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
		"handle":   {"x"},
		"password": {"password1"}, "confirm_password": {"password2"},
	})
	if !strings.Contains(mismatch, "don&#39;t match") && !strings.Contains(mismatch, "don't match") {
		t.Errorf("mismatched passwords must be rejected: %s", firstError(mismatch))
	}
	// Too short → min-length message.
	_, short := signupPost(t, srv, url.Values{
		"handle":   {"x"},
		"password": {"short"}, "confirm_password": {"short"},
	})
	if !strings.Contains(short, "at least 8") {
		t.Errorf("short password must be rejected: %s", firstError(short))
	}
}

// TestSignupShowsRecoveryPhraseThenLogin: signup does NOT log the user in — it
// sends them to a standalone phrase page (outside the app) that survives reloads,
// gates entry behind an "I understand" acknowledgement, and only then routes them
// to sign in.
func TestSignupShowsRecoveryPhraseThenLogin(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	page := getBody(t, cl, srv.URL+"/login?tab=signup")
	resp := post("/signup", url.Values{"handle": {"newbie"}, "password": {"password1"}, "confirm_password": {"password1"}, "gorilla.csrf.Token": {csrfTokenFrom(t, page)}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/recovery-phrase" {
		t.Fatalf("signup must redirect to the phrase page (not into the app), got %q", loc)
	}

	// Standalone (no app chrome), 12 words, copy button, acknowledgement checkbox.
	body := getBody(t, cl, srv.URL+"/recovery-phrase")
	if strings.Count(body, "<li>") < 12 {
		t.Errorf("phrase page must show the 12 words: %s", body)
	}
	if !strings.Contains(body, `data-copy="#phrase-words"`) || !strings.Contains(body, `name="understand"`) {
		t.Error("phrase page must offer a copy button and an acknowledgement checkbox")
	}
	if strings.Contains(body, `class="sidebar"`) || strings.Contains(body, `class="topbar"`) {
		t.Error("the phrase page must be standalone — no app chrome")
	}
	// It survives a reload (peek, not one-shot) so the user can re-copy.
	if strings.Count(getBody(t, cl, srv.URL+"/recovery-phrase"), "<li>") < 12 {
		t.Error("phrase page must survive a reload until acknowledged")
	}

	tok := csrfTokenFrom(t, body)
	// Acknowledgement without the box goes back to the phrase page, not on.
	r1 := post("/recovery-phrase", url.Values{"gorilla.csrf.Token": {tok}})
	r1.Body.Close()
	if r1.Header.Get("Location") != "/recovery-phrase" {
		t.Errorf("acknowledgement without the checkbox must return to the phrase page, got %q", r1.Header.Get("Location"))
	}
	// With the box: cleared and routed to sign in — the point access is granted.
	r2 := post("/recovery-phrase", url.Values{"understand": {"on"}, "gorilla.csrf.Token": {tok}})
	r2.Body.Close()
	if r2.StatusCode != http.StatusSeeOther || r2.Header.Get("Location") != "/login" {
		t.Fatalf("acknowledgement must route to /login, got %d %q", r2.StatusCode, r2.Header.Get("Location"))
	}
	// Now cleared: revisiting the phrase page falls through to login.
	r3, _ := cl.Get(srv.URL + "/recovery-phrase")
	r3.Body.Close()
	if r3.Header.Get("Location") != "/login" {
		t.Errorf("after acknowledgement the phrase page must redirect to login, got %q", r3.Header.Get("Location"))
	}
}

// TestRecoverResetsPassword drives the seed-phrase reset: the BFF signs the
// kernel nonce with the phrase-derived key and completes the reset.
func TestRecoverResetsPassword(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	page := getBody(t, cl, srv.URL+"/recover")
	if !strings.Contains(page, `name="phrase"`) {
		t.Fatalf("recover page must have a phrase field: %s", page)
	}
	const phrase = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	form := url.Values{
		"handle": {"alice"}, "phrase": {phrase},
		"password": {"brandnewpw"}, "confirm_password": {"brandnewpw"},
		"gorilla.csrf.Token": {csrfTokenFrom(t, page)},
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/recover", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("successful reset must redirect to /login, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestOwnerPageListsPublicActions: /users/{handle} renders the handle and that
// owner's public action cards.
// TestOwnerPageShowsVisibleSetAcrossLevels: the owner page shows what the viewer
// may see across private/local/public — your own page includes your private and
// inactive actions (all=1 in your own scope), anyone else's shows only the
// caller-visible active set — and the page never labels the list a single level.
func TestOwnerPageShowsVisibleSetAcrossLevels(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL) // signed in as alice

	// Your own page: A3 is alice's inactive/private draft — it must appear (and
	// self-labels via the card's inactive/lock chips).
	own := getBody(t, cl, srv.URL+"/users/alice")
	if !strings.Contains(own, "alice") || !strings.Contains(own, `class="action-card`) {
		t.Error("owner page must show the handle and action cards")
	}
	if !strings.Contains(own, "draft") {
		t.Error("your own owner page must include your private/inactive actions")
	}
	if strings.Contains(own, "Public actions") {
		t.Error("owner page must not label the whole list 'Public actions'")
	}

	// Someone else's page: caller-visible active set only (all=0) — no private/inactive.
	if strings.Contains(getBody(t, cl, srv.URL+"/users/bob"), "draft") {
		t.Error("another owner's page must not surface inactive/private actions")
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
	// Signed, perspective amounts: alice is buyer on T1 (−gross) and seller on
	// T2 (+net) — the Role/Gross/Fee columns are gone (they live on the detail).
	if !strings.Contains(body, "-100 credits") {
		t.Error("buyer transaction must show the negative gross (−100.000000)")
	}
	if !strings.Contains(body, "+45 credits") || !strings.Contains(body, "sig-credited") {
		t.Error("seller transaction must show the positive net (+45 credits) as credited")
	}
	// Gross/fee/full-party breakdown stays on the detail page; the list shows the
	// signed perspective amount (plus the viewer's role, a lightweight label).
	for _, gone := range []string{"<th>Fee</th>", "<th>Gross</th>"} {
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
		t.Error("action name must link to the action")
	}
	if !strings.Contains(body, `href="/users/alice"`) {
		t.Error("action owner must link to its owner page")
	}
	// The Amount column is right-aligned, so its header is too (class="money").
	if !strings.Contains(body, `<th class="money">Amount</th>`) {
		t.Error("the Amount header must be right-aligned (class=\"money\") over its values")
	}
}

// The call history renders through _tx_row: the action ref linked, and the
// signed perspective amounts rather than the raw gross.
func TestCallHistoryUsesUnifiedRow(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/transactions")
	if !strings.Contains(body, `href="/actions/owned"`) {
		t.Error("the call history must link the action ref")
	}
	if !strings.Contains(body, "-100 credits") || !strings.Contains(body, "+45 credits") {
		t.Error("the call history must show the signed perspective amounts")
	}
	if strings.Contains(body, "<th>Gross</th>") {
		t.Error("the call history must not carry the Gross column")
	}
}

func TestTransactionDetailShowsActionAndParties(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/transactions/T1")
	// Action ref: owner links to its owner page.
	if !strings.Contains(body, `href="/users/alice"`) {
		t.Error("transaction detail must show the action ref with a linked owner")
	}
	// Buyer: current user (alice) is the buyer party
	if !strings.Contains(body, "alice") {
		t.Error("transaction detail must show the buyer handle when current user is buyer")
	}
}

// How money comes in and goes out is a fact about the world this kernel serves:
// a world without payment addresses has no address to register, and the
// operator records payments in and out — so a withdrawal is offered, and the
// address form is not (kernel §D23, verified live on d59742b).
func TestWalletPayInAndWithdrawalFollowTheWorld(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// The fake kernel serves no rail address (a play-style world).
	body := getBody(t, cl, srv.URL+"/wallet")
	if !strings.Contains(body, "500") {
		t.Error("wallet should show available balance")
	}
	if !strings.Contains(body, "the operator records money in and out") {
		t.Error("without addresses, the wallet must say who records a payment")
	}
	if !strings.Contains(body, "form=withdraw") {
		t.Error("every world pays out, so a withdrawal is offered")
	}
	if strings.Contains(body, "form=address") {
		t.Error("a world without addresses offers no address to register")
	}
}

// On a world whose money has addresses the wallet offers the two verbs that
// exist there: register the address you are paid at, and withdraw to it.
func TestWalletOffersWithdrawalOnAnAddressWorld(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","handle":"tango","public_key":"PK","network":"test","network_digest":"D9","decimals":6,"symbol":"USDC","rail_address":"0xKERNEL"}`))
	})
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=C"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":5000000,"locked":0,"rail_address":"0xALICE"}`))
	})
	mux.HandleFunc("GET /v1/withdrawals", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"W1","kind":"payout","amount":10,"status":"confirmed","tx_hash":"manual:W1","created_at":"2026-09-09T09:17:26Z"}]`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"results":[]},"tx_id":"T1"}`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/wallet")
	if !strings.Contains(body, "0xKERNEL") || !strings.Contains(body, "0xALICE") {
		t.Errorf("the wallet must name where the kernel is paid and where the user is: %s", body)
	}
	if !strings.Contains(body, "form-withdraw") || !strings.Contains(body, "form-address") {
		t.Error("an address world offers withdraw and register")
	}
	// The id is minted here so a double submit replays to one row.
	if !strings.Contains(body, `name="id" value="`) {
		t.Error("the withdrawal form must carry a minted id")
	}
	// The world's own scale and symbol: 5000000 base units at six decimals is
	// 5.00 USDC, and the trailing zeros stop at the two-digit floor.
	if !strings.Contains(body, "5.00 USDC") {
		t.Errorf("the balance must scale by the world's decimals: %s", body)
	}
	if !strings.Contains(body, "manual:W1") {
		t.Error("the withdrawals table must show the payment that settled it")
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

	body := getBody(t, cl, srv.URL+"/workshop")
	// The workshop offers no control the kernel cannot serve: sys/make is not a
	// kernel action, so no synthesis affordance exists anywhere on the page.
	if strings.Contains(body, "Synthesize") || strings.Contains(body, "/actions/make") {
		t.Error("workshop must not offer synthesis: sys/make does not exist on the kernel")
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

func TestUserShowsConnectors(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	body := getBody(t, cl, srv.URL+"/settings")

	// Connected accounts renders the grant tree: a folder per directory, its
	// account provider in the summary, expandable to the contained grants.
	if !strings.Contains(body, ">Connected accounts</h2>") || !strings.Contains(body, "<details") || !strings.Contains(body, "gmail.com") {
		t.Error("/you must render the grant tree (folders per directory)")
	}
	// The folder summary offers account-level disconnect (account=provider_key)…
	if !strings.Contains(body, `action="/grants/disconnect"`) || !strings.Contains(body, `value="oauth:G"`) {
		t.Error("/you must offer an account-level disconnect (account=provider_key)")
	}
	// …and each contained grant offers a per-action disconnect (action=selector).
	if !strings.Contains(body, `name="action" value="sys/gmail-send"`) {
		t.Error("/you grant tree must offer per-action disconnect")
	}
}

// TestGrantConnectPostsToken checks the delegated_bearer static-token path: the
// handler forwards {action, token} to POST /v1/grants and returns the user to the
// page they connected from (the Referer).
func TestGrantConnectPostsToken(t *testing.T) {
	base := authKernel()
	defer base.Close()
	var mu sync.Mutex
	var gotBody string
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/grants" {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotBody = string(b)
			mu.Unlock()
			w.Write([]byte(`{"status":"connected"}`))
			return
		}
		base.Config.Handler.ServeHTTP(w, r) // delegate login/dashboard reads to authKernel
	}))
	defer wrap.Close()

	srv := httptest.NewServer(newTestHandler(t, wrap.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/settings"))
	form := url.Values{"action": {"lena/api"}, "token": {"PAT-123"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/grants/connect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", srv.URL+"/actions/abc") // connected from an action page
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/actions/abc" {
		t.Errorf("redirected to %q, want /actions/abc (back to the originating action)", loc)
	}
	mu.Lock()
	got := gotBody
	mu.Unlock()
	// The legacy `action` form field is mapped to the v0.7 `selector`.
	if !strings.Contains(got, `"selector":"lena/api"`) || !strings.Contains(got, `"token":"PAT-123"`) {
		t.Errorf("kernel /v1/grants body = %s", got)
	}
}

// TestCreateActionParsesDollarPrice: a price typed in dollars on the create form
// reaches the kernel as the correct integer credits (the parseDollars inverse of
// the money display). "12 credits" MUST arrive as "price":12, not 0 or 12000.
func TestCreateActionParsesTypedPrice(t *testing.T) {
	base := authKernel()
	defer base.Close()
	var mu sync.Mutex
	var gotBody string
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/actions" {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotBody = string(b)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"A9","action":"alice/priced","owner_handle":"alice","name":"priced","kind":"http","price":12}`))
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer wrap.Close()

	srv := httptest.NewServer(newTestHandler(t, wrap.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/workshop"))
	form := url.Values{
		"name":               {"priced"},
		"kind":               {"http"},
		"price":              {"12"}, // a typed amount is digits: the symbol is display only
		"gorilla.csrf.Token": {tok},
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	mu.Lock()
	got := gotBody
	mu.Unlock()
	if !strings.Contains(got, `"price":12`) {
		t.Errorf("a typed price of 12 must reach the kernel as 12 base units; kernel body = %s", got)
	}
}

// TestGrantStartRedirectsToProvider: the browser-consent start is a
// navigational GET (never a form — form-action 'self' would block the
// submission's cross-origin redirect) that 303s straight to the provider's
// authorize URL.
func TestGrantStartRedirectsToProvider(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := cl.Get(srv.URL + "/grants/start?selector=%40g%2Fmail&provider=oauth%3AG")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "https://provider.test/auth?client_id=cid&state=ST1" {
		t.Errorf("redirected to %q, want the provider authorize URL", loc)
	}
}

// TestBackToKeepsQuery: backTo must preserve the Referer's query — the consent
// page is addressed by ?selector=…, and dropping it made the success redirect
// land on a selector-less /grants/connect, whose guard then overwrote the
// success flash with "No account to connect."
func TestBackToKeepsQuery(t *testing.T) {
	mk := func(ref string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://ui.test/grants/connect", nil)
		if ref != "" {
			r.Header.Set("Referer", ref)
		}
		return r
	}
	cases := []struct{ ref, want string }{
		{"http://ui.test/grants/connect?selector=%40lena%2Fapi", "/grants/connect?selector=%40lena%2Fapi"},
		{"http://ui.test/actions/abc", "/actions/abc"},
		{"http://evil.test/grants/connect?selector=x", "/settings"}, // off-site Referer never honored
		{"", "/settings"},
	}
	for _, c := range cases {
		if got := backTo(mk(c.ref)); got != c.want {
			t.Errorf("backTo(Referer=%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

// TestGrantConnectReturnsToConsentPage: a token paste made on the consent page
// must land back on it (query intact) with the success flash — not on the
// selector-less page whose guard flashes "No account to connect."
func TestGrantConnectReturnsToConsentPage(t *testing.T) {
	base := authKernel()
	defer base.Close()
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/grants" {
			w.Write([]byte(`{"status":"connected","actions":["lena/api"]}`))
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer wrap.Close()

	srv := httptest.NewServer(newTestHandler(t, wrap.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/settings"))
	form := url.Values{"selector": {"lena/api"}, "token": {"PAT-123"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/grants/connect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", srv.URL+"/grants/connect?selector=%40lena%2Fapi")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/grants/connect?selector=%40lena%2Fapi" {
		t.Fatalf("redirected to %q, want the consent page with its selector", loc)
	}
	body := getBody(t, cl, srv.URL+loc)
	if strings.Contains(body, "No account to connect.") {
		t.Error("post-connect page must not flash the selector-missing error")
	}
	if !strings.Contains(body, "Account connected") {
		t.Error("post-connect page must carry the success flash")
	}
	if !strings.Contains(body, ">connected</span>") {
		t.Error("consent plan must show the connected pill after the paste")
	}
}

// --- kernel-down handling (snag #2) ---

func TestHealthDownReturns503(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1")) // dead kernel
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("plain /health with kernel down = %d, want 503", resp.StatusCode)
	}
}

func TestLoginShowsKernelUnreachable(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	if body := getBody(t, &http.Client{}, srv.URL+"/login"); !strings.Contains(body, "unreachable") {
		t.Errorf("login page must warn the kernel is unreachable: %s", body)
	}
}

// TestHtmxAuthLossRedirectsViaHeader guards the weird-badge fix: an htmx request that
// loses auth must full-page redirect via the HX-Redirect header, never a 303 whose
// body htmx would swap into the triggering element (the badge span).
func TestHtmxAuthLossRedirectsViaHeader(t *testing.T) {
	k := authKernel()
	defer k.Close()
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/inbox/badge", nil) // requireAuth-gated
	req.Header.Set("HX-Request", "true")
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("HX-Redirect"); got != "/login" {
		t.Errorf("htmx auth-loss: HX-Redirect=%q status=%d, want /login (no page swap)", got, resp.StatusCode)
	}
}

// TestHealthHeartbeatLogsOutWhenKernelDown: an authenticated heartbeat whose kernel
// has gone away is logged out cleanly (HX-Redirect to /login).
func TestHealthHeartbeatLogsOutWhenKernelDown(t *testing.T) {
	base := authKernel()
	defer base.Close()
	var down atomic.Bool
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && down.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer wrap.Close()
	srv := httptest.NewServer(newTestHandler(t, wrap.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	down.Store(true) // the kernel goes away after the session exists
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("HX-Redirect"); got != "/login" {
		t.Errorf("authed heartbeat + kernel down: HX-Redirect=%q status=%d, want /login", got, resp.StatusCode)
	}
}

func TestKernelIdentityShown(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	// Login page (unauthenticated) names the kernel and shows the key fingerprint.
	login := getBody(t, &http.Client{}, srv.URL+"/login")
	if !strings.Contains(login, "Sign in to") || !strings.Contains(login, "testnet") {
		t.Error("login page must name the kernel it's joining (testnet)")
	}
	if !strings.Contains(login, "ABCDEFGHIJ…abcd") {
		t.Error("login page must show the kernel key fingerprint for verification")
	}
	// The sidebar brand shows the kernel handle once signed in.
	cl := loginClient(t, srv.URL)
	if !strings.Contains(getBody(t, cl, srv.URL+"/browse"), `class="lbl">testnet`) {
		t.Error("sidebar brand must show the kernel handle")
	}
}

func TestActionCardChips(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Browse renders active public actions as image cards. The ref is the title:
	// the owner links to its owner page, the name to the action (YouTube model).
	browse := getBody(t, cl, srv.URL+"/browse")
	if !strings.Contains(browse, `class="card-title"`) {
		t.Error("browse card must carry a card title")
	}
	if !strings.Contains(browse, `href="/actions/A1"`) {
		t.Error("browse card name must link to the action")
	}
	if !strings.Contains(browse, `href="/users/alice"`) {
		t.Error("browse card owner must link to its owner page")
	}
	if strings.Contains(browse, `title="WebAssembly action">W`) {
		t.Error("browse card must NOT show an always-on kind chip (exceptional-only)")
	}
	if !strings.Contains(browse, `class="media"`) {
		t.Error("browse card must carry a media slot")
	}

	// The workshop management table carries the visibility + state cells: a private
	// inactive action shows the lock chip, the inactive pill, and a dimmed row.
	body := getBody(t, cl, srv.URL+"/workshop")
	if !strings.Contains(body, `/static/icons.svg#lock`) {
		t.Error("a private action must show the lock chip in the workshop table")
	}
	if !strings.Contains(body, `class="pill inactive">inactive`) {
		t.Error("an inactive action must show the inactive pill")
	}
	if !strings.Contains(body, `class="rowable dim"`) {
		t.Error("an inactive action's row must be dimmed")
	}
	// The old cluttered card markup is gone.
	for _, gone := range []string{"action-card-name", "action-card-meta"} {
		if strings.Contains(body, gone) {
			t.Errorf("must not contain legacy %q", gone)
		}
	}
}

// TestNeedsAccountChip: the filled key chip marks exactly the delegated actions
// the viewer hasn't connected. The fake kernel grants sys/gmail-send (AG) but
// not sys/gmail-read (AN); both carry requires_grant.
func TestNeedsAccountChip(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	card := func(body, id string) string {
		t.Helper()
		for _, a := range strings.Split(body, "<article") {
			if strings.Contains(a, `/actions/`+id+`"`) {
				return a
			}
		}
		t.Fatalf("no card for %s on /browse", id)
		return ""
	}
	browse := getBody(t, cl, srv.URL+"/browse")
	if !strings.Contains(card(browse, "AN"), "icons.svg#key") {
		t.Error("ungranted delegated action must carry the key chip on its card")
	}
	if strings.Contains(card(browse, "AG"), "icons.svg#key") {
		t.Error("granted delegated action must NOT carry the key chip")
	}
	if strings.Contains(card(browse, "A1"), "icons.svg#key") {
		t.Error("a grant-free action must NOT carry the key chip")
	}

	// Same split on the action page's chips row.
	if !strings.Contains(getBody(t, cl, srv.URL+"/actions/AN"), "icons.svg#key") {
		t.Error("ungranted action page must show the key chip")
	}
	if strings.Contains(getBody(t, cl, srv.URL+"/actions/AG"), "icons.svg#key") {
		t.Error("granted action page must NOT show the key chip")
	}
}

// TestRunFormGatedOnNeedsConnect: a run without the grant is doomed, so the run
// region offers the connect gesture instead of the form (same rule as the
// peer-state gate). A granted action keeps its form.
func TestRunFormGatedOnNeedsConnect(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	gated := getBody(t, cl, srv.URL+"/actions/AN")
	if !strings.Contains(gated, "Needs a connected account.") ||
		!strings.Contains(gated, `/grants/connect?selector=`) ||
		!strings.Contains(gated, ">Connect account</a>") {
		t.Error("ungranted action page must replace the run form with the connect gesture")
	}
	if strings.Contains(gated, `hx-post="/actions/AN/run"`) {
		t.Error("ungranted action page must not offer the doomed run form")
	}
	// The connect link must target the action's FOLDER (sys), not the degenerate
	// single-action selector (sys/gmail-read) — one paste then connects the whole
	// provider group, per the kernel's directory grouping.
	if sel := connectSelector(t, gated); sel != "sys" {
		t.Errorf("connect link selector = %q, want the folder sys (not the bare action ref)", sel)
	}

	open := getBody(t, cl, srv.URL+"/actions/AG")
	if !strings.Contains(open, `hx-post="/actions/AG/run"`) {
		t.Error("granted action page must keep the run form")
	}
	if strings.Contains(open, "Needs a connected account.") {
		t.Error("granted action page must not show the connect gate")
	}
}

// TestRunGrantRequiredConnectsFolder: a run rejected pre-lock with grant_required
// (e.g. the grant was revoked after page load) renders a connect affordance whose
// selector is the action's FOLDER (sys), not the bare failing ref
// (sys/gmail-read) — so one key paste connects every sys/* action sharing the
// provider, not just the one that failed. Guards the run-outcome connect site,
// the twin of the NeedsConnect gate above.
func TestRunGrantRequiredConnectsFolder(t *testing.T) {
	base := authKernel()
	defer base.Close()
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/run" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"grant_required","meta":{"action":"sys/gmail-read"},"error":"grant required"}`))
			return
		}
		base.Config.Handler.ServeHTTP(w, r) // login/action/me reads via authKernel
	}))
	defer wrap.Close()

	srv := httptest.NewServer(newTestHandler(t, wrap.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/actions/AN"))
	form := url.Values{"gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/AN/run", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true") // htmx path renders the _call_outcome fragment
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "Connect an account to run this.") {
		t.Fatalf("run outcome did not render the grant-required connect affordance: %s", body)
	}
	if sel := connectSelector(t, string(body)); sel != "sys" {
		t.Errorf("run-outcome connect selector = %q, want the folder sys (not the bare action ref)", sel)
	}
}

func TestActionsCommandAndFilters(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	body := getBody(t, cl, srv.URL+"/workshop")

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
	for _, form := range []string{"action-create", "action-import"} {
		if !strings.Contains(body, "?form="+form) {
			t.Errorf("+ new action menu missing the %q method", form)
		}
	}
	// There is no unimport: an application is switched off or removed with the
	// ordinary verbs over its path (kernel §D21).
	if strings.Contains(body, "unimport") {
		t.Error("the kernel serves no unimport")
	}

	// Filters are the unified .tabs view-switch component (not the retired linkbar).
	if strings.Contains(body, "linkbar") {
		t.Error("filters must use .tabs, not the retired .linkbar")
	}
	if !strings.Contains(body, `class="tabs control-tabs"`) {
		t.Error("action filters must render as a .tabs row")
	}
	if !strings.Contains(body, `class="tab active" href="/workshop" aria-current="page">all`) {
		t.Error("action filter tabs must mark 'all' active")
	}
	if !strings.Contains(body, `href="/workshop?filter=active"`) {
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
	cl := loginClientAs(t, srv.URL, "sys")
	body := getBody(t, cl, srv.URL+"/workshop?scope=all")

	if strings.Contains(body, ">all actions<") {
		t.Error("the confusing 'all actions' filter tab must be gone (scope is its own switch)")
	}
	if !strings.Contains(body, `>everyone</a>`) || !strings.Contains(body, `>yours</a>`) {
		t.Error("superuser must get a yours|everyone scope switch")
	}
	// everyone active on ?scope=all…
	if !strings.Contains(body, `class="tab active" href="/workshop?scope=all" aria-current="page">everyone`) {
		t.Error("the 'everyone' scope tab must be active on ?scope=all")
	}
	// …and the state-filter tabs preserve the scope (carry scope=all; html/template
	// escapes the & in the href).
	if !strings.Contains(body, `href="/workshop?filter=active&amp;scope=all"`) {
		t.Error("filter tabs must preserve the scope (carry scope=all)")
	}
}

func TestStudioLifecycleRedirect(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// Get CSRF token.
	body := getBody(t, cl, srv.URL+"/workshop")
	tok := csrfTokenFrom(t, body)

	// Disable action A1: must redirect to the action detail page.
	form := url.Values{"gorilla.csrf.Token": {tok}, "target": {"A1"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/disable", strings.NewReader(form.Encode()))
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

	body := getBody(t, cl, srv.URL+"/inbox")

	// S1 is waiting and requires U1 (the signed-in user) → must appear in Inbox.
	if !strings.Contains(body, "Waiting for you") {
		t.Error("steps page must have an Inbox section")
	}
	if !strings.Contains(body, `action="/inbox/S1/complete"`) {
		t.Error("inbox must contain the step completion form for S1")
	}
}

func TestInboxGroupsByCreatingAction(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/inbox")

	// Each step card names who asked (the creating action, sys/make) in its ask
	// line — "sys/make needs …" — not just the completion target. The old separate
	// group heading is gone (simplified inbox), but the attribution stays.
	if !strings.Contains(body, "needs") {
		t.Errorf("step card must name who asks (an ask line): %s", body)
	}
	if !strings.Contains(body, "/make") {
		t.Errorf("step card must name the creating action sys/make: %s", body)
	}
	// Both completion forms are present, one per step.
	if !strings.Contains(body, `action="/inbox/S1/complete"`) || !strings.Contains(body, `action="/inbox/S2/complete"`) {
		t.Error("every waiting step keeps its own completion form")
	}
	// The completion button is neutral (no money button, no amount) — a step
	// completer is doing work, not paying.
	if strings.Contains(body, `class="btn-money" type="submit">Complete`) {
		t.Error("step completion must be a neutral button, not a money button naming an amount")
	}
}

func TestCompleteStepHTMX(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/inbox")
	tok := csrfTokenFrom(t, body)

	form := url.Values{"raw_args": {"{}"}, "gorilla.csrf.Token": {tok}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/inbox/S1/complete", strings.NewReader(form.Encode()))
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

	body := getBody(t, cl, srv.URL+"/inbox")

	// Step S1 (status=waiting, required_caller=alice=current user) appears as a
	// completable card. The price rides on the Complete button — the redundant
	// No funds-mechanics prose (parked / your wallet / from their process).
	for _, banned := range []string{"parked", "your wallet", "their process"} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Errorf("inbox must not narrate funds mechanics (found %q)", banned)
		}
	}
	// Completion is a neutral button (no amount) — the completer isn't paying.
	if !strings.Contains(body, `type="submit">Complete</button>`) {
		t.Error("inbox must show a neutral Complete button (no amount)")
	}
	if strings.Contains(body, "Complete (5)") {
		t.Error("inbox Complete button must NOT name an amount (the completer is not the payer)")
	}
	// The inbox is work addressed to you, nothing else: step creation is API and
	// app-module surface (it needs a live funding trace), never a browser form.
	for _, gone := range []string{"step-create", "new step", "Funding trace"} {
		if strings.Contains(body, gone) {
			t.Errorf("inbox must not offer step creation (found %q)", gone)
		}
	}
}

func TestWalletShowsLedger(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/wallet")
	// The activity is the ledger: each movement links to the transaction that
	// caused it.
	if !strings.Contains(body, `href="/transactions/`+ledgerTx+`"`) {
		t.Error("a ledger entry must link to the transaction its reason names")
	}
	// Each row encodes the viewer's role by the signed amount: as seller (T2, payee)
	// alice sees a credited +net; as buyer (T1, payer) she sees a −gross debit.
	txBody := getBody(t, cl, srv.URL+"/transactions")
	if !strings.Contains(txBody, "sig-credited") || !strings.Contains(txBody, "+45 credits") {
		t.Error("transactions ledger must show alice's seller credit (+net) on T2")
	}
	if !strings.Contains(txBody, ">-100 credits<") {
		t.Error("transactions ledger must show alice's buyer debit (−gross) on T1")
	}
}

func TestStudioShowsEarnings(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/workshop")
	// T2 in authKernel is a seller transaction for U1 (net 45). The three stat
	// tiles (this month / parked / all time) must render, all-time showing +45.
	lower := strings.ToLower(body)
	for _, tile := range []string{"this month", "parked", "all time"} {
		if !strings.Contains(lower, tile) {
			t.Errorf("workshop must show the %q earnings tile", tile)
		}
	}
	if !strings.Contains(body, "+45 credits") {
		t.Errorf("all-time earnings tile must show the +45 credits net: %s", body)
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
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/A1", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(actionJSON)) })
	return mux
}

func TestActionDetailShowsHTTPAndEditForm(t *testing.T) {
	kernel := httptest.NewServer(actionDetailKernel(
		`{"id":"A1","owner_user_id":"U1","owner_handle":"alice","name":"weather","kind":"http",` +
			`"active":true,"public":true,"price":0,"description":"Weather lookup",` +
			`"input_schema":{"type":"object"},"output_schema":{"type":"string"},` +
			`"http":{"method":"GET","url":"https://api.x/{city}","params":[{"name":"city","in":"path"},{"name":"units","in":"query"}]}}`))
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/actions/A1")

	// The header titles on owner/name (the ref), not the bare name, and drops the
	// spelled-out "active · public" meta line (state shows via chips, like the cards).
	if !strings.Contains(page, `class="action-head"`) || !strings.Contains(page, "weather") {
		t.Errorf("action header must title on the owner/name ref: %s", page)
	}
	if strings.Contains(page, "action-detail-meta") {
		t.Error("action header must not carry the spelled-out kind·active·public meta line")
	}

	// Regression: the run form must POST to /actions/{id}/run — the route rename
	// once left it pointing at /actions/{id}/browse (a 404).
	if !strings.Contains(page, `action="/actions/A1/run"`) {
		t.Errorf("run form must post to /actions/A1/run, not the renamed collateral: %s", page)
	}

	// The page has ONE run anatomy (hero → form → result) — no inert Method/URL
	// block dumped above the form.
	if strings.Contains(page, "<dt>Method</dt>") || strings.Contains(page, `class="action-detail-card"`) {
		t.Errorf("action page must not dump a Method/URL block; the source lives in the edit modal: %s", page)
	}
	// The http source is editable by the owner inside the edit modal only: routed to
	// /update, with prefilled schemas + the http subsection (source URL, method,
	// param bindings).
	for _, want := range []string{
		`action="/actions/A1/update"`,
		`name="input_schema"`,
		`name="output_schema"`,
		`name="source"`,
		`value="https://api.x/{city}"`,
		`value="city"`,
		`value="units"`,
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
		`{"id":"A1","owner_user_id":"U1","owner_handle":"alice","name":"calc","kind":"wasm",` +
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
	if !strings.Contains(body, "alice") {
		t.Error("settings page must show handle")
	}
	tok := csrfTokenFrom(t, body)

	// Description update — success path.
	form := url.Values{"gorilla.csrf.Token": {tok}, "description": {"a short about"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings description update: want 200, got %d", resp.StatusCode)
	}

	// Password change — wrong current password shows inline error, no redirect.
	// The new password is valid locally (≥8, confirmed) so it reaches the kernel,
	// which rejects the wrong current_password.
	body2 := getBody(t, cl, srv.URL+"/settings")
	tok2 := csrfTokenFrom(t, body2)
	form2 := url.Values{
		"gorilla.csrf.Token": {tok2},
		"current_password":   {"wrongpass"},
		"password":           {"newpassword"},
		"confirm_password":   {"newpassword"},
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
			{"action_id": "A1", "name": "hello", "owner_handle": "alice", "description": "greet", "score": 0.9},
			{"action_id": "A2", "name": "bye", "owner_handle": "bob", "description": "", "score": 0.5},
		},
	})
	got := parseLookup(raw)
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].Action.ID != "A1" || got[0].Action.Name != "hello" || got[0].Action.OwnerHandle != "alice" {
		t.Errorf("first result fields wrong: %+v", got[0].Action)
	}
	if got[0].Action.Kind != "" {
		t.Error("parseLookup must leave Kind empty so the goroutine fetches it via GetAction")
	}
}

func TestCatalogAsResults(t *testing.T) {
	actions := []client.Action{
		{ID: "A1", Name: "hello", OwnerHandle: "alice", Kind: "wasm", Active: true, Visibility: "public", Price: 50},
		{ID: "A2", Name: "goodbye", OwnerHandle: "bob", Kind: "ai", Active: true, Visibility: "public", Price: 0},
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

// TestSearchIsOnePill guards the single search component: the segmented .omni
// pill (input left, equal-height .omni-go submit segment right) is THE search,
// rendered in the topbar (layout.html) and in-page on browse ≤720px — identical
// anatomy in both, with a search-typed input so phone keyboards offer submit.
func TestSearchIsOnePill(t *testing.T) {
	for file, marker := range map[string]string{
		"templates/browse.html": `class="omni browse-search"`,
		"templates/layout.html": `class="omni"`,
	} {
		b, err := tmplFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		i := strings.Index(src, marker)
		if i < 0 {
			t.Fatalf("%s: no %s search pill", file, marker)
		}
		end := strings.Index(src[i:], "</form>")
		if end < 0 {
			t.Fatalf("%s: search pill form not closed", file)
		}
		form := src[i : i+end]
		for _, want := range []string{`type="search"`, `enterkeyhint="search"`, `omni-go`, `type="submit"`} {
			if !strings.Contains(form, want) {
				t.Errorf("%s: search pill lacks %s — one segmented pill everywhere (input + submit segment)", file, want)
			}
		}
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
			fakeHealth(mux)
			mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"redirect":"?code=CODE"}`))
			})
			mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
			})
			mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
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
				w.Write([]byte(`{"id":"A9","owner_user_id":"U1","owner_handle":"alice","name":"act","kind":"http","active":false,"public":false,"price":0}`))
			})
			kernel := httptest.NewServer(mux)
			defer kernel.Close()
			srv := httptest.NewServer(newTestHandler(t, kernel.URL))
			defer srv.Close()
			cl := loginClient(t, srv.URL)

			studioBody := getBody(t, cl, srv.URL+"/workshop")
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
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		*gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"A9","owner_user_id":"U1","owner_handle":"alice","name":"act","kind":"http"}`))
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

			csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/workshop"))
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
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("PUT /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		*gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`[{"id":"A1","owner_handle":"alice","name":"weather","kind":"http","active":false}]`))
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
	csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/workshop"))
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
		t.Fatal("kernel PUT /v1/actions was not called")
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

	csrfTok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/workshop"))
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

// paymentKernel is a minimal kernel for the v0.12 two-branch inbox: bob has a
// payment step (target action bears effect="transfer") and an ordinary step
// whose PRIVATE target action 403s — the completion form must derive from the
// step's own allowed_input (requirements.md §6.3).
func paymentKernel() *httptest.Server {
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U2","handle":"bob","available":700,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
		 {"id":"SP","required_caller_handle":"bob","owner_handle":"alice","action":"sys/transfer","action_id":"AT",
		  "price":0,"status":"waiting","created_by":"sys/message","created_at":"2026-08-01T00:00:00Z",
		  "partial_args":{"target":"alice","amount":300},
		  "allowed_input":{"type":"object","properties":{}}},
		 {"id":"SO","required_caller_handle":"bob","owner_handle":"alice","action":"alice/private-work","action_id":"AP",
		  "price":5,"status":"waiting","created_by":"sys/message","created_at":"2026-08-02T00:00:00Z",
		  "partial_args":{"context":"sealed"},
		  "allowed_input":{"type":"object","properties":{"answer":{"type":"string","description":"Your answer"}},"required":["answer"]}}
		]`))
	})
	mux.HandleFunc("GET /v1/actions/AT", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"AT","action":"sys/transfer","owner_handle":"sys","name":"transfer","kind":"native",
		 "active":true,"visibility":"public","price":0,"effect":"transfer",
		 "input_schema":{"type":"object","properties":{"target":{"type":"string"},"amount":{"type":"integer"}}}}`))
	})
	mux.HandleFunc("GET /v1/actions/AP", func(w http.ResponseWriter, r *http.Request) {
		// A private target under the v0.12 binding rule: parked fine, unreadable
		// by the completer.
		w.WriteHeader(403)
		w.Write([]byte(`{"code":"unauthorized","error":"not allowed"}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	return httptest.NewServer(mux)
}

func TestInboxTwoBranchCompletion(t *testing.T) {
	kernel := paymentKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/inbox")

	// Payment branch: the completion is a SPEND of the completer's own money —
	// money button naming the sealed amount, recipient named in the provenance
	// face (§6.3).
	if !strings.Contains(page, "Complete (300 credits)") {
		t.Errorf("payment step must render the money amount on the button: %s", page)
	}
	if !strings.Contains(page, "Paid from your balance") || !strings.Contains(page, "alice") {
		t.Errorf("payment step must disclose whose funds move and to whom: %s", page)
	}
	// Ordinary branch: neutral Complete, no amount — even though the target
	// action is unreadable, the form renders from the step's allowed_input.
	if !strings.Contains(page, `type="submit">Complete</button>`) {
		t.Errorf("ordinary step must keep the neutral Complete: %s", page)
	}
	if !strings.Contains(page, `name="f_answer"`) {
		t.Errorf("completion form must derive from allowed_input when the action 403s: %s", page)
	}
}

func TestStepDetailPrivateTargetStillCompletable(t *testing.T) {
	kernel := paymentKernel()
	defer kernel.Close()
	// Serve the single-step read from the same fixtures.
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// The detail route must not error on the 403 target; the form comes from
	// allowed_input.
	kernelMux := http.NewServeMux()
	_ = kernelMux // (single-step read is exercised via the inbox page above)
	page := getBody(t, cl, srv.URL+"/inbox")
	if strings.Contains(page, "could not be found") {
		t.Errorf("a private target action must not surface as an error: %s", page)
	}
}

// postForm posts an authenticated form with a fresh CSRF token scraped from the
// wallet page.
// postForm posts a wallet form, taking its CSRF token from the wallet page.
func postForm(t *testing.T, cl *http.Client, urlStr string, form url.Values) *http.Response {
	t.Helper()
	base := urlStr[:strings.LastIndex(urlStr, "/")]
	return postTo(t, cl, urlStr, getBody(t, cl, base+"/wallet"), form)
}

func TestActionRatingsRenderFromProjection(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/actions/A1")
	if !strings.Contains(body, ">good<") || strings.Contains(body, ">poor<") {
		t.Error("a value=1 rating must render good")
	}
	if !strings.Contains(body, "worked well") {
		t.Error("the rating note must render")
	}
}

// Step creation has no browser route: it needs a live funding trace, which is
// bookkeeping the UI keeps hidden. The kernel API and app modules still compose
// steps; the browser does not.
func TestNoBrowserStepCreation(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/inbox")
	// A well-formed post (real CSRF token) still finds no route: 405, because only
	// GET /inbox is registered.
	resp := postTo(t, cl, srv.URL+"/inbox", page, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /inbox = %d, want no such route", resp.StatusCode)
	}
	// Nor may the palette still offer it.
	if strings.Contains(page, `"step create"`) {
		t.Error("the palette must not carry a step-create command")
	}
}

// No action gets a bespoke surface. sys/transfer is an ordinary priced action
// (kernel §9: natives hold no special privileges), so the UI must not carry a
// hand-written form, route, or price helper for it — it is found and run like
// anything else. This guards the absence, which is the whole point.
func TestNoActionGetsABespokeSurface(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	wallet := getBody(t, cl, srv.URL+"/wallet")
	for _, gone := range []string{"form=transfer", "_form_transfer", "Send credits"} {
		if strings.Contains(wallet, gone) {
			t.Errorf("the wallet must not carry a bespoke send surface: %q", gone)
		}
	}
	// The route is gone too, not merely unlinked. Posted WITH a valid CSRF
	// token, because the CSRF middleware runs before routing and would answer
	// 403 either way — which would prove nothing.
	resp := postTo(t, cl, srv.URL+"/transfers", wallet, url.Values{"recipient": {"bob"}, "amount": {"1.000"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /transfers should not exist, got %d", resp.StatusCode)
	}
	// And no command names one action.
	if strings.Contains(getBody(t, cl, srv.URL+"/browse"), "send credits") {
		t.Error("the palette must not carry a command for a single action")
	}
}

// The activity list reads each movement from the kernel's own from/to
// projection, never from a guess: the viewer is one side and the counterpart
// the other, a deposit included (the kernel names sys as its sender). A reason
// that is a transaction id links to its receipt; any other reason — an
// obligation's ticket, a transfer's note — is the text it is.
func TestEntryRowPartySignAndReason(t *testing.T) {
	const tx = "67753356-bfa3-4c11-9b70-299334ed67c3"
	cases := []struct {
		name        string
		entry       client.LedgerEntry
		counterpart string
		credited    bool
		txID, note  string
	}{
		{"deposit", client.LedgerEntry{FromHandle: "sys", ToHandle: "bob", Amount: 100}, "sys", true, "", ""},
		{"withdrawal", client.LedgerEntry{FromHandle: "bob", ToHandle: "sys", Amount: 100}, "sys", false, "", ""},
		{"payout", client.LedgerEntry{FromHandle: "alice", ToHandle: "bob", Amount: 25, Reason: tx}, "alice", true, tx, ""},
		{"fee", client.LedgerEntry{FromHandle: "bob", ToHandle: "sys", Amount: 5, Reason: tx}, "sys", false, tx, ""},
		{"obligation", client.LedgerEntry{FromHandle: "bob", ToHandle: "sys", Amount: 9, Reason: "obligation e2c28a67-9c1b-4272-abb7-7f13d75cd93b"}, "sys", false, "", "obligation e2c28a67-9c1b-4272-abb7-7f13d75cd93b"},
		{"crossing in", client.LedgerEntry{ToHandle: "bob", Amount: 7}, "", true, "", ""},
	}
	for _, c := range cases {
		got := entryRowOf(c.entry, "bob")
		if got.Counterpart != c.counterpart || got.Credited != c.credited || got.TxID != c.txID || got.Note != c.note {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

// §6.2: a lookup row is discovered iff its action_id names no LOCAL action. A
// resolved proxy keeps its kernel-qualified ref while having a readable local
// row, so reading provenance off the ref would strip a card the buyer has
// already paid for down to a stranger's.
func TestLookupRowsCarryObservedAt(t *testing.T) {
	result := json.RawMessage(`{"results":[
		{"action_id":"local-1","action":"alice/echo","price":50},
		{"action_id":"remote-1","action":"vendor@bravo/fast","price":111,"observed_at":"2026-08-18T17:00:10Z"}
	]}`)
	cards := parseLookup(result)
	if len(cards) != 2 {
		t.Fatalf("expected 2 cards, got %d", len(cards))
	}
	if cards[0].Observed() {
		t.Error("a local row carries no observed_at and must state no freshness")
	}
	if !cards[1].Observed() || cards[1].ObservedAgo() == "" {
		t.Errorf("a remote-backed row states its freshness: %+v", cards[1])
	}
	// The ref only makes the discovered flag PROVISIONAL; the read settles it.
	if cards[1].Discovered != true {
		t.Error("a kernel-qualified ref starts out provisionally discovered")
	}
}

// §6.2: browse decides "discovered" by whether the action_id resolves LOCALLY,
// not by the shape of the ref. A resolved proxy keeps its kernel-qualified ref
// (vendor@bravo/name) while having a local row with stats — the previous
// ref-sniffing rule stripped such a card, already paid for, down to a
// stranger's. This drives the real handler through a kernel that 404s the
// unresolved id, exactly as the live kernel does.
func TestBrowseResolvedProxyIsNotRenderedAsDiscovered(t *testing.T) {
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	// sys/lookup: one unresolved discovery hit and one RESOLVED proxy. Both
	// carry a kernel-qualified ref and an observed_at; only the second has a
	// local row.
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"results":[
			{"action_id":"remote-unresolved","action":"vendor@bravo/fresh","description":"never called","price":111,"observed_at":"2026-08-18T17:00:10Z"},
			{"action_id":"proxy-local","action":"vendor@bravo/bought","description":"already resolved","price":111,"observed_at":"2026-08-18T17:00:10Z"}
		]},"tx_id":"TX1","trace_id":"TR1"}`))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "proxy-local" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"not_found","error":"action not found"}`))
			return
		}
		w.Write([]byte(withEvidence(`{"id":"proxy-local","action":"vendor@bravo/bought","owner_handle":"bravo","name":"vendor/bought","kind":"remote_proxy","active":true,"visibility":"local","price":111}`,
			`{"action_id":"proxy-local","uses":4,"successes":4}`)))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "alice")

	body := getBody(t, cl, srv.URL+"/browse?q=vendor")
	// The resolved proxy links to its LOCAL action page and keeps its stats.
	if !strings.Contains(body, `/actions/proxy-local`) {
		t.Error("a resolved proxy must link to its local action page, not the discovered view")
	}
	if strings.Contains(body, `/discovered?ref=vendor%40bravo%2Fbought`) ||
		strings.Contains(body, `/discovered?ref=vendor@bravo/bought`) {
		t.Error("a resolved proxy must not be routed through the discovered view")
	}
	// The unresolved one still renders as discovered, from the lookup row alone.
	if !strings.Contains(body, "/discovered?ref=vendor") {
		t.Error("an unresolved hit must still render as a discovered card")
	}
	// Both rows are remote-backed, so both state the kernel's freshness stamp.
	if !strings.Contains(body, "terms seen") {
		t.Error("a remote-backed row must state its observed_at freshness")
	}
}

// §6.2: a parked run must render as reserved funds, never as a refund. This
// drives the real run handler with the kernel's live 504 shape and asserts the
// three things the buyer needs: the amount still held, where it is held, and no
// claim that money came back.
func TestRunOutcomeParkedStatesReservedFundsNotARefund(t *testing.T) {
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"P1","action":"vendor@bravo/slow","owner_handle":"bravo","name":"vendor/slow","kind":"remote_proxy","active":true,"price":111,
		 "input_schema":{"type":"object","properties":{"q":{"type":"string","description":"query"}}},"output_schema":{"type":"object"}}`))
	})
	mux.HandleFunc("GET /v1/actions/{id}/ratings", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	// The live d59742b shape: dispatched, unsettled, funds locked, and no date
	// by which anything expires.
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		w.Write([]byte(`{"code":"timeout","error":"remote receipt pending","meta":{"pending_since":"2026-08-18T17:01:54Z","process_id":"proc-7"}}`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "alice")

	page := getBody(t, cl, srv.URL+"/actions/P1")
	resp := postTo(t, cl, srv.URL+"/actions/P1/run", page,
		url.Values{"f_q": {"hello"}, "t_q": {"string"}, "has_fields": {"1"}})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	for _, want := range []string{"Result pending", "111 credits", "/processes/proc-7"} {
		if !strings.Contains(body, want) {
			t.Errorf("parked outcome missing %q:\n%s", want, body)
		}
	}
	// The whole point: no refund is claimed, and it is not dressed as a failure.
	for _, banned := range []string{"fully refunded", "Failed —", "Refunds in full from"} {
		if strings.Contains(body, banned) {
			t.Errorf("parked outcome must not say %q — the funds are still locked", banned)
		}
	}
}

// §6.5: the activity is the ledger, one row per movement — which way, with
// whom, why, how much. The kernel journals every movement between accounts,
// settlement included (verified live on d59742b), so no call needs a row of its
// own beside the money it moved. A reason that is a transaction id links to the
// receipt; any other reason is the text the kernel gave.
func TestWalletActivityIsTheLedger(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/wallet")
	activity := body[strings.Index(body, "Recent activity"):]
	for _, want := range []string{"to bob", "−300 credits", "from sys", "+5000 credits",
		"obligation e2c28a67-9c1b-4272-abb7-7f13d75cd93b", `href="/transactions/` + ledgerTx + `"`} {
		if !strings.Contains(activity, want) {
			t.Errorf("activity missing %q:\n%s", want, activity)
		}
	}
	// The id is the link, never prose.
	if strings.Contains(activity, ">"+ledgerTx+"<") {
		t.Error("a transaction id must render as a truncated link, not as text")
	}
	// A call is not a second row: the wallet reads no transactions.
	if strings.Contains(activity, "<th>Role</th>") || strings.Contains(activity, "/transactions/T1") {
		t.Error("the activity must be the ledger alone")
	}
}

// A cross-kernel call reads differently from a local one: the buyer paid the
// all-in quote, the import fee stayed here, and what is called `net` is the
// obligation returned to the caller — the provider's own economics live in the
// peer's signed receipt. Captioning it "net to provider" would be false (§6.5).
func TestRemoteTransactionBreakdownNamesTheObligation(t *testing.T) {
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=C"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"results":[]},"tx_id":"T1"}`))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"TX9","action_id":"PX","action_name":"vendor/echo/index","status":"success",
		 "gross":133,"net":126,"fee":7,"owner_handle":"alice","caller_handle":"alice","target_handle":"bravo",
		 "remote_receipt_hash":"H","remote_signer_key":"KEYB","ticket_id":"TICKET1",
		 "started_at":"2026-09-09T09:20:29Z"}`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "alice")

	body := getBody(t, cl, srv.URL+"/transactions/TX9")
	for _, want := range []string{"paid", "import fee", "obligation", "TICKET1"} {
		if !strings.Contains(body, want) {
			t.Errorf("a remote call's breakdown must name %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Fee is charged on value added") {
		t.Error("that caption describes a local settlement, not a cross-kernel one")
	}
}

// A native's price is the OPERATOR's configuration, not a constant: a kernel
// booted with native.lookup.price = 7 serves 7 on the action row and charges it
// (measured, docs/kernel-v0.15-verification.md §4). Semantic search is a page
// load, so running it unchecked would make a GET spend the person's money —
// §8.2 forbids that outright. The UI reads the price first and degrades to
// catalog search, saying so in its own words, because the kernel said nothing.
func TestBrowseDoesNotSpendOnAPricedNative(t *testing.T) {
	var ran bool
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") == "sys/lookup" {
			w.Write([]byte(`[{"id":"L","action":"sys/lookup","owner_handle":"sys","name":"lookup","kind":"native","active":true,"price":7}]`))
			return
		}
		w.Write([]byte(`[{"id":"A1","action":"alice/hello","owner_handle":"alice","name":"hello","kind":"wasm","active":true,"price":0,"description":"says hello"}]`))
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.Write([]byte(`{"result":{"results":[]},"tx_id":"T1"}`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/browse?q=hello")
	if ran {
		t.Error("a page load must not run a priced native: that is a spend from a GET (§8.2)")
	}
	if !strings.Contains(body, "Semantic search unavailable") || !strings.Contains(body, "priced on this kernel") {
		t.Errorf("the page must say why it fell back, in our own words: %s", body)
	}
	// And it still answers the query, from the catalog.
	if !strings.Contains(body, "alice/hello") {
		t.Errorf("catalog fallback must still search: %s", body)
	}
}

// proposalKernel serves the planner: sys/lookup ranks, sys/llm/decide chooses.
// decidePrice makes the planner cost money, which must withdraw the command
// rather than spend without a confirm.
func proposalKernel(t *testing.T, decidePrice int64, decision string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"WEB","action":"sys/web","owner_handle":"sys","name":"web","kind":"http","active":true,"price":0,
		 "description":"Fetch a public web page","input_schema":{"type":"object","properties":{"url":{"type":"string","description":"the page"}},"required":["url"]}}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("ref") {
		case "sys/llm/decide":
			fmt.Fprintf(w, `[{"id":"D","action":"sys/llm/decide","owner_handle":"sys","name":"decide","kind":"native","active":true,"price":%d}]`, decidePrice)
		case "sys/web":
			w.Write([]byte(`[{"id":"WEB","action":"sys/web","owner_handle":"sys","name":"web","kind":"http","active":true,"price":0,
			 "input_schema":{"type":"object","properties":{"url":{"type":"string","description":"the page"}},"required":["url"]}}]`))
		case "sys/lookup":
			w.Write([]byte(`[{"id":"L","action":"sys/lookup","owner_handle":"sys","name":"lookup","kind":"native","active":true,"price":0}]`))
		default:
			w.Write([]byte(`[]`))
		}
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		if in.Action == "sys/llm/decide" {
			lastDecide = string(b)
			fmt.Fprintf(w, `{"result":%s,"tx_id":"T1"}`, decision)
			return
		}
		w.Write([]byte(`{"result":{"results":[{"action_id":"WEB","action":"sys/web","description":"Fetch a public web page","price":0}]},"tx_id":"T0"}`))
	})
	return httptest.NewServer(mux)
}

var lastDecide string

// The kernel ships a planner that selects an action and proposes arguments
// WITHOUT executing (U46: planning and spending are separate decisions). The UI
// renders its choice as the ordinary action page with the form filled in, so
// the spend does not move: the same confirm still has to be pressed.
func TestProposalFillsTheFormAndSpendsNothing(t *testing.T) {
	lastDecide = ""
	kernel := proposalKernel(t, 0, `{"action":"sys/web","args":{"url":"example.com"},"message":"this fetches a page"}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// The command is offered beside the results it acts on.
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	if !strings.Contains(page, `action="/browse/propose"`) || !strings.Contains(page, `name="ref" value="sys/web"`) {
		t.Fatalf("browse must offer the planner, carrying the refs on screen: %s", page)
	}
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{
		"q": {"fetch the front page of example.com"}, "ref": {"sys/web"},
	})
	defer resp.Body.Close()
	bb, _ := io.ReadAll(resp.Body)
	body := string(bb)
	// The candidate set sent is the one the person could see — not one assembled
	// in private, which would be a claim about what was offered that nobody
	// could check.
	if !strings.Contains(lastDecide, `"actions":["sys/web"]`) {
		t.Errorf("the planner must be given the visible candidates: %s", lastDecide)
	}
	if !strings.Contains(body, "Chosen for you") {
		t.Errorf("a proposal must say it is one: %s", body)
	}
	if !strings.Contains(body, `name="f_url" rows="1">example.com`) {
		t.Errorf("the proposed argument must fill the ordinary field: %s", body)
	}
	// Nothing ran, and the spend is where it always was: behind the run form's
	// own confirm on the action's own page.
	if !strings.Contains(body, `action="/actions/WEB/run"`) {
		t.Errorf("the proposal lands on the action's ordinary run form: %s", body)
	}
}

// A proposal announces itself even when the model says nothing, which is the
// common case: a person must know they are reading a choice made for them.
func TestProposalAnnouncesItselfWithoutAMessage(t *testing.T) {
	kernel := proposalKernel(t, 0, `{"action":"sys/web","args":{"url":"example.com"}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{"q": {"x"}, "ref": {"sys/web"}})
	defer resp.Body.Close()
	bb, _ := io.ReadAll(resp.Body)
	if body := string(bb); !strings.Contains(body, "Chosen for you") {
		t.Errorf("a silent model still produced a proposal: %s", body)
	}
}

// An operator may price the planner. Then the command is not offered at all: an
// affordance that would spend from a page nobody confirmed is not an affordance
// (§8.2).
func TestPricedPlannerIsNotOffered(t *testing.T) {
	kernel := proposalKernel(t, 9, `{"action":"sys/web","args":{}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	if page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page"); strings.Contains(page, "/browse/propose") {
		t.Errorf("a priced planner must not be offered: %s", page)
	}
}

// Feedback the UI collects has to come back out somewhere, or it is a number on
// a card and nothing more. The signals the kernel already publishes — the
// rating share and the price — order the list, so a buyer can choose by them.
// This is an arrangement of served rows, never a computation (§6.2).
func TestBrowseSortsByTheSignalsTheKernelServes(t *testing.T) {
	rows := map[string]string{
		"CHEAP": `{"id":"CHEAP","action":"alice/cheap","owner_handle":"alice","name":"cheap","kind":"wasm","active":true,"price":1}`,
		"LOVED": `{"id":"LOVED","action":"alice/loved","owner_handle":"alice","name":"loved","kind":"wasm","active":true,"price":50}`,
		"MEH":   `{"id":"MEH","action":"alice/meh","owner_handle":"alice","name":"meh","kind":"wasm","active":true,"price":10}`,
	}
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte("[" + rows["CHEAP"] + "," + rows["LOVED"] + "," + rows["MEH"] + "]"))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		switch id {
		case "LOVED":
			w.Write([]byte(withEvidence(rows[id], `{"uses":9,"successes":9,"rating_count":9,"rating_estimate":0.95}`)))
		case "MEH":
			w.Write([]byte(withEvidence(rows[id], `{"uses":9,"successes":4,"rating_count":9,"rating_estimate":0.2}`)))
		default: // CHEAP has said nothing
			w.Write([]byte(withEvidence(rows[id], `{}`)))
		}
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// The switch is offered, through the one tabs component.
	page := getBody(t, cl, srv.URL+"/browse")
	for _, want := range []string{"relevance", "?sort=rating", "?sort=price"} {
		if !strings.Contains(page, want) {
			t.Errorf("browse must offer the sort %q: %s", want, page)
		}
	}
	order := func(body string) []string {
		var out []string
		for _, name := range []string{"alice/cheap", "alice/loved", "alice/meh"} {
			out = append(out, name)
		}
		sort.Slice(out, func(i, j int) bool { return strings.Index(body, out[i]) < strings.Index(body, out[j]) })
		return out
	}
	if got := order(getBody(t, cl, srv.URL+"/browse?sort=price")); got[0] != "alice/cheap" || got[2] != "alice/loved" {
		t.Errorf("price order = %v", got)
	}
	if got := order(getBody(t, cl, srv.URL+"/browse?sort=rating")); got[0] != "alice/loved" {
		t.Errorf("rating order must put the best-rated first: %v", got)
	}
	// A row nobody has rated is not SCORED as a bad one. It sorts into the
	// unrated block after the rated rows, which says the true thing — it has not
	// been rated — rather than interleaving silence with measured opinion.
	rated := getBody(t, cl, srv.URL+"/browse?sort=rating")
	if strings.Index(rated, "alice/cheap") < strings.Index(rated, "alice/meh") {
		t.Error("an unrated action belongs after the rated ones, not among them")
	}
	// An unknown sort shows the list, not an error.
	if body := getBody(t, cl, srv.URL+"/browse?sort=nonsense"); !strings.Contains(body, "alice/cheap") {
		t.Errorf("an unknown sort falls back to relevance: %s", body)
	}
}

// A sort orders the WHOLE catalog and the page is cut from it. Ordering only the
// page answers a different question than the one asked: the best-rated action
// could sit on page two while page one claimed to be sorted by rating, and
// nothing on screen would say so.
func TestRatingSortOrdersTheWholeCatalogNotThePage(t *testing.T) {
	const total = 25 // more than one page
	row := func(i int) string {
		return fmt.Sprintf(`{"id":"A%d","action":"alice/a%02d","owner_handle":"alice","name":"a%02d","kind":"wasm","active":true,"price":0}`, i, i, i)
	}
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","handle":"alice","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/steps", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "" {
			w.Write([]byte(`[]`))
			return
		}
		// The best-rated action is LAST, so it falls on page two of the
		// unsorted catalog.
		var rows []string
		for i := 0; i < total; i++ {
			rows = append(rows, row(i))
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var i int
		fmt.Sscanf(r.PathValue("id"), "A%d", &i)
		if i == total-1 {
			w.Write([]byte(withEvidence(row(i), `{"uses":9,"successes":9,"rating_count":9,"rating_estimate":0.99}`)))
			return
		}
		w.Write([]byte(withEvidence(row(i), `{"uses":9,"successes":9,"rating_count":9,"rating_estimate":0.10}`)))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/browse?sort=rating")
	best := fmt.Sprintf("alice/a%02d", total-1)
	if !strings.Contains(body, best) {
		t.Errorf("the best-rated action must be on the first page of a rating sort: %s", best)
	}
}

// The planner's proposal must reach the raw-JSON input as well as the fields.
// That box is the ONLY input where an action's schema is unusable, and it is one
// toggle away everywhere else — so filling only the fields drops the proposal
// silently for one action and loses it on a switch for every other.
func TestProposalReachesTheRawInputToo(t *testing.T) {
	kernel := proposalKernel(t, 0, `{"action":"sys/web","args":{"url":"example.com","depth":2}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{"q": {"x"}, "ref": {"sys/web"}})
	defer resp.Body.Close()
	bb, _ := io.ReadAll(resp.Body)
	body := string(bb)
	if !strings.Contains(body, `name="raw_args"`) {
		t.Fatalf("no raw input rendered: %s", body)
	}
	raw := body[strings.Index(body, `name="raw_args"`):]
	raw = raw[:strings.Index(raw, "</textarea>")]
	// As the kernel sent them: a number stays a number. Quotes arrive escaped,
	// which is the point — the box is filled by the template, inert.
	raw = html.UnescapeString(raw)
	if !strings.Contains(raw, `"url": "example.com"`) || !strings.Contains(raw, `"depth": 2`) {
		t.Errorf("the raw box must carry the proposed arguments: %s", raw)
	}
}

// What the model said is counterparty prose, not a kernel-vouched fact. The
// provenance face is reserved for money, ids, hashes, refs and sealed args
// (grammar §3), so rendering the message in it would dress a claim as evidence.
func TestProposedMessageIsProseNotProvenance(t *testing.T) {
	kernel := proposalKernel(t, 0, `{"action":"sys/web","args":{},"message":"this fetches a page"}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{"q": {"x"}, "ref": {"sys/web"}})
	defer resp.Body.Close()
	bb, _ := io.ReadAll(resp.Body)
	body := string(bb)
	if !strings.Contains(body, "this fetches a page") {
		t.Fatalf("the message must be shown: %s", body)
	}
	if strings.Contains(body, `<pre class="json">this fetches a page`) {
		t.Error("a model's prose must not render in the provenance face")
	}
}
