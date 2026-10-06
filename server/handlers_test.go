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
	"os"
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
	lastRename         string
	lastDeposit        string
	lastSignIn         string // the account an authorize named
	lastProcessesQuery string // the query the last process listing sent
)

// fakeHealth gives a fake kernel the identity banner every login pins: its key,
// the network fingerprint, and the decimals that scale every amount (§5, §7).
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
		 "network":"play","network_fingerprint":"D1","decimals":0,"symbol":"credits","blockchain_address":""}`))
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
	lastRename, lastDeposit, lastSignIn, lastProcessesQuery = "", "", "", ""
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Handle string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		lastSignIn = in.Handle
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","description":"","available":500,"locked":0,
		 "connectors":[{"directory":"sys@testnet","connections":[{"provider":"gmail.com","actions":1,"unused":false,"provider_key":"oauth:G","created_at":"2024-01-01T00:00:00Z"}],
		   "actions":[{"action":"sys@testnet/gmail-send","scopes":["send"],"provider_key":"oauth:G","created_at":"2024-01-01T00:00:00Z"}]}],
		 "connections":[{"provider":"gmail.com","actions":1,"unused":false,"provider_key":"oauth:G","created_at":"2024-01-01T00:00:00Z"}]}`))
	})
	mux.HandleFunc("POST /v1/grants/start", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"ST1","authorize_url":"https://provider.test/auth?client_id=cid&state=ST1"}`))
	})
	mux.HandleFunc("GET /v1/grants/plan", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"groups":[{"provider_key":"bearer:api.lena.dev","provider":"api.lena.dev","scheme":"delegated_bearer","scopes":[],"connected":true,"covered":true,"actions":[{"action_id":"AL","action":"lena@testnet/api","granted":true}]}],"skipped_loginless":0,"skipped_uncallable":0}`))
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","description":"about me","available":500,"locked":0}`))
	})
	mux.HandleFunc("POST /v1/auth/recover/start", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"nonce":"NONCE1","expires_in_seconds":600}`))
	})
	mux.HandleFunc("POST /v1/auth/recover/complete", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		lastProcessesQuery = r.URL.RawQuery
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		// T1: alice is buyer; T2: alice is seller. Both reference action "owned".
		w.Write([]byte(`[
			{"id":"T1","action_id":"owned","action":"alice@testnet/x","owner":"alice@testnet","target":"bob@testnet","caller":"alice@testnet","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z"},
			{"id":"T2","action_id":"owned","action":"alice@testnet/x","owner":"bob@testnet","target":"alice@testnet","caller":"bob@testnet","status":"success","gross":50,"fee":5,"net":45,"started_at":"2024-01-02T00:00:00Z"}
		]`))
	})
	// The journal, in the d59742b shape (verified live): every movement between
	// two accounts, settlement included. alice's fee and her payout to bob name
	// the settling transaction; an obligation names its ticket; the operator's
	// deposit names sys as its sender.
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"amount":300,"reason":"` + ledgerTx + `","created_at":"2024-01-02T12:00:00Z","operator":"alice@testnet","from":"alice@testnet","to":"bob@testnet"},
			{"amount":9,"reason":"obligation e2c28a67-9c1b-4272-abb7-7f13d75cd93b","created_at":"2024-01-02T11:00:00Z","operator":"sys@testnet","from":"alice@testnet","to":"sys@testnet"},
			{"amount":5000,"reason":"","created_at":"2024-01-01T00:00:00Z","operator":"sys@testnet","from":"sys@testnet","to":"alice@testnet"}
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
		"A1": `{"id":"A1","action":"alice@testnet/hello","name":"hello","kind":"wasm","active":true,"public":true,"price":0,` + schemas + `}`,
		"A3": `{"id":"A3","action":"alice@testnet/draft","name":"draft","kind":"wasm","active":false,"public":false,"price":0,` + schemas + `}`,
		// Two delegated actions: /v1/me grants sys/gmail-send (AG) but not
		// sys/gmail-read (AN) — the needs-account chip must split them.
		"AG": `{"id":"AG","action":"sys@testnet/gmail-send","name":"gmail-send","kind":"http","active":true,"public":true,"price":0,"auth_scheme":"oauth_delegated","requires_grant":true,` + schemas + `}`,
		"AN": `{"id":"AN","action":"sys@testnet/gmail-read","name":"gmail-read","kind":"http","active":true,"public":true,"price":0,"auth_scheme":"oauth_delegated","requires_grant":true,` + schemas + `}`,
	}
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		// The catalog (kernel 034426e): the caller's own actions in every
		// state — A3 is alice's inactive private draft — and every other live
		// one; ?owner= narrows it to one owner's.
		if owner := r.URL.Query().Get("owner"); owner != "" {
			var mine []string
			for _, id := range []string{"A1", "A3", "AG", "AN"} {
				if strings.Contains(rows[id], `"action":"`+owner+`/`) {
					mine = append(mine, rows[id])
				}
			}
			w.Write([]byte("[" + strings.Join(mine, ",") + "]"))
		} else {
			w.Write([]byte("[" + rows["A1"] + "," + rows["A3"] + "," + rows["AG"] + "," + rows["AN"] + "]"))
		}
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		// A listed action reads back as its row; "owned" belongs to alice (the
		// signed-in user), and anything else is bob's.
		id := r.PathValue("id")
		row, ok := rows[id]
		if !ok {
			owner := "bob@testnet"
			if id == "owned" {
				owner = "alice@testnet"
			}
			row = `{"id":"` + id + `","action":"` + owner + `/x","name":"x","kind":"http",` +
				`"active":true,"public":true,"price":0,` + schemas + `}`
		}
		w.Write([]byte(withEvidence(row, `{"action_id":"`+id+`"}`)))
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"status":"success","action_id":"A2","action_name":"synthesized","diagnostics":[]},"tx_id":"TX1","trace_id":"TR1"}`))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"T1","action_id":"owned","action":"alice@testnet/x","owner":"alice@testnet","target":"bob@testnet","caller":"alice@testnet","status":"success","gross":100,"fee":10,"net":90,"started_at":"2024-01-01T00:00:00Z",` +
			`"args":{"q":"<b>x</b>"},"result":{"items":["a","b"],"text":"line1\nline2","ok":true,"none":null}}`))
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
		w.Write([]byte(`[{"id":"` + mutationTarget(r) + `","action":"alice@testnet/x","name":"x","kind":"http","active":true,"price":0}]`))
	})
	mux.HandleFunc("DELETE /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"` + r.URL.Query().Get("target") + `"}]`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		// Two waiting tasks spawned by the same creating action (sys/make) — the
		// common case: one process fans out several tasks sharing one created_by.
		w.Write([]byte(`[
			{"id":"S1","required_caller":"alice@testnet","owner":"bob@testnet","action":"alice@testnet/hello","price":5,"status":"waiting","created_by":"sys@testnet/make","created_at":"2026-01-01T00:00:00Z"},
			{"id":"S2","required_caller":"alice@testnet","owner":"bob@testnet","action":"alice@testnet/hello","price":3,"status":"waiting","created_by":"sys@testnet/make","created_at":"2026-01-02T00:00:00Z"}
		]`))
	})
	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","required_caller":"alice@testnet","owner":"bob@testnet","action":"alice@testnet/hello","price":5,"partial_args":{},"status":"waiting","created_at":"2026-01-01T00:00:00Z"}`))
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
		w.Write([]byte(`{"id":"U9","address":"new@testnet","email":"new@test.com","available":0,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"U1","address":"alice@testnet","email":"alice@test.com","available":500,"locked":0}]`))
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
		w.Write([]byte(`{"address":"renamed@testnet"}`))
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
		w.Write([]byte(`{"amount":100,"external_key":"attr:rail:ref:` + body.Ref + `","operator":"sys@testnet","from":"sys@testnet","to":"` + h + `"}`))
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
		  "party":"0x70997970c51812dc3a010c7d01b50e0d17dc79c8"}],
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
		 "actions":[{"action_id":"a2","name":"synth@kernelc/make","description":"synth","price":20,"indicative":true}],
		 "account":` + account + `,
		 "evidence":[{"issuer_public_key":"` + key + `","subject_action_id":"a2","uses":6,"successes":6,"failures":0,
		    "corroborated_uses":0,"avg_latency_ms":690.5,"rating_count":2,"rating_mean":1,"unverified_ratings":1},
		   {"issuer_public_key":"KEYB","subject_action_id":"a2","uses":1,"successes":0,"failures":1,
		    "corroborated_uses":1,"avg_latency_ms":0,"rating_count":0,"rating_mean":0,"unverified_ratings":0}],
		 "online":true,"source":"live",
		 "reachability":{"path":"direct","rtt_millis":4,"protocols":["/juice/fed/gossip/1"]}}`))
	})
	mux.HandleFunc("GET /v1/admin/kernel", func(w http.ResponseWriter, r *http.Request) {
		// v0.14: the kernel's identity, the money rules it serves under, its rail
		// position, and its credit position — what it has delivered unpaid against
		// the limit it set (kernel §D14). `stop` is absent unless the rail halts.
		w.Write([]byte(`{"handle":"kernel","public_key":"KEY","about":"our test kernel","addrs":[],
		 "network":"play","network_fingerprint":"ef1f","blockchain_address":"","finalized":null,
		 "sys":{"earnings":170,"pending_payouts":0,"held_deposits":0,"refill_locks":0},
		 "solvency":{"liabilities":8126,"vault":8126,"gap":0},
		 "custody":{"checked":false,"difference":0,"ok":false},
		 "lottery":10,"credit_limit":1000,"exposure":320,
		 "fee_bps":2000,"remote_bps":500,"import_bps":500}`))
	})
	mux.HandleFunc("POST /v1/tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{},"tx_id":"T2","trace_id":"TR2","receipt_id":"R2","task_id":"` + r.PathValue("id") + `"}`))
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
	own := getBody(t, cl, srv.URL+"/users/alice@testnet")
	if !strings.Contains(own, "alice") || !strings.Contains(own, `class="action-row`) {
		t.Error("owner page must show the handle and action rows")
	}
	if !strings.Contains(own, "draft") {
		t.Error("your own owner page must include your private/inactive actions")
	}
	if strings.Contains(own, "Public actions") {
		t.Error("owner page must not label the whole list 'Public actions'")
	}

	// Someone else's page: caller-visible active set only (all=0) — no private/inactive.
	if strings.Contains(getBody(t, cl, srv.URL+"/users/bob@testnet"), "draft") {
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
	if !strings.Contains(body, `href="/users/alice@testnet"`) {
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
	if !strings.Contains(body, `href="/users/alice@testnet"`) {
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
		w.Write([]byte(`{"status":"ok","handle":"tango","public_key":"PK","network":"test","network_fingerprint":"D9","decimals":6,"symbol":"USDC","blockchain_address":"0xKERNEL"}`))
	})
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=C"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":5000000,"locked":0,"blockchain_address":"0xALICE"}`))
	})
	mux.HandleFunc("GET /v1/withdrawals", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"W1","kind":"payout","amount":10,"status":"confirmed","tx_hash":"manual:W1","created_at":"2026-09-09T09:17:26Z"}]`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
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
	if !strings.Contains(body, `name="action" value="sys@testnet/gmail-send"`) {
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
	form := url.Values{"action": {"lena@testnet/api"}, "token": {"PAT-123"}, "gorilla.csrf.Token": {tok}}
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
	if !strings.Contains(got, `"selector":"lena@testnet/api"`) || !strings.Contains(got, `"token":"PAT-123"`) {
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
			w.Write([]byte(`{"id":"A9","action":"alice@testnet/priced","name":"priced","kind":"http","price":12}`))
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
		"title":              {"Priced thing"},
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
	if !strings.Contains(got, `"title":"Priced thing"`) {
		t.Errorf("the title must reach the kernel; kernel body = %s", got)
	}
}

// TestCreateActionRefusedKeepsDraft: a refused create — the kernel's refusal or
// a local parse error — re-renders the workshop with the create dialog open,
// the message inside it, and everything typed kept, except a secret, which
// never reaches a page and is asked for again.
func TestCreateActionRefusedKeepsDraft(t *testing.T) {
	cases := []struct {
		name, price, wantMsg string
		kernelCalled         bool
	}{
		{"kernel refusal", "1", "Please check your input: title is longer than 80 characters", true},
		{"bad amount", "abc", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			mux := http.NewServeMux()
			fakeHealth(mux)
			mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"redirect":"?code=CODE"}`))
			})
			mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
			})
			mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
			})
			mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
			mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
			mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"code":"invalid_input","error":"title is longer than 80 characters"}`))
			})
			kernel := httptest.NewServer(mux)
			defer kernel.Close()
			srv := httptest.NewServer(newTestHandler(t, kernel.URL))
			defer srv.Close()
			cl := loginClient(t, srv.URL)

			fields := url.Values{
				"gorilla.csrf.Token": {csrfTokenFrom(t, getBody(t, cl, srv.URL+"/workshop"))},
				"title":              {"My weather"},
				"name":               {"weather"},
				"kind":               {"http"},
				"price":              {tc.price},
				"input_schema":       {`{"type":"object","properties":{}}`},
				"auth_scheme":        {"header"},
				"auth_param_name":    {"X-Api-Key"},
				"auth_param_value":   {"s3cret-value"},
			}
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions", strings.NewReader(fields.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := cl.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			page := string(b)

			if called != tc.kernelCalled {
				t.Errorf("kernel called = %v, want %v", called, tc.kernelCalled)
			}
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", resp.StatusCode)
			}
			if !strings.Contains(page, `id="form-action-create" class="modal-form" open`) {
				t.Error("the create dialog must come back open")
			}
			if tc.wantMsg != "" && !strings.Contains(page, tc.wantMsg) {
				t.Errorf("the refusal must be stated in the dialog, want %q", tc.wantMsg)
			}
			if !strings.Contains(page, `banner-error`) {
				t.Error("the dialog must carry the error banner")
			}
			for _, kept := range []string{`value="My weather"`, `value="weather"`, `value="X-Api-Key"`, `<option value="header" selected>`, `{&#34;type&#34;:&#34;object&#34;`} {
				if !strings.Contains(page, kept) {
					t.Errorf("typed input not kept: %q", kept)
				}
			}
			if strings.Contains(page, "s3cret-value") {
				t.Error("a secret must never be written back into the page")
			}
			// The chosen scheme's secret is asked for again; an unchosen scheme's
			// inputs are rendered disabled, so they neither block a submit without
			// JavaScript nor are sent.
			if !strings.Contains(page, `id="c-auth-hval" name="auth_param_value" type="password" autocomplete="off" required>`) {
				t.Error("the chosen scheme's secret must come back empty and required")
			}
			if !strings.Contains(page, `name="auth_bearer_token" type="password" autocomplete="off" required disabled>`) {
				t.Error("an unchosen scheme's secret must be rendered disabled")
			}
		})
	}
}

// TestImportFlashNamesLocationsAndNotices: an import names each refused
// operation with its place in the document and the kernel's reason, and each
// installed operation whose contract the kernel adjusted.
func TestImportFlashNamesLocationsAndNotices(t *testing.T) {
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/actions/import", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"created":[{"id":"P1","name":"pets/good","title":"Add a pet"}],"updated":[],"unchanged":[],"deactivated":[],
			"rejected":[{"key":"choice","location":"POST /echo/choice","reason":"input.properties.x: oneOf is not supported"}],
			"notices":[{"key":"good","location":"POST /echo/good","note":"input.properties.tag: nullable folded into type [T, \"null\"]"}]}`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	fields := url.Values{
		"gorilla.csrf.Token": {csrfTokenFrom(t, getBody(t, cl, srv.URL+"/workshop"))},
		"name":               {"pets"},
		"spec_url":           {"http://127.0.0.1:1/openapi.json"},
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/import", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := html.UnescapeString(string(b))
	for _, want := range []string{
		"pets: 1 created, 0 updated, 0 unchanged, 0 deactivated.",
		"Not installed: choice (POST /echo/choice) — input.properties.x: oneOf is not supported.",
		`Adjusted: good — input.properties.tag: nullable folded into type [T, "null"].`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("import flash missing %q", want)
		}
	}
}

// TestBuildArgsThreeStates: a blank field is omitted, a value is sent, and null
// is sent only when the field's null box is ticked — whatever its input holds.
func TestBuildArgsThreeStates(t *testing.T) {
	form := url.Values{
		"has_fields": {"1"},
		"f_/blank":   {""}, "t_/blank": {"string"},
		"f_/value": {"hi"}, "t_/value": {"string"},
		"f_/nulled": {"ignored"}, "t_/nulled": {"string"}, "n_/nulled": {"1"},
		"f_/obj/y": {"ignored"}, "t_/obj/y": {"string"}, "t_/obj": {"object"}, "n_/obj": {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw, err := buildArgs(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if _, ok := got["blank"]; ok {
		t.Error("a blank field must be omitted")
	}
	if got["value"] != "hi" {
		t.Errorf("value = %v, want hi", got["value"])
	}
	for _, k := range []string{"nulled", "obj"} {
		if v, ok := got[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
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
			w.Write([]byte(`{"status":"connected","actions":["lena@testnet/api"]}`))
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
	form := url.Values{"selector": {"lena@testnet/api"}, "token": {"PAT-123"}, "gorilla.csrf.Token": {tok}}
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

	// Browse lists active public actions as rows: the title links to the
	// action, the owner in the ref to its owner page.
	browse := getBody(t, cl, srv.URL+"/browse")
	if !strings.Contains(browse, `class="action-title"`) {
		t.Error("browse row must carry a title")
	}
	if !strings.Contains(browse, `href="/actions/A1"`) {
		t.Error("browse card name must link to the action")
	}
	if !strings.Contains(browse, `href="/users/alice@testnet"`) {
		t.Error("browse card owner must link to its owner page")
	}
	if strings.Contains(browse, `title="WebAssembly action">W`) {
		t.Error("browse card must NOT show an always-on kind chip (exceptional-only)")
	}
	if !strings.Contains(browse, `class="media tile"`) {
		t.Error("browse row must lead with a tile")
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
	// The connect link must target the action's FOLDER (sys@testnet), not the
	// degenerate single-action selector (sys@testnet/gmail-read) — one paste then
	// connects the whole provider group, per the kernel's directory grouping.
	if sel := connectSelector(t, gated); sel != "sys@testnet" {
		t.Errorf("connect link selector = %q, want the folder sys@testnet (not the action address)", sel)
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
			w.Write([]byte(`{"code":"grant_required","meta":{"action":"sys@testnet/gmail-read"},"error":"grant required"}`))
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
	if sel := connectSelector(t, string(body)); sel != "sys@testnet" {
		t.Errorf("run-outcome connect selector = %q, want the folder sys@testnet (not the action address)", sel)
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

func TestInboxShowsWaitingTask(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/inbox")

	// S1 is waiting and requires U1 (the signed-in user) → must appear in Inbox.
	if !strings.Contains(body, "Waiting for you") {
		t.Error("tasks page must have an Inbox section")
	}
	if !strings.Contains(body, `action="/inbox/S1/complete"`) {
		t.Error("inbox must contain the task completion form for S1")
	}
}

func TestInboxGroupsByCreatingAction(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/inbox")

	// Each task card names who asked (the creating action, sys/make) in its ask
	// line — "sys/make needs …" — not just the completion target. The old separate
	// group heading is gone (simplified inbox), but the attribution stays.
	if !strings.Contains(body, "needs") {
		t.Errorf("task card must name who asks (an ask line): %s", body)
	}
	if !strings.Contains(body, "/make") {
		t.Errorf("task card must name the creating action sys/make: %s", body)
	}
	// Both completion forms are present, one per task.
	if !strings.Contains(body, `action="/inbox/S1/complete"`) || !strings.Contains(body, `action="/inbox/S2/complete"`) {
		t.Error("every waiting task keeps its own completion form")
	}
	// The completion button is neutral (no money button, no amount) — a task
	// completer is doing work, not paying.
	if strings.Contains(body, `class="btn-money" type="submit">Complete`) {
		t.Error("task completion must be a neutral button, not a money button naming an amount")
	}
}

func TestCompleteTaskHTMX(t *testing.T) {
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
		t.Fatalf("complete task returned %d, body: %s", resp.StatusCode, frag)
	}
	if !strings.Contains(frag, `class="outcome success"`) {
		t.Error("complete task HTMX response must show success outcome")
	}
	if strings.Contains(frag, "<html") {
		t.Error("HTMX complete task must return a fragment, not a full page")
	}
}

func TestInboxPageRendersTasks(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/inbox")

	// Task S1 (status=waiting, required_caller=alice=current user) appears as a
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
	// The inbox is work addressed to you, nothing else: task creation is API and
	// app-module surface (it needs a live funding trace), never a browser form.
	for _, gone := range []string{"task-create", "new task", "Funding trace"} {
		if strings.Contains(body, gone) {
			t.Errorf("inbox must not offer task creation (found %q)", gone)
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
		t.Errorf("all-time earnings must show the +45 credits net: %s", body)
	}
	// Every page that leads with money draws it through the one figures
	// component, and links each earnings figure to its transactions.
	if !strings.Contains(body, `<a href="/transactions">1 call all-time →</a>`) {
		t.Error("an earnings figure must link to its transactions")
	}
	for _, page := range []string{"/workshop", "/wallet", "/"} {
		b := getBody(t, cl, srv.URL+page)
		if !strings.Contains(b, `<div class="stat-row">`) || strings.Contains(b, `class="balance`) {
			t.Errorf("%s must render its money through the figures component", page)
		}
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/A1", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(actionJSON)) })
	return mux
}

func TestActionDetailShowsHTTPAndEditForm(t *testing.T) {
	kernel := httptest.NewServer(actionDetailKernel(
		`{"id":"A1","owner_user_id":"U1","action":"alice@testnet/weather","name":"weather","kind":"http",` +
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
		`name="title"`,
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
		`{"id":"A1","owner_user_id":"U1","action":"alice@testnet/calc","name":"calc","kind":"wasm",` +
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
			{"action_id": "A1", "action": "alice@testnet/hello", "name": "hello", "description": "greet", "score": 0.9},
			{"action_id": "A2", "action": "bob@testnet/bye", "name": "bye", "description": "", "score": 0.5},
		},
	})
	got := parseLookup(raw)
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].Action.ID != "A1" || got[0].Action.Name != "hello" || got[0].Action.Ref() != "alice@testnet/hello" {
		t.Errorf("first result fields wrong: %+v", got[0].Action)
	}
	if got[0].Action.Kind != "" {
		t.Error("parseLookup must leave Kind empty so the goroutine fetches it via GetAction")
	}
}

func TestCatalogAsResults(t *testing.T) {
	actions := []client.Action{
		{ID: "A1", Action: "alice@testnet/hello", Name: "hello", Kind: "wasm", Active: true, Visibility: "public", Price: 50},
		{ID: "A2", Action: "bob@testnet/goodbye", Name: "goodbye", Kind: "ai", Active: true, Visibility: "public", Price: 0},
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

	if none := catalogAsResults(actions, "  "); len(none) != 0 {
		t.Fatalf("a query with no words matches nothing, got %d", len(none))
	}
}

// TestSearchIsOnePill guards the single search component: the segmented .omni
// pill (input left, equal-height .omni-go submit segment right) is THE search,
// rendered once, in the topbar (layout.html), present at every width, with a
// search-typed input so phone keyboards offer submit.
func TestSearchIsOnePill(t *testing.T) {
	for file, marker := range map[string]string{
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
		for _, want := range []string{`type="search"`, `enterkeyhint="search"`, `omni-go`, `type="submit"`, `data-search-hints`, `class="omni-suggest"`} {
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
				w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
			})
			mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
				if pastEnd(w, r) {
					return
				}
				w.Write([]byte(`[]`))
			})
			mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`[]`))
			})
			mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{"id":"A9","owner_user_id":"U1","action":"alice@testnet/act","name":"act","kind":"http","active":false,"public":false,"price":0}`))
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		*gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"A9","owner_user_id":"U1","action":"alice@testnet/act","name":"act","kind":"http"}`))
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/A1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"A1","action":"alice@testnet/weather","name":"weather","title":"Weather now","description":"Weather.","kind":"http","active":true,"price":0,
			"input_schema":{"additionalProperties":false,"properties":{"city":{"description":"City.","type":"string"}},"type":"object"},
			"http":{"method":"GET","url":"https://api.example.com/w"}}`))
	})
	mux.HandleFunc("GET /v1/actions/A1/ratings", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("PUT /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		*gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`[{"id":"A1","action":"alice@testnet/weather","name":"weather","kind":"http","active":false}]`))
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

// TestUpdateActionSendsOnlyChanges: the edit dialog shows every field, but the
// kernel deactivates an action on any schema in an update, even an identical
// one, so only what differs from the action as served is sent. A title edit
// with everything else as shown sends the title alone; an untouched dialog
// sends nothing.
func TestUpdateActionSendsOnlyChanges(t *testing.T) {
	var gotBody []byte
	kernel := httptest.NewServer(updateActionKernel(&gotBody))
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	shown := url.Values{
		"gorilla.csrf.Token": {csrfTokenFrom(t, getBody(t, cl, srv.URL+"/workshop"))},
		"title":              {"Weather now"},
		"description":        {"Weather."},
		"price":              {"0"},
		// The served schema as the dialog shows it: indented, so only its value
		// is the same.
		"input_schema": {"{\n  \"type\": \"object\",\n  \"properties\": {\"city\": {\"type\": \"string\", \"description\": \"City.\"}},\n  \"additionalProperties\": false\n}"},
		"source":       {"https://api.example.com/w"},
		"method":       {"GET"},
	}
	post := func(v url.Values) string {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/actions/A1/update", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	if page := post(shown); gotBody != nil || !strings.Contains(page, "No changes.") {
		t.Errorf("an untouched dialog must send nothing; sent %s", gotBody)
	}
	retitled := url.Values{}
	for k, v := range shown {
		retitled[k] = v
	}
	retitled.Set("title", "Weather right now")
	post(retitled)
	if string(gotBody) != `{"target":"A1","title":"Weather right now"}` {
		t.Errorf("a title edit must send the title alone; sent %s", gotBody)
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
	// The refusal re-renders the edit dialog open, with the message inside it
	// and what was typed kept.
	body := string(page)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(body, "The input schema is not valid JSON.") {
		t.Error("invalid schema must surface its message in the dialog")
	}
	if !strings.Contains(body, `id="form-action-edit" class="modal-form" open`) {
		t.Error("the edit dialog must come back open")
	}
	if !strings.Contains(body, "{not json</textarea>") {
		t.Error("the typed schema must be kept")
	}
}

// heldKey is a public key as a task held on another kernel names its holder
// when no petname is bound for it (4dbfedd).
const heldKey = "K98AJeQ6631CO6LJ73nK2PiQW4qY3Nb3eqquXPHYNJ8"

// taskRows are the tasks bob@testnet may list, one of each kind the inbox
// distinguishes (requirements.md §6.3).
var taskRows = map[string]string{
	// A payment task: its target bears effect="transfer".
	"SP": `{"id":"SP","required_caller":"bob@testnet","owner":"alice@testnet","action":"sys@testnet/transfer",
	  "price":0,"status":"waiting","created_by":"sys@testnet/message","created_at":"2026-08-01T00:00:00Z",
	  "partial_args":{"target":"alice@testnet","amount":300},"allowed_input":{"type":"object","properties":{}}}`,
	// An ordinary task on a PRIVATE target the completer cannot read.
	"SO": `{"id":"SO","required_caller":"bob@testnet","owner":"alice@testnet","action":"alice@testnet/private-work",
	  "price":5,"status":"waiting","created_by":"sys@testnet/message","created_at":"2026-08-02T00:00:00Z",
	  "partial_args":{"context":"sealed"},
	  "allowed_input":{"type":"object","properties":{"answer":{"type":"string","description":"Your answer"}},"required":["answer"]}}`,
	// A task another kernel holds for bob: no action, no creator, owner the holder.
	"SH": `{"id":"SH","required_caller":"bob@testnet","owner":"` + heldKey + `",
	  "price":0,"status":"waiting","created_at":"2026-08-03T00:00:00Z",
	  "partial_args":{"message":"from afar"},"allowed_input":{"type":"object"}}`,
	// A task on another kernel's action: never read here.
	"SR": `{"id":"SR","required_caller":"bob@testnet","owner":"alice@testnet","action":"vendor@bravo/thing",
	  "price":0,"status":"waiting","created_by":"alice@testnet/ask","created_at":"2026-08-04T00:00:00Z",
	  "partial_args":{"note":"x"},"allowed_input":{"type":"object"}}`,
	// Bob asked carol: his to withdraw, not to complete.
	"SX": `{"id":"SX","required_caller":"carol@testnet","owner":"bob@testnet","action":"carol@testnet/review",
	  "price":7,"status":"waiting","created_by":"bob@testnet/ask","created_at":"2026-08-05T00:00:00Z",
	  "partial_args":{},"allowed_input":{"type":"object"}}`,
	// Done: history, no gesture.
	"SD": `{"id":"SD","required_caller":"bob@testnet","owner":"alice@testnet","action":"alice@testnet/hello",
	  "price":0,"status":"done","tx_id":"TD","created_at":"2026-07-01T00:00:00Z","partial_args":{}}`,
}

// taskRefReads and taskCancels record what the BFF asked of the task kernel.
var taskRefReads, taskCancels []string

// taskKernel serves taskRows to bob@testnet. A reference read answers the
// transfer action with its effect and nothing else — a private action is not
// served to him.
func taskKernel() *httptest.Server {
	taskRefReads, taskCancels = nil, nil
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U2","address":"bob@testnet","available":700,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		rows := []string{}
		for _, id := range []string{"SP", "SO", "SH", "SR", "SX", "SD"} {
			rows = append(rows, taskRows[id])
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	})
	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(taskRows[r.PathValue("id")]))
	})
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		taskCancels = append(taskCancels, r.PathValue("id"))
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","status":"cancelled"}`))
	})
	mux.HandleFunc("POST /v1/tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "SH" { // completed on the holding kernel
			w.Write([]byte(`{"result":{},"tx_id":"their-tx","trace_id":"tr","receipt_id":"","task_id":"SH"}`))
			return
		}
		w.Write([]byte(`{"result":{},"tx_id":"T9","trace_id":"tr","receipt_id":"R9","task_id":"` + r.PathValue("id") + `"}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			w.Write([]byte(`[]`))
			return
		}
		taskRefReads = append(taskRefReads, ref)
		if ref == "sys@testnet/transfer" {
			w.Write([]byte(`[{"id":"AT","action":"sys@testnet/transfer","name":"transfer","kind":"native",
			 "active":true,"visibility":"local","price":0,"effect":"transfer",
			 "input_schema":{"type":"object","properties":{"target":{"type":"string"},"amount":{"type":"integer"}}}}]`))
			return
		}
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	return httptest.NewServer(mux)
}

func TestInboxTwoBranchCompletion(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/inbox")

	// Payment branch: the completion is a SPEND of the completer's own money —
	// money button naming the sealed amount, recipient named in the provenance
	// face (§6.3).
	if !strings.Contains(page, "Complete (300 credits)") {
		t.Errorf("payment task must render the money amount on the button: %s", page)
	}
	if !strings.Contains(page, "Paid from your balance") || !strings.Contains(page, "alice") {
		t.Errorf("payment task must disclose whose funds move and to whom: %s", page)
	}
	// Ordinary branch: neutral Complete, no amount — even though the target
	// action is unreadable, the form renders from the task's allowed_input.
	if !strings.Contains(page, `type="submit">Complete</button>`) {
		t.Errorf("ordinary task must keep the neutral Complete: %s", page)
	}
	if !strings.Contains(page, `name="f_/answer"`) {
		t.Errorf("completion form must derive from allowed_input: %s", page)
	}
	if strings.Contains(page, "could not be found") {
		t.Errorf("an unreadable target must not surface as an error: %s", page)
	}
}

// The effect is read by the row's action address, once per address, and only
// for an address on this kernel: a reference to another kernel would dial it
// and cache a proxy (§6.3).
func TestInboxReadsEffectsOnlyForLocalAddresses(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	_ = getBody(t, cl, srv.URL+"/inbox")
	got := strings.Join(taskRefReads, ",")
	if got != "sys@testnet/transfer,alice@testnet/private-work" {
		t.Errorf("reference reads = %q, want the two local waiting targets once each", got)
	}
}

// A task held on another kernel names its holder (a key truncated, the whole
// key in the title), completes with the neutral Complete, and links no receipt
// afterwards: its reply names none here (§6.3).
func TestInboxTaskHeldOnAnotherKernel(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/inbox")
	if !strings.Contains(page, `title="`+heldKey+`">`+heldKey[:8]+`…</span>`) {
		t.Errorf("the holder must be named by its truncated key: %s", page)
	}
	if !strings.Contains(page, `action="/inbox/SH/complete"`) {
		t.Error("a task held elsewhere is completable here")
	}

	form := url.Values{"raw_args": {"{}"}, "gorilla.csrf.Token": {csrfTokenFrom(t, page)}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/inbox/SH/complete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frag, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(frag), `class="outcome success"`) {
		t.Fatalf("completion must succeed: %s", frag)
	}
	if strings.Contains(string(frag), "/transactions/") {
		t.Errorf("no receipt is linked when the reply names none: %s", frag)
	}
}

// Decline is offered to the two parties the kernel accepts — the one asked and
// the one who asked — on a waiting task, never in a list row, and posts to the
// kernel's cancel route (§6.3).
func TestInboxDeclineOfferedToBothParties(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/inbox")
	for _, id := range []string{"SP", "SO", "SH", "SR"} {
		if !strings.Contains(page, `action="/inbox/`+id+`/cancel"`) {
			t.Errorf("the one asked may decline %s", id)
		}
	}
	for _, id := range []string{"SX", "SD"} {
		if strings.Contains(page, `action="/inbox/`+id+`/cancel"`) {
			t.Errorf("no decline in a history row (%s)", id)
		}
	}
	if !strings.Contains(page, `data-confirm="Decline this task?"`) {
		t.Error("decline must sit behind a plain confirm")
	}
	// Decline stands in the task's command row, beside Complete, submitting
	// its own form through the form attribute.
	if !regexp.MustCompile(`(?s)<div class="cluster">\s*<button class="btn[^"]*" type="submit">Complete[^<]*</button>\s*<button class="btn-danger" type="submit" form="decline-SP">Decline</button>`).MatchString(page) {
		t.Error("Decline must sit beside Complete in one row")
	}

	asked := getBody(t, cl, srv.URL+"/inbox/SX")
	if !strings.Contains(asked, `action="/inbox/SX/cancel"`) {
		t.Error("the one who asked may decline from the task's page")
	}
	if !strings.Contains(asked, `<div class="cluster"><button class="btn-danger" type="submit" form="decline-SX">Decline</button></div>`) {
		t.Error("a decline without a completion form still stands in a command row")
	}
	if strings.Contains(asked, `action="/inbox/SX/complete"`) {
		t.Error("only the required caller may complete")
	}
	if done := getBody(t, cl, srv.URL+"/inbox/SD"); strings.Contains(done, "/cancel") {
		t.Error("a task no longer waiting offers no decline")
	}

	resp := postTo(t, cl, srv.URL+"/inbox/SX/cancel", asked, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/inbox" {
		t.Errorf("decline = %d → %q, want a redirect to the inbox", resp.StatusCode, resp.Header.Get("Location"))
	}
	if strings.Join(taskCancels, ",") != "SX" {
		t.Errorf("kernel cancels = %v", taskCancels)
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

// Task creation has no browser route: it needs a live funding trace, which is
// bookkeeping the UI keeps hidden. The kernel API and app modules still compose
// tasks; the browser does not.
func TestNoBrowserTaskCreation(t *testing.T) {
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
	// Nor may the command registry still offer it.
	if strings.Contains(page, `"task create"`) {
		t.Error("the registry must not carry a task-create command")
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
		t.Error("the registry must not carry a command for a single action")
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
		{"deposit", client.LedgerEntry{From: "sys", To: "bob", Amount: 100}, "sys", true, "", ""},
		{"withdrawal", client.LedgerEntry{From: "bob", To: "sys", Amount: 100}, "sys", false, "", ""},
		{"payout", client.LedgerEntry{From: "alice", To: "bob", Amount: 25, Reason: tx}, "alice", true, tx, ""},
		{"fee", client.LedgerEntry{From: "bob", To: "sys", Amount: 5, Reason: tx}, "sys", false, tx, ""},
		{"obligation", client.LedgerEntry{From: "bob", To: "sys", Amount: 9, Reason: "obligation e2c28a67-9c1b-4272-abb7-7f13d75cd93b"}, "sys", false, "", "obligation e2c28a67-9c1b-4272-abb7-7f13d75cd93b"},
		{"crossing in", client.LedgerEntry{To: "bob", Amount: 7}, "", true, "", ""},
		// A transfer to or from another kernel names the SENDER's transaction:
		// the sender's own, so linked; on the receiving kernel, the other's, so text.
		{"transfer sent", client.LedgerEntry{From: "bob", To: "sys", Amount: 9, Reason: "transfer " + tx}, "sys", false, tx, ""},
		{"transfer received", client.LedgerEntry{From: "sys", To: "bob", Amount: 9, Reason: "transfer " + tx}, "sys", true, "", "transfer " + tx},
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
		{"action_id":"local-1","action":"alice@testnet/echo","price":50},
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
	// Whether a row is discovered is never read off its shape: the local read
	// in getBrowse settles it.
	if cards[1].Discovered {
		t.Error("parseLookup must not decide discovery from the row")
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
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
		w.Write([]byte(withEvidence(`{"id":"proxy-local","action":"vendor@bravo/bought","name":"vendor/bought","kind":"remote_proxy","active":true,"visibility":"local","price":111}`,
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"P1","action":"vendor@bravo/slow","name":"vendor/slow","kind":"remote_proxy","active":true,"price":111,
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
		url.Values{"f_/q": {"hello"}, "t_/q": {"string"}, "has_fields": {"1"}})
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"results":[]},"tx_id":"T1"}`))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"TX9","action_id":"PX","action":"vendor@bravo/echo/index","status":"success",
		 "gross":133,"net":126,"fee":7,"owner":"alice@testnet","caller":"alice@testnet","target":"bravo@testnet",
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		if r.URL.Query().Get("ref") == "sys@testnet/lookup" {
			w.Write([]byte(`[{"id":"L","action":"sys@testnet/lookup","name":"lookup","kind":"native","active":true,"price":7}]`))
			return
		}
		w.Write([]byte(`[{"id":"A1","action":"alice@testnet/hello","name":"hello","kind":"wasm","active":true,"price":0,"description":"says hello"}]`))
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
	if !strings.Contains(body, "alice@testnet/hello") {
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"WEB","action":"sys@testnet/web","name":"web","kind":"http","active":true,"price":0,
		 "description":"Fetch a public web page","input_schema":{"type":"object","properties":{"url":{"type":"string","description":"the page"}},"required":["url"]}}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		switch r.URL.Query().Get("ref") {
		case "sys@testnet/llm/decide":
			fmt.Fprintf(w, `[{"id":"D","action":"sys@testnet/llm/decide","name":"decide","kind":"native","active":true,"price":%d}]`, decidePrice)
		case "sys@testnet/web":
			w.Write([]byte(`[{"id":"WEB","action":"sys@testnet/web","name":"web","kind":"http","active":true,"price":0,
			 "input_schema":{"type":"object","properties":{"url":{"type":"string","description":"the page"}},"required":["url"]}}]`))
		case "sys@testnet/lookup":
			w.Write([]byte(`[{"id":"L","action":"sys@testnet/lookup","name":"lookup","kind":"native","active":true,"price":0}]`))
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
		if in.Action == "sys@testnet/llm/decide" {
			lastDecide = string(b)
			fmt.Fprintf(w, `{"result":%s,"tx_id":"T1"}`, decision)
			return
		}
		w.Write([]byte(`{"result":{"results":[{"action_id":"WEB","action":"sys@testnet/web","description":"Fetch a public web page","price":0}]},"tx_id":"T0"}`))
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
	kernel := proposalKernel(t, 0, `{"action":"sys@testnet/web","args":{"url":"example.com"},"message":"this fetches a page"}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// The command is offered beside the results it acts on.
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	if !strings.Contains(page, `action="/browse/propose"`) || !strings.Contains(page, `name="ref" value="sys@testnet/web"`) {
		t.Fatalf("browse must offer the planner, carrying the refs on screen: %s", page)
	}
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{
		"q": {"fetch the front page of example.com"}, "ref": {"sys@testnet/web"},
	})
	defer resp.Body.Close()
	bb, _ := io.ReadAll(resp.Body)
	body := string(bb)
	// The candidate set sent is the one the person could see — not one assembled
	// in private, which would be a claim about what was offered that nobody
	// could check.
	if !strings.Contains(lastDecide, `"actions":["sys@testnet/web"]`) {
		t.Errorf("the planner must be given the visible candidates: %s", lastDecide)
	}
	if !strings.Contains(body, "Chosen for you") {
		t.Errorf("a proposal must say it is one: %s", body)
	}
	if !strings.Contains(body, `name="f_/url" rows="1">example.com`) {
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
	kernel := proposalKernel(t, 0, `{"action":"sys@testnet/web","args":{"url":"example.com"}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{"q": {"x"}, "ref": {"sys@testnet/web"}})
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
	kernel := proposalKernel(t, 9, `{"action":"sys@testnet/web","args":{}}`)
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
		"CHEAP": `{"id":"CHEAP","action":"alice@testnet/cheap","name":"cheap","kind":"wasm","active":true,"price":1}`,
		"LOVED": `{"id":"LOVED","action":"alice@testnet/loved","name":"loved","kind":"wasm","active":true,"price":50}`,
		"MEH":   `{"id":"MEH","action":"alice@testnet/meh","name":"meh","kind":"wasm","active":true,"price":10}`,
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
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
	// A row is found by its link; the order of the links is the order shown.
	order := func(body string) []string {
		out := []string{"/actions/CHEAP", "/actions/LOVED", "/actions/MEH"}
		sort.Slice(out, func(i, j int) bool { return strings.Index(body, out[i]) < strings.Index(body, out[j]) })
		return out
	}
	if got := order(getBody(t, cl, srv.URL+"/browse?sort=price")); got[0] != "/actions/CHEAP" || got[2] != "/actions/LOVED" {
		t.Errorf("price order = %v", got)
	}
	if got := order(getBody(t, cl, srv.URL+"/browse?sort=rating")); got[0] != "/actions/LOVED" {
		t.Errorf("rating order must put the best-rated first: %v", got)
	}
	// A row nobody has rated is not SCORED as a bad one. It sorts into the
	// unrated block after the rated rows, which says the true thing — it has not
	// been rated — rather than interleaving silence with measured opinion.
	rated := getBody(t, cl, srv.URL+"/browse?sort=rating")
	if strings.Index(rated, "/actions/CHEAP") < strings.Index(rated, "/actions/MEH") {
		t.Error("an unrated action belongs after the rated ones, not among them")
	}
	// An unknown sort shows the list, not an error.
	if body := getBody(t, cl, srv.URL+"/browse?sort=nonsense"); !strings.Contains(body, "/actions/CHEAP") {
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
		return fmt.Sprintf(`{"id":"A%d","action":"alice@testnet/a%02d","name":"a%02d","kind":"wasm","active":true,"price":0}`, i, i, i)
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
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
	best := fmt.Sprintf(`href="/actions/A%d"`, total-1)
	if !strings.Contains(body, best) {
		t.Errorf("the best-rated action must be on the first page of a rating sort: %s", best)
	}
}

// The planner's proposal must reach the raw-JSON input as well as the fields.
// That box is the ONLY input where an action's schema is unusable, and it is one
// toggle away everywhere else — so filling only the fields drops the proposal
// silently for one action and loses it on a switch for every other.
func TestProposalReachesTheRawInputToo(t *testing.T) {
	kernel := proposalKernel(t, 0, `{"action":"sys@testnet/web","args":{"url":"example.com","depth":2}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{"q": {"x"}, "ref": {"sys@testnet/web"}})
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
	kernel := proposalKernel(t, 0, `{"action":"sys@testnet/web","args":{},"message":"this fetches a page"}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{"q": {"x"}, "ref": {"sys@testnet/web"}})
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

// The entry screen names the kernel it signs in to, so a person types only the
// handle; the account the kernel is asked for is that handle's address here,
// composed once from /health (requirements.md §5). The session then holds the
// address, which is what the superuser gate compares.
func TestLoginSignsInByAddress(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	loginClient(t, srv.URL)
	if lastSignIn != "alice@testnet" {
		t.Errorf("authorize named %q, want alice@testnet", lastSignIn)
	}
	if body := getBody(t, loginClientAs(t, srv.URL, "sys"), srv.URL+"/browse"); !strings.Contains(body, `href="/admin"`) {
		t.Error("sys@testnet is the superuser")
	}
}

// The sessions page lists closed sessions too, which the kernel serves only
// when asked (?all=1); the wallet's open list does not ask (§6.5).
func TestSessionsPageAsksForClosedSessions(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	_ = getBody(t, cl, srv.URL+"/processes")
	if !strings.Contains(lastProcessesQuery, "all=1") {
		t.Errorf("sessions page query = %q, want all=1", lastProcessesQuery)
	}
	_ = getBody(t, cl, srv.URL+"/wallet")
	if strings.Contains(lastProcessesQuery, "all=1") {
		t.Errorf("wallet query = %q, want open sessions only", lastProcessesQuery)
	}
}

// Registering a payout address takes the account's password. A wrong one is
// the kernel's 401, which must read as the password being wrong and must NOT
// end the session the way a 401 elsewhere does (§6.5).
func TestRegisterAddressWrongPasswordKeepsTheSession(t *testing.T) {
	var sent string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","handle":"testnet","public_key":"PK","network":"test","network_fingerprint":"D9","decimals":6,"symbol":"USDC","blockchain_address":"0xKERNEL"}`))
	})
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=C"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":5000000,"locked":0}`))
	})
	mux.HandleFunc("PUT /v1/me/blockchain-address", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		if !strings.Contains(sent, `"current_password":"right-password"`) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":"unauthenticated","error":"invalid current password"}`))
			return
		}
		w.Write([]byte(`{"blockchain_address":"0xalice","attributed":1}`))
	})
	for _, path := range []string{"/v1/withdrawals", "/v1/processes", "/v1/transactions", "/v1/ledger", "/v1/tasks", "/v1/actions"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	}
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/wallet")
	if !strings.Contains(page, `name="current_password"`) {
		t.Fatal("the address form must ask for the password")
	}
	form := url.Values{"address": {"0xAlice"}, "signature": {"sig"}, "current_password": {"wrong"}}
	resp := postTo(t, cl, srv.URL+"/wallet/address", page, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/wallet" {
		t.Fatalf("wrong password = %d → %q, want back to the wallet", resp.StatusCode, resp.Header.Get("Location"))
	}
	after := getBody(t, cl, srv.URL+"/wallet")
	if !strings.Contains(after, "Current password is incorrect.") {
		t.Errorf("the wallet must say the password was wrong: %s", after)
	}
	if !strings.Contains(after, `name="current_password"`) {
		t.Error("a wrong password must not end the session")
	}
	for _, want := range []string{`"blockchain_address":"0xAlice"`, `"signature":"sig"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("request %s lacks %s", sent, want)
		}
	}

	form.Set("current_password", "right-password")
	resp = postTo(t, cl, srv.URL+"/wallet/address", after, form)
	resp.Body.Close()
	if ok := getBody(t, cl, srv.URL+"/wallet"); !strings.Contains(ok, "Address registered; 1 earlier payment(s) credited.") {
		t.Errorf("a right password registers: %s", ok)
	}
}

// A transfer to another kernel's user carries the state of this kernel's
// payment on its transaction; the receipt states it as served (§6.5).
func TestReceiptStatesATransferPayment(t *testing.T) {
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=C"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"TXP","action":"sys@testnet/transfer","status":"success","gross":0,"net":0,"fee":0,
		 "args":{"target":"bob@bravo","amount":2500000},"result":{"amount":2500000},"payment":"announced",
		 "owner":"alice@testnet","caller":"alice@testnet","target":"sys@testnet","started_at":"2026-10-02T11:15:59Z"}`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClientAs(t, srv.URL, "alice")

	body := getBody(t, cl, srv.URL+"/transactions/TXP")
	if !strings.Contains(body, `Payment to the recipient's kernel <span class="pill ">announced</span>`) {
		t.Errorf("the receipt must state the payment as served, in a neutral pill: %s", body)
	}
}

// homeKernel is a fake kernel for the dashboard and the type-ahead. It records
// every task-list query and counts action-list reads and runs, so a test can
// assert what each page asked the kernel for.
type homeKernel struct {
	*httptest.Server
	taskQueries []string
	actionLists int
	runs        int
	failLedger  bool
}

func newHomeKernel(t *testing.T) *homeKernel {
	k := &homeKernel{}
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"redirect":"?code=CODE"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":500,"locked":40}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		k.taskQueries = append(k.taskQueries, r.URL.RawQuery)
		// Seven waiting tasks for alice and one for bob (a superuser's view).
		var rows []string
		for i := 0; i < 7; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"T%d","required_caller":"alice@testnet","owner":"bob@testnet","action":"alice@testnet/hello","price":5,"status":"waiting","created_at":"2026-01-01T00:00:00Z"}`, i))
		}
		rows = append(rows, `{"id":"TBOB","required_caller":"bob@testnet","owner":"bob@testnet","action":"bob@testnet/x","price":5,"status":"waiting","created_at":"2026-01-01T00:00:00Z"}`)
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		var rows []string
		for i := 0; i < 7; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"P%d000000","status":"open","available":10,"locked":0}`, i))
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	})
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) {
		if k.failLedger {
			http.Error(w, `{"code":"internal","error":"boom"}`, http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`[{"amount":300,"reason":"","created_at":"2024-01-02T12:00:00Z","operator":"sys@testnet","from":"sys@testnet","to":"alice@testnet"}]`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		if r.URL.Query().Get("ref") != "" {
			w.Write([]byte(`[]`))
			return
		}
		k.actionLists++
		var rows []string
		for i := 0; i < 12; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"G%d","action":"alice@testnet/greet%d","name":"greet%d","title":"Greet %d","kind":"wasm","active":true,"price":0}`, i, i, i, i))
		}
		rows = append(rows, `{"id":"S1","action":"alice@testnet/summ","name":"summ","title":"Summarize a page","kind":"wasm","active":true,"price":0}`,
			`{"id":"S2","action":"alice@testnet/summ2","name":"summ2","title":"summarize a page","kind":"wasm","active":true,"price":0}`,
			`{"id":"","action":"tom@BRAVOKEY/tools/weather","name":"tools/weather","title":"Weather forecast","kind":"wasm","active":true,"price":3,"discovered":true,"remote_action_id":"R1"}`)
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		k.runs++
		w.Write([]byte(`{"result":{}}`))
	})
	k.Server = httptest.NewServer(mux)
	t.Cleanup(k.Close)
	return k
}

// Home is the dashboard: balances, the waiting tasks addressed to the viewer
// (at most five, asked for as waiting so no history is read), open sessions
// (at most five), and recent ledger activity, each linking to its full page.
func TestHomeIsTheDashboard(t *testing.T) {
	k := newHomeKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	k.taskQueries = nil
	resp, err := cl.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/" {
		t.Fatalf("/ must render the dashboard, got %d at %s", resp.StatusCode, resp.Request.URL.Path)
	}
	for _, want := range []string{"500 credits", "40 credits", `href="/inbox"`, `href="/processes"`, `href="/transactions"`, `href="/wallet"`, "from sys@testnet"} {
		if !strings.Contains(page, want) {
			t.Errorf("home must show %q", want)
		}
	}
	if n := strings.Count(page, `data-href="/inbox/T`); n != 5 {
		t.Errorf("home shows %d waiting tasks, want 5", n)
	}
	if strings.Contains(page, "/inbox/TBOB") {
		t.Error("home must show only tasks addressed to the viewer")
	}
	if n := strings.Count(page, `<a class="mono" href="/processes/P`); n != 5 {
		t.Errorf("home shows %d open sessions, want 5", n)
	}
	for _, q := range k.taskQueries {
		if !strings.Contains(q, "status=waiting") || strings.Contains(q, "all=1") {
			t.Errorf("home must ask for waiting tasks only, asked %q", q)
		}
	}
	if !strings.Contains(page, `aria-current="page"><svg`) || !strings.Contains(page, `href="/" title="Home" aria-current="page"`) {
		t.Error("the home nav item must be current on /")
	}
	// A secondary read failing leaves the rest of the page standing.
	k.failLedger = true
	if page := getBody(t, cl, srv.URL+"/"); !strings.Contains(page, "500 credits") || !strings.Contains(page, "No activity yet.") {
		t.Error("home must still render when the ledger read fails")
	}
}

// hintFixture is server/testdata/hints.json, the cases the server's hint
// rules and the browser's matcher (static/app.js) are both held to: keys
// (normalization), a catalog of title/key pairs, and queries with the titles
// the hints must offer, in order. The live check runs the same file through
// the browser's matcher.
type hintFixture struct {
	Keys []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"keys"`
	Catalog []catalogTitle `json:"catalog"`
	Cases   []struct {
		Query string   `json:"query"`
		Want  []string `json:"want"`
	} `json:"cases"`
}

func TestHintFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/hints.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx hintFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Keys {
		if got := hintKey(c.In); got != c.Out {
			t.Errorf("hintKey(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
	actions := make([]client.Action, len(fx.Catalog))
	for i, e := range fx.Catalog {
		if got := hintKey(e.T); got != e.K {
			t.Errorf("catalog key of %q = %q, fixture says %q", e.T, got, e.K)
		}
		actions[i] = client.Action{Title: e.T}
	}
	// With titles alone, the fallback's ranking is the hints' ranking; the
	// hints show the first twelve.
	for _, c := range fx.Cases {
		var got []string
		for _, r := range catalogAsResults(actions, c.Query) {
			if len(got) < 12 {
				got = append(got, r.Action.Title)
			}
		}
		if strings.Join(got, "|") != strings.Join(c.Want, "|") {
			t.Errorf("hints for %q = %q, want %q", c.Query, got, c.Want)
		}
	}
}

// The search fallback ranks a title starting with the query, then a title
// holding its words, then a name or description holding them, and keeps a
// discovered row as discovered.
func TestCatalogFallbackRanksAndKeepsDiscovered(t *testing.T) {
	actions := []client.Action{
		{ID: "D", Title: "Forecast tool", Description: "Gives the weather anywhere."},
		{ID: "B", Title: "US weather alerts"},
		{ID: "A", Title: "Weather forecast"},
		{Title: "Weather in Norway", Discovered: true, Action: "sys@KEY/weather/norway"},
		{ID: "X", Title: "Unrelated", Description: "Nothing here."},
	}
	got := catalogAsResults(actions, "WEATHER")
	var order []string
	for _, r := range got {
		order = append(order, r.Action.Title)
	}
	if strings.Join(order, "|") != "Weather forecast|Weather in Norway|US weather alerts|Forecast tool" {
		t.Errorf("fallback order = %q", order)
	}
	if !got[1].Discovered || got[0].Discovered {
		t.Error("the fallback must keep the catalog's discovered mark")
	}
}

// pastEnd answers the catalog read beyond its last page. The client reads
// GET /v1/actions by offset until an empty page, and every fixture here fits
// in the first page, so any later offset is past the end.
func pastEnd(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("offset") == "" {
		return false
	}
	w.Write([]byte(`[]`))
	return true
}

// The search box's hints come from one catalog file, as a static site's search
// index does: every title the viewer may see — local and discovered — once,
// with its normalized key, ordered by key. It is read in one catalog read,
// never runs anything, and is never stored by the browser (it holds the
// account's private titles).
func TestSearchCatalogServesEveryTitleOnce(t *testing.T) {
	k := newHomeKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	k.actionLists, k.runs = 0, 0
	resp, err := cl.Get(srv.URL + "/search/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var got []catalogTitle
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if k.actionLists != 1 || k.runs != 0 {
		t.Errorf("catalog made %d list reads and %d runs, want 1 and 0", k.actionLists, k.runs)
	}
	keys := map[string]int{}
	for i, e := range got {
		keys[e.K]++
		if i > 0 && got[i-1].K > e.K {
			t.Errorf("catalog not ordered by key at %q", e.K)
		}
		if e.K != hintKey(e.T) {
			t.Errorf("key of %q = %q", e.T, e.K)
		}
	}
	if keys["weather forecast"] != 1 {
		t.Error("a discovered action's title belongs in the catalog")
	}
	if keys["summarize a page"] != 1 || keys["greet 0"] != 1 || len(got) != 14 {
		t.Errorf("each title once, all 14 of them: %+v", got)
	}
}

// The browse catalog renders a discovered row in its discovered form — linked
// to resolve on open, never to a local action page it does not have — and
// the workshop, which manages this kernel's actions, leaves it out.
func TestCatalogDiscoveredRows(t *testing.T) {
	k := newHomeKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/browse?sort=price")
	if !strings.Contains(page, `href="/discovered?ref=tom%40BRAVOKEY%2ftools%2fweather"`) {
		t.Error("a discovered catalog row must link to resolve-on-open")
	}
	if strings.Contains(page, `href="/actions/"`) {
		t.Error("no catalog row may link to an action page without an id")
	}
	if !strings.Contains(page, `title="Discovered`) {
		t.Error("a discovered catalog row carries the discovered chip")
	}
	if strings.Contains(getBody(t, cl, srv.URL+"/workshop"), "Weather forecast") {
		t.Error("the workshop manages this kernel's actions; a discovered row is not one")
	}
}

// The args are rebuilt from the form's pointers and hints alone: a nested
// object, an array whose blank row is dropped and whose indices compact, a
// null box that nulls its subtree, an escaped key, and an untouched optional
// map, which is omitted like any blank field (§6.2).
func TestBuildArgsRebuildsTheTree(t *testing.T) {
	form := url.Values{
		"has_fields":         {"1"},
		"t_/messages":        {"array"},
		"t_/messages/0":      {"object"},
		"f_/messages/0/role": {"user"}, "t_/messages/0/role": {"string"},
		"f_/messages/0/content": {"hi"}, "t_/messages/0/content": {"string"},
		"t_/messages/1":      {"object"},
		"f_/messages/1/role": {""}, "t_/messages/1/role": {"string"},
		"f_/messages/1/content": {""}, "t_/messages/1/content": {"string"},
		"t_/messages/2":      {"object"},
		"f_/messages/2/role": {"system"}, "t_/messages/2/role": {"string"},
		"f_/messages/2/content": {"later"}, "t_/messages/2/content": {"string"},
		"t_/options":       {"object"},
		"f_/options/depth": {"3"}, "t_/options/depth": {"integer"},
		"t_/options/tags":   {"array"},
		"f_/options/tags/0": {""}, "t_/options/tags/0": {"string"},
		"t_/tool":      {"object"},
		"f_/tool/name": {"ignored"}, "t_/tool/name": {"string"}, "n_/tool": {"1"},
		"f_/meta": {""}, "t_/meta": {"object"},
		"f_/a~1b": {"slash"}, "t_/a~1b": {"string"},
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw, err := buildArgs(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a/b":"slash","messages":[{"content":"hi","role":"user"},{"content":"later","role":"system"}],"options":{"depth":3},"tool":null}`
	if string(raw) != want {
		t.Errorf("args = %s\nwant %s", raw, want)
	}
}

// A result downloads as a file from its transaction, through the kernel's own
// read, so who may have it is the kernel's decision (§6.5).
func TestResultDownloadIsAnAttachment(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	resp, err := cl.Get(srv.URL + "/transactions/T1/result.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="result-T1.json"` {
		t.Errorf("disposition = %q", cd)
	}
	if !strings.Contains(string(body), `"items":["a","b"]`) {
		t.Errorf("body = %s", body)
	}
	// The receipt page renders both values as trees, escaped, and offers the download.
	page := getBody(t, cl, srv.URL+"/transactions/T1")
	for _, want := range []string{`<label>q</label>`, `<div class="box">&lt;b&gt;x&lt;/b&gt;</div>`, `<legend>items</legend>`, `<div class="rows">`, `<div class="box muted">null</div>`, `href="/transactions/T1/result.json" download`, "line1\nline2", `data-json-switch`} {
		if !strings.Contains(page, want) {
			t.Errorf("receipt page missing %q", want)
		}
	}
}
