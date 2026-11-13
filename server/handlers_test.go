package server

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/wallet"
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
	mux.HandleFunc("GET /v1/withdrawals", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
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
	// The kernel verifies the signature over the challenge in the recovery
	// domain of its network (fingerprint D1), as v0.15.7 does, against the key
	// of the phrase TestRecoverResetsPassword uses.
	mux.HandleFunc("POST /v1/auth/recover/complete", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Nonce, Signature string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		pubB64, _ := recoveryPubKey("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
		pub, _ := base64.RawURLEncoding.DecodeString(pubB64)
		sig, _ := base64.RawURLEncoding.DecodeString(in.Signature)
		if !ed25519.Verify(pub, []byte(`juice/v1/D1/recovery`+"\n"+`{"recovery_challenge":"`+in.Nonce+`"}`), sig) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"unauthorized","error":"recovery signature is invalid"}`))
			return
		}
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
			{"id":"S1","required_caller":"alice@testnet","owner":"bob@testnet","requester":"bob@testnet","action":"alice@testnet/hello","price":5,"status":"waiting","created_by":"sys@testnet/make","created_at":"2026-01-01T00:00:00Z"},
			{"id":"S2","required_caller":"alice@testnet","owner":"bob@testnet","requester":"bob@testnet","action":"alice@testnet/hello","price":3,"status":"waiting","created_by":"sys@testnet/make","created_at":"2026-01-02T00:00:00Z"}
		]`))
	})
	mux.HandleFunc("GET /v1/me/requesters", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"` + r.PathValue("id") + `","required_caller":"alice@testnet","owner":"bob@testnet","requester":"bob@testnet","action":"alice@testnet/hello","price":5,"partial_args":{},"status":"waiting","created_at":"2026-01-01T00:00:00Z"}`))
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
		// The merged roster (verified live 2026-10-08, v0.15.11): counterparties
		// (traded) and discovered kernels in one list keyed by public key,
		// this kernel excluded. A discovered row carries a catalog and a last_seen
		// but no credit, and no PETNAME until one is bound — only its self-asserted
		// nickname, which "twin" repeats across two keys, legally, so they must
		// stay two rows. A suspended counterparty appears only under ?all=1.
		rows := `{"petname":"peerb","nickname":"peerb","public_key":"KEYB","traded":true,"actions":4,"last_seen":"2026-08-04T09:54:01Z","last_contact_failed_at":"2026-08-04T10:00:00Z"},
		  {"petname":"debtor","public_key":"KEYD","traded":true,"actions":0},
		  {"nickname":"twin","public_key":"KEYT1","traded":false,"actions":13,"last_seen":"2026-08-06T00:04:34Z"},
		  {"petname":"twin","nickname":"twin","public_key":"KEYT2","traded":false,"actions":7,"last_seen":"2026-08-06T00:04:35Z"}`
		if r.URL.Query().Get("all") == "1" {
			rows += `,{"petname":"frozen","public_key":"KEYS","traded":true,"suspended_at":"2026-08-01T00:00:00Z"}`
		}
		// A bare array: the kernel envelopes no list (API.md R6; verified live
		// 2026-08-05 — an envelope here would silently render zero peers).
		w.Write([]byte(`[` + rows + `]`))
	})
	mux.HandleFunc("GET /v1/admin/peers/{key}", func(w http.ResponseWriter, r *http.Request) {
		// The kernel row: every known kernel has one, dated by first_seen and
		// carrying its suspension; a never-learned key has none (verified live
		// 2026-10-08, v0.15.11).
		key := r.PathValue("key")
		if key == "KEYNONE" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"not_found","error":"kernel not found"}`))
			return
		}
		suspended := "null"
		if key == "KEYS" {
			suspended = `"2026-08-01T00:00:00Z"`
		}
		w.Write([]byte(`{"petname":"peerb","nickname":"peerb","public_key":"` + key + `","about":"","blockchain_address":"",
		 "first_seen":"2026-09-14T01:13:39Z","last_seen":"2026-08-04T09:54:01Z","suspended_at":` + suspended + `}`))
	})
	mux.HandleFunc("GET /v1/admin/peers/{key}/inspect", func(w http.ResponseWriter, r *http.Request) {
		// online/source are TOP-LEVEL in the real response; reachability holds only
		// the transport diagnostics. An action is {name, description, price}, and
		// evidence carries both legs: the subject's own rows (issuer == public_key)
		// and a third party's, whose corroborated_uses is the two-kernel link. The
		// inspection states no account and no suspension (v0.15.11): the kernel
		// row does. KEYT* are discovered (source live); KEYNONE is a stranger key
		// the kernel has never learned.
		key := r.PathValue("key")
		if key == "KEYNONE" {
			w.Write([]byte(`{"handle":"","public_key":"` + key + `","actions":[],"evidence":[],
			 "online":false,"source":"none","reachability":{"path":"unreachable","error":"routing: not found"}}`))
			return
		}
		// A discovered kernel is unbound: it has only its claim, "twin", which
		// KEYT2 already holds here as a petname (roster above).
		names := `"petname":"kernelc","nickname":"selfname"`
		if strings.HasPrefix(key, "KEYT") {
			names = `"petname":"","nickname":"twin"`
		}
		w.Write([]byte(`{` + names + `,"public_key":"` + key + `","about":"a peer kernel",
		 "actions":[{"action_id":"a2","name":"synth@kernelc/make","description":"synth","price":20,"indicative":true}],
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
	if resp2.StatusCode != 200 { // followed redirect to the landing
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
	// Each auth panel's action is its form's primary (grammar §5.1), never a
	// money colour; a "Sign in" and a "Create account" submit exist.
	if !strings.Contains(def, `class="btn-primary" type="submit">Sign in<`) {
		t.Error("sign-in panel must have a primary Sign in action")
	}
	if !strings.Contains(def, `class="btn-primary" type="submit">Create account<`) {
		t.Error("signup panel must have a primary Create account action")
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
	if !strings.Contains(body, `data-copy="`) || !strings.Contains(body, `aria-label="Copy phrase"`) || !strings.Contains(body, `name="understand"`) {
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
func TestDiscoverNarrowsToAnOwner(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL) // signed in as alice

	// Your own name: A3 is alice's inactive/private draft — it must appear (and
	// self-labels via the card's inactive/lock chips).
	own := getBody(t, cl, srv.URL+"/browse?owner=alice@testnet")
	if !strings.Contains(own, `<div class="subhead"><h2 class="mono">alice@testnet</h2><a class="btn" href="/browse">All actions</a></div>`) {
		t.Error("an owner's actions are headed by the address, with the way back to every owner")
	}
	if !strings.Contains(own, `class="action-row`) || !strings.Contains(own, "draft") {
		t.Error("your own name must list your private/inactive actions")
	}
	// The sort switch keeps the owner; the first page of a new order has no offset.
	if !strings.Contains(own, `href="/browse?owner=alice%40testnet&amp;sort=rating"`) {
		t.Error("sorting must keep the owner")
	}

	// Someone else's name: caller-visible active set only — no private/inactive.
	if strings.Contains(getBody(t, cl, srv.URL+"/browse?owner=bob@testnet"), "draft") {
		t.Error("another owner's actions must not surface inactive/private ones")
	}
	// Every owner: no heading.
	if strings.Contains(getBody(t, cl, srv.URL+"/browse"), `<h2 class="mono">`) {
		t.Error("the whole catalog names no owner")
	}
	if resp, err := cl.Get(srv.URL + "/users/alice@testnet"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Error("an owner has no page of its own")
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
	if !strings.Contains(body, `href="/browse?owner=alice%40testnet"`) {
		t.Error("action owner must link to its actions on Discover")
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
	// Action ref: owner links to its actions on Discover.
	if !strings.Contains(body, `href="/browse?owner=alice%40testnet"`) {
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
	if strings.Contains(body, `data-modal="form-fund"`) {
		t.Error("a chain the fingerprint does not prove is never asked to send")
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

// On a proven chain the wallet deposits and withdraws, two symmetric openers
// whose reviews name the amount on the money button; the transfer is built at send time from
// a fresh read of the registered address, and a withdrawal carries the id its
// review minted (requirements §6.5).
func TestWalletFundsAndWithdrawsThroughReviews(t *testing.T) {
	registered, sent := "0xalice", ""
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","handle":"tango","public_key":"PK","network":"anvil","network_fingerprint":"7d16b1469f57f12eb983417f64de442bdb7b06909e26a65daa8bcd311bb80d9b","decimals":6,"symbol":"USDT0","token":"0x5fbdb2315678afecb367f032d93f642f64180aa3","blockchain_address":"0xvault"}`))
	})
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"redirect":"?code=C"}`)) })
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":"U1","address":"alice@tango","available":5000000,"locked":0,"blockchain_address":%q}`, registered)
	})
	mux.HandleFunc("POST /v1/withdrawals", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		w.Write([]byte(`{"id":"x","status":"submitted"}`))
	})
	for _, path := range []string{"/v1/withdrawals", "/v1/processes", "/v1/ledger", "/v1/tasks", "/v1/actions"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	}
	kernel := httptest.NewServer(mux)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	page := getBody(t, cl, srv.URL+"/wallet")
	for _, want := range []string{"<h2>Deposits and withdrawals</h2>", "<dt>Your crypto address</dt>", "<dt>Juice deposit address</dt>",
		"USDT0 · ", `data-wallet-register>Change address</a>`,
		`<svg class="ico ico-inline" aria-hidden="true"><use href="/static/icons.svg#alert"/></svg> Only send money from your crypto wallet, never from an exchange or any other wallet.`,
		`<a class="btn" href="/wallet?form=fund" data-modal="form-fund" data-wallet-js hidden>Deposit</a>`,
		`<a class="btn" href="/wallet?form=withdraw" data-modal="form-withdraw">Withdraw</a>`,
		`src="/wallet/wallet.mjs"`, "user: U1\naddress: \""} {
		if !strings.Contains(page, want) {
			t.Errorf("wallet lacks %q", want)
		}
	}
	if strings.Contains(page, "btn-money") {
		t.Error("an opener moves no money, so the page carries no accent until a review")
	}
	review := func(path, amount string) string {
		resp := postTo(t, cl, srv.URL+path, page, url.Values{"amount": {amount}})
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if body := review("/wallet/fund/review", "1.5"); !strings.Contains(body, `<button class="btn-money" type="button" data-wallet-send data-amount="1.5">Deposit (1.50 USDT0)</button>`) {
		t.Errorf("fund review must name the amount on the money button: %s", body)
	}
	if body := review("/wallet/fund", "1.5"); body != `{"from":"0xalice","to":"0x5fbdb2315678afecb367f032d93f642f64180aa3","data":"`+wallet.TransferData("0xvault", 1500000)+`","chainId":"0x7a69"}`+"\n" {
		t.Errorf("fund tx = %s", body)
	}
	registered = ""
	if resp := postTo(t, cl, srv.URL+"/wallet/fund", page, url.Values{"amount": {"1.5"}}); resp.StatusCode != http.StatusConflict {
		t.Errorf("a registration changed since the review must refuse the send, got %d", resp.StatusCode)
	}
	registered = "0xalice"

	body := review("/wallet/withdraw/review", "2")
	m := regexp.MustCompile(`name="id" value="([0-9a-f-]{36})"`).FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, `<button class="btn-money" type="submit">Withdraw (2.00 USDT0)</button>`) {
		t.Fatalf("withdraw review must mint an id and name the amount: %s", body)
	}
	resp := postTo(t, cl, srv.URL+"/wallet/withdraw", page, url.Values{"id": {m[1]}, "amount": {"2"}})
	resp.Body.Close()
	if sent != `{"amount":2000000,"id":"`+m[1]+`"}` || !strings.Contains(getBody(t, cl, srv.URL+"/wallet"), "Withdrawal submitted.") {
		t.Errorf("withdraw sent %s", sent)
	}
	if body := getBody(t, cl, srv.URL+"/wallet?form=address&address=0xAbC"); !strings.Contains(body, "address: 0xAbC</div>") || !strings.Contains(body, `value="0xAbC"`) {
		t.Errorf("without JS the typed address gets its message: %s", body)
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
		t.Error("/settings must render the grant tree (folders per directory)")
	}
	// The folder summary offers account-level disconnect (account=provider_key)…
	if !strings.Contains(body, `action="/grants/disconnect"`) || !strings.Contains(body, `value="oauth:G"`) {
		t.Error("/settings must offer an account-level disconnect (account=provider_key)")
	}
	// …and each contained grant offers a per-action disconnect (action=selector).
	if !strings.Contains(body, `name="action" value="sys@testnet/gmail-send"`) {
		t.Error("/settings grant tree must offer per-action disconnect")
	}
}

// Disconnecting a grant held on another kernel that does not answer removes it
// here and says to disconnect again, rather than showing an error page.
func TestGrantDisconnectElsewhereUnreachable(t *testing.T) {
	base := authKernel()
	defer base.Close()
	var mu sync.Mutex
	var gotQuery string
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/grants" {
			mu.Lock()
			gotQuery = r.URL.RawQuery
			mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"code":"peer_unreachable","error":"peer unreachable"}`))
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer wrap.Close()
	srv := httptest.NewServer(newTestHandler(t, wrap.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	tok := csrfTokenFrom(t, getBody(t, cl, srv.URL+"/settings"))
	resp, err := cl.PostForm(srv.URL+"/grants/disconnect", url.Values{"selector": {"chat@alpha/room"}, "gorilla.csrf.Token": {tok}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	mu.Lock()
	q := gotQuery
	mu.Unlock()
	if q != "selector=chat%40alpha%2Froom" {
		t.Errorf("disconnect by selector: %q", q)
	}
	if !strings.Contains(string(b), "Disconnected chat@alpha/room here. Its kernel did not answer; disconnect again once it does.") {
		t.Errorf("the flash must say what happened: %s", b)
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
		base.Config.Handler.ServeHTTP(w, r) // delegate login and landing reads to authKernel
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
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/tick", nil) // requireAuth-gated
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

// TestTickLogsOutWhenKernelDown: a page's heartbeat whose kernel has gone away
// is logged out cleanly (HX-Redirect to /login).
func TestTickLogsOutWhenKernelDown(t *testing.T) {
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
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/tick", nil)
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
	if !strings.Contains(getBody(t, cl, srv.URL+"/browse"), `width="24" height="26"><span>testnet <span class="world">play</span>`) {
		t.Error("the brand must show the kernel handle and its world")
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
	if !strings.Contains(browse, `href="/browse?owner=alice%40testnet"`) {
		t.Error("browse card owner must link to its actions on Discover")
	}
	if strings.Contains(browse, `title="WebAssembly action">W`) {
		t.Error("browse card must NOT show an always-on kind chip (exceptional-only)")
	}
	if !strings.Contains(browse, `class="media tile"`) {
		t.Error("browse row must lead with a tile")
	}

	// One row everywhere: the inactive draft says so to every viewer, unfaded;
	// a live action names who may call it only to a viewer who may manage it
	// (the fixture serves no visibility: private, the kernel's default), in
	// the live green. alice's own row reads the same on her Workshop and on
	// Discover; sys's actions on Discover carry no state for her.
	body := getBody(t, cl, srv.URL+"/workshop")
	own := getBody(t, cl, srv.URL+"/browse?owner=alice@testnet")
	for page, b := range map[string]string{"workshop": body, "discover": own} {
		if !strings.Contains(b, `<span class="pill muted">inactive</span>`) || !strings.Contains(b, `<span class="pill status-credited">private</span>`) {
			t.Errorf("%s: an owner's row states its state in one pill: %s", page, b)
		}
		for _, gone := range []string{`/static/icons.svg#lock`, `title="Inactive — not callable"`, `title="Local — `, `action-row rowable inactive`} {
			if strings.Contains(b, gone) {
				t.Errorf("%s: a row wears no %q and is not faded", page, gone)
			}
		}
	}
	if row := func(b, id string) string {
		i := strings.Index(b, `data-href="/actions/`+id+`"`)
		return b[i : i+strings.Index(b[i:], "</article>")]
	}; row(body, "A1") != row(own, "A1") {
		t.Errorf("the same action is drawn the same on the Workshop and Discover:\n%s\n%s", row(body, "A1"), row(own, "A1"))
	}
	if strings.Contains(getBody(t, cl, srv.URL+"/browse?owner=sys@testnet"), "status-credited") {
		t.Error("another owner's live action carries no state for this viewer")
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

	// New action is the section's command, beside its heading: one button with
	// its icon, opening the two ways to make one (each a real ?form= link).
	if !regexp.MustCompile(`(?s)<div class="subhead">\s*<h2>Your actions</h2>\s*<button class="btn" data-menu="menu-new-action"[^>]*><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#plus"/></svg><span>New action</span></button>`).MatchString(body) {
		t.Errorf("New action stands beside the heading: %s", body)
	}
	for _, form := range []string{"action-create", "action-import"} {
		if !strings.Contains(body, "?form="+form) {
			t.Errorf("New action menu missing the %q method", form)
		}
	}
	// The filter row is filters only; there is no unimport (kernel §D21).
	if strings.Contains(body, "tabs-action") || strings.Contains(body, "unimport") {
		t.Error("the filter row holds no command, and the kernel serves no unimport")
	}
	if !strings.Contains(body, `class="tab active" href="/workshop" aria-current="page">all`) || !strings.Contains(body, `href="/workshop?filter=active"`) {
		t.Error("the filters are all, active and inactive, all current by default")
	}
	// A row holds no controls: its page manages it.
	for _, gone := range []string{`id="row-`, `action="/actions/disable"`, `action="/actions/enable"`, `action="/actions/visibility"`, `action="/actions/delete"`, `<table`} {
		if strings.Contains(body, gone) {
			t.Errorf("the Workshop carries %q", gone)
		}
	}
}

// Everyone's Workshop is their own actions, the superuser's too: there is no
// second, everyone's Workshop. The superuser finds another user's action on
// Discover, and manages it on its page as its owner does.
func TestOneWorkshopTheSuperuserManagesOnThePage(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	// The fixture's /v1/me answers alice@testnet for every session; signing in
	// as sys makes the session the superuser's. sys@testnet/gmail-send (AG) is
	// not the viewer's.
	sys := loginClientAs(t, srv.URL, "sys")
	for _, u := range []string{"/workshop", "/workshop?scope=all"} {
		body := getBody(t, sys, srv.URL+u)
		if strings.Contains(body, ">everyone<") || strings.Contains(body, `href="/actions/AG"`) || !strings.Contains(body, `href="/actions/A1"`) {
			t.Errorf("%s: the superuser's Workshop is the viewer's own actions, with no scope switch", u)
		}
	}
	page := getBody(t, sys, srv.URL+"/actions/AG") // not the viewer's
	for _, want := range []string{"<h2>Manage</h2>", `action="/actions/disable"`, `action="/actions/visibility"`, `action="/actions/delete"`, `?form=action-edit`} {
		if !strings.Contains(page, want) {
			t.Errorf("sys on another user's action lacks %q", want)
		}
	}
	// Manage stands first, under the header: what its owner came for is not
	// below the run form, the ratings and the evidence. The page states who
	// may call it beside the title, as the action's row does for this viewer.
	if m, r := strings.Index(page, "<h2>Manage</h2>"), strings.Index(page, "<h2>Run</h2>"); m < 0 || r < m {
		t.Errorf("Manage must precede Run: manage %d, run %d", m, r)
	}
	if !strings.Contains(page, `<span class="pill status-credited">private</span>`) {
		t.Errorf("the page states who may call the action to whoever may manage it: %s", page)
	}
	resp := postTo(t, sys, srv.URL+"/actions/disable", page, url.Values{"target": {"AG"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/actions/AG" {
		t.Errorf("disable = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// An ordinary user on another's action has nothing to manage.
	if strings.Contains(getBody(t, loginClient(t, srv.URL), srv.URL+"/actions/AG"), "<h2>Manage</h2>") {
		t.Error("an ordinary user manages only their own actions")
	}
}

// The Workshop lists what needs attention first — the inactive — then the most
// run, each row with its counts, which come with its detail read.
func TestWorkshopOrdersInactiveThenRuns(t *testing.T) {
	rows := map[string]string{
		"few":   `{"id":"few","action":"alice@testnet/few","title":"Few","kind":"http","active":true,"visibility":"public","price":0}`,
		"many":  `{"id":"many","action":"alice@testnet/many","title":"Many","kind":"http","active":true,"visibility":"local","price":0}`,
		"draft": `{"id":"draft","action":"alice@testnet/draft","title":"Draft","kind":"http","active":false,"visibility":"private","price":0}`,
	}
	uses := map[string]int{"few": 2, "many": 20, "draft": 0}
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"redirect":"?code=CODE"}`)) })
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":0,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		w.Write([]byte("[" + rows["few"] + "," + rows["many"] + "," + rows["draft"] + "]"))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		w.Write([]byte(withEvidence(rows[id], fmt.Sprintf(`{"uses":%d,"successes":%d}`, uses[id], uses[id]))))
	})
	k := httptest.NewServer(mux)
	defer k.Close()
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	body := getBody(t, loginClient(t, srv.URL), srv.URL+"/workshop")
	d, m, f := strings.Index(body, `data-href="/actions/draft"`), strings.Index(body, `data-href="/actions/many"`), strings.Index(body, `data-href="/actions/few"`)
	if d < 0 || m < d || f < m {
		t.Errorf("order must be inactive, then most run: draft %d, many %d, few %d", d, m, f)
	}
	if !strings.Contains(body, `title="100% success · 20 runs"`) || !strings.Contains(body, `<span class="pill status-credited">local</span>`) {
		t.Errorf("a row shows its counts and who may call it: %s", body)
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

// A task is drawn as an action run (§6.3): its head is what runs, then who
// asked, via what and when; then the one form card, whose result follows it
// below and outside any other frame.
func TestInboxTaskIsAnActionRun(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/inbox")

	head := regexp.MustCompile(`(?s)<h3 class="flush"><span class="ref mono"><span class="owner"><a href="/browse\?owner=alice%40testnet">alice@testnet</a></span>/hello</span></h3>\s*` +
		`<p class="muted small flush">asked by <span class="mono">bob@testnet</span> · via <span class="ref mono"><span class="owner"><a href="/browse\?owner=sys%40testnet">sys@testnet</a></span>/make</span> · \d+ days? ago</p>\s*` +
		`<form class="card stack" method="post" action="/inbox/S1/complete"`)
	if !head.MatchString(body) {
		t.Errorf("a task heads with what runs, then who asked, via what and when, then its form card: %s", body)
	}
	if strings.Contains(body, "task-card") || strings.Contains(body, " needs ") {
		t.Error("no frame wraps a task, and no sentence of addresses heads it")
	}
	// The result follows the form card directly (after the decline form), as on
	// an action page: never inside the task's frame.
	if !regexp.MustCompile(`(?s)</form>\s*<form id="decline-S1"[^>]*>.*?</form>\s*<div id="[^"]+"></div>\s*</div>`).MatchString(body) {
		t.Error("the result sits below the form card")
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

// Workshop adds up no money: only a read of every transaction could, so it reads
// none. Every page that leads with money draws it through the one figures component.
func TestWorkshopAddsUpNoMoney(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	lower := strings.ToLower(getBody(t, cl, srv.URL+"/workshop"))
	for _, gone := range []string{"this month", "all time", "all-time", ">earned<", "last failure"} {
		if strings.Contains(lower, gone) {
			t.Errorf("workshop shows %q", gone)
		}
	}
	// What is parked is the wallet's: a buyer's figure is no seller's headline.
	if strings.Contains(lower, "parked") || strings.Contains(lower, `class="stat-row"`) {
		t.Error("the workshop shows no money figure")
	}
	if b := getBody(t, cl, srv.URL+"/wallet"); !strings.Contains(b, `<div class="stat-row">`) || strings.Contains(b, `class="balance`) {
		t.Error("the wallet renders its money through the figures component")
	}
}

// bigKernel holds a thousand rows of every long list, answers as the kernel does,
// and records each list read.
func bigKernel(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	reads := []string{}
	serve := func(mux *http.ServeMux, path, row string) {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			reads = append(reads, path+"?"+r.URL.RawQuery)
			mu.Unlock()
			q := r.URL.Query()
			limit, offset := 50, 0
			if v, err := strconv.Atoi(q.Get("limit")); err == nil {
				limit = min(v, 200)
			}
			if v, err := strconv.Atoi(q.Get("offset")); err == nil {
				offset = v
			}
			rows := []string{}
			if !(path == "/v1/processes" && q.Get("all") == "") && !(path == "/v1/tasks" && q.Get("status") == "waiting") {
				for i := offset; i < min(offset+limit, 1000); i++ {
					rows = append(rows, fmt.Sprintf(row, i))
				}
			}
			w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
		})
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
		w.Write([]byte(`{"id":"U1","address":"alice@testnet","available":5000000,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/me/requesters", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/processes/P1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"P1","status":"closed","owner":"alice@testnet"}`))
	})
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	serve(mux, "/v1/transactions", `{"id":"T%d","status":"success","owner":"alice@testnet","caller":"alice@testnet","target":"bob@testnet","action":"bob@testnet/a"}`)
	serve(mux, "/v1/tasks", `{"id":"K%d","status":"done","owner":"alice@testnet","required_caller":"bob@testnet","action":"bob@testnet/a"}`)
	serve(mux, "/v1/processes", `{"id":"P%d","status":"closed","owner":"alice@testnet"}`)
	serve(mux, "/v1/withdrawals", `{"id":"W%d","kind":"payout","amount":1,"status":"confirmed","created_at":"2026-09-09T09:17:26Z"}`)
	k := httptest.NewServer(mux)
	t.Cleanup(k.Close)
	return k, &reads
}

// A page reads one page from the kernel, however long the list: it asks for one row
// more than it shows, at its own offset, and never walks the list. Its pager follows.
func TestListPagesReadOnePage(t *testing.T) {
	k, reads := bigKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	for _, c := range []struct{ page, list, query, row string }{
		{"/transactions", "/v1/transactions", "limit=21", "T0"},
		{"/processes/P1", "/v1/transactions", "limit=21&process_id=P1", "T0"},
		{"/inbox?tab=sent", "/v1/tasks", "all=1&limit=21", "K0"},
		{"/processes", "/v1/processes", "all=1&limit=21", "P0"},
		{"/wallet", "/v1/withdrawals", "limit=21", "W0"},
	} {
		*reads = (*reads)[:0]
		body := getBody(t, cl, srv.URL+c.page)
		var got []string
		for _, r := range *reads {
			if q, ok := strings.CutPrefix(r, c.list+"?"); ok && q != "limit=200&status=waiting" && q != "limit=200" {
				got = append(got, q)
			}
		}
		if len(got) != 1 || got[0] != c.query {
			t.Errorf("%s read %v, want only %s", c.page, got, c.query)
		}
		if !strings.Contains(body, c.row) {
			t.Errorf("%s lacks its first row", c.page)
		}
		if !strings.Contains(body, `rel="next"`) {
			t.Errorf("%s has no next page", c.page)
		}
	}
	// A later page asks at its offset; the last page offers no next.
	*reads = (*reads)[:0]
	body := getBody(t, cl, srv.URL+"/transactions?offset=980")
	if !strings.Contains(strings.Join(*reads, " "), "limit=21&offset=980") || strings.Contains(body, `rel="next"`) || !strings.Contains(body, `rel="prev"`) {
		t.Errorf("the last page: %v", *reads)
	}
	// Past the end there is no row, and still a way back.
	if body := getBody(t, cl, srv.URL+"/transactions?offset=5000"); !strings.Contains(body, `rel="prev"`) || strings.Contains(body, `rel="next"`) {
		t.Error("past the end, a page offers the way back")
	}
	// Workshop reads no transaction at all.
	*reads = (*reads)[:0]
	getBody(t, cl, srv.URL+"/workshop")
	if strings.Contains(strings.Join(*reads, " "), "/v1/transactions") {
		t.Errorf("workshop read %v", *reads)
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
// endpoints the action page and the post-login landing need.
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

// TestDiscoverHasItsOwnSearch: with the pill gone, the Discover page carries
// its query box as its content, a GET to itself, with the launcher's list of
// suggestions under it in place of the browser's own; no chrome holds a
// second one. The list's rows carry no caption: each kind is marked by itself.
func TestDiscoverHasItsOwnSearch(t *testing.T) {
	b, err := tmplFS.ReadFile("templates/browse.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `method="get" action="/browse" role="search"`) || !strings.Contains(string(b), `type="search" name="q"`) {
		t.Error("Discover must carry its own query box")
	}
	if !strings.Contains(string(b), `autocomplete="off"`) || !strings.Contains(string(b), `<div class="launcher" hidden>`) {
		t.Error("Discover's box must carry the launcher's list, not the browser's")
	}
	if js, _ := staticFS.ReadFile("static/app.js"); strings.Contains(string(js), "menu-caption") {
		t.Error("the launcher's rows carry no caption")
	}
	layout, _ := tmplFS.ReadFile("templates/layout.html")
	if strings.Contains(string(layout), `role="search"`) {
		t.Error("the chrome holds no search box: the composer's launcher is the way in")
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
	// A transfer task: its value was reserved from the requester at creation.
	"SP": `{"id":"SP","required_caller":"bob@testnet","owner":"alice@testnet","requester":"alice@testnet","action":"sys@testnet/transfer",
	  "price":0,"status":"waiting","created_by":"sys@testnet/message","created_at":"2026-08-01T00:00:00Z",
	  "partial_args":{"target":"alice@testnet","amount":300},"allowed_input":{"type":"object","properties":{}}}`,
	// An ordinary task on a PRIVATE target the completer cannot read.
	"SO": `{"id":"SO","required_caller":"bob@testnet","owner":"alice@testnet","requester":"alice@testnet","action":"alice@testnet/private-work",
	  "price":5,"status":"waiting","created_by":"sys@testnet/message","created_at":"2026-08-02T00:00:00Z",
	  "partial_args":{"context":"sealed"},
	  "allowed_input":{"type":"object","properties":{"answer":{"type":"string","description":"Your answer"}},"required":["answer"]}}`,
	// A task another kernel holds for bob: no action, no creator, owner the holder.
	"SH": `{"id":"SH","required_caller":"bob@testnet","owner":"` + heldKey + `","requester":"dan@bravo",
	  "price":0,"status":"waiting","created_at":"2026-08-03T00:00:00Z",
	  "partial_args":{"message":"from afar"},"allowed_input":{"type":"object"}}`,
	// A task on another kernel's action: never read here.
	"SR": `{"id":"SR","required_caller":"bob@testnet","owner":"alice@testnet","requester":"alice@testnet","action":"vendor@bravo/thing",
	  "price":0,"status":"waiting","created_by":"alice@testnet/ask","created_at":"2026-08-04T00:00:00Z",
	  "partial_args":{"note":"x"},"allowed_input":{"type":"object"}}`,
	// Bob asked carol: his to withdraw, not to complete.
	"SX": `{"id":"SX","required_caller":"carol@testnet","owner":"bob@testnet","requester":"bob@testnet","action":"carol@testnet/review",
	  "price":7,"status":"waiting","created_by":"bob@testnet/ask","created_at":"2026-08-05T00:00:00Z",
	  "partial_args":{},"allowed_input":{"type":"object"}}`,
	// Done, asked by alice: bob answered it, and only alice may read the answer.
	"SD": `{"id":"SD","required_caller":"bob@testnet","owner":"alice@testnet","requester":"alice@testnet","action":"alice@testnet/hello",
	  "price":0,"status":"done","tx_id":"TD","created_at":"2026-07-01T00:00:00Z","partial_args":{}}`,
	// Done, asked by bob of carol: its answer is bob's to read.
	"SA": `{"id":"SA","required_caller":"carol@testnet","owner":"bob@testnet","requester":"bob@testnet","action":"carol@testnet/review",
	  "price":0,"status":"done","tx_id":"TA","created_by":"bob@testnet/ask","created_at":"2026-07-02T00:00:00Z","partial_args":{}}`,
	// Done, sent by bob to himself: a Done row, whose answer he may read.
	"SS": `{"id":"SS","required_caller":"bob@testnet","owner":"bob@testnet","requester":"chat@testnet","action":"chat@testnet/validate",
	  "price":0,"status":"done","tx_id":"TS","created_by":"chat@testnet/register","created_at":"2026-07-03T00:00:00Z","partial_args":{}}`,
}

// taskActionReads, taskCancels and taskDecisions record what the BFF asked of
// the task kernel.
var taskActionReads, taskCancels, taskDecisions []string

// taskKernel serves taskRows and three requesters to bob@testnet.
func taskKernel() *httptest.Server {
	taskActionReads, taskCancels, taskDecisions = nil, nil, nil
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
		for _, id := range []string{"SP", "SO", "SH", "SR", "SX", "SD", "SA", "SS"} {
			rows = append(rows, taskRows[id])
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	})
	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(taskRows[r.PathValue("id")]))
	})
	mux.HandleFunc("GET /v1/me/requesters", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		w.Write([]byte(`[{"requester":"eve@testnet","status":"blocked","since":"2026-07-01T00:00:00Z"},
		 {"requester":"alice@testnet","status":"approved","since":"2026-07-02T00:00:00Z"},
		 {"requester":"mallory@bravo","status":"pending","since":"2026-08-06T09:30:00Z"}]`))
	})
	mux.HandleFunc("POST /v1/me/requesters/{address}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		taskDecisions = append(taskDecisions, r.PathValue("address")+" "+r.PathValue("verb"))
		w.Write([]byte(`{"requester":"` + r.PathValue("address") + `","status":"approved","since":"2026-08-06T09:30:00Z"}`))
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
		taskActionReads = append(taskActionReads, r.URL.RawQuery)
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	return httptest.NewServer(mux)
}

// Every completion is the neutral Complete naming no amount, a transfer task
// included: answering a task spends nothing of the completer's (kernel U52),
// so the inbox reads no action to tell the two apart. The card names the
// requester the task acts for.
func TestInboxCompletionNamesNoAmount(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	taskActionReads = nil // what the inbox asks, not the landing signed in to

	page := getBody(t, cl, srv.URL+"/inbox")
	if strings.Contains(page, "btn-money") || strings.Contains(page, "Complete (") {
		t.Errorf("no completion names an amount: %s", page)
	}
	if !regexp.MustCompile(`(?s)<form[^>]*action="/inbox/SP/complete".*?<button class="btn-primary" type="submit">Complete</button>`).MatchString(page) {
		t.Errorf("a transfer task completes with Complete, its form's primary and never a money button: %s", page)
	}
	if !strings.Contains(page, `name="f_/answer"`) {
		t.Errorf("completion form must derive from allowed_input: %s", page)
	}
	if strings.Contains(page, "could not be found") {
		t.Errorf("an unreadable target must not surface as an error: %s", page)
	}
	if len(taskActionReads) != 0 {
		t.Errorf("the inbox reads no action: %v", taskActionReads)
	}
	if !strings.Contains(page, `asked by <span class="mono">alice@testnet</span> · via`) || !strings.Contains(page, `asked by <span class="mono">dan@bravo</span> · `) {
		t.Errorf("each card names its requester: %s", page)
	}
	// A task another kernel holds names no action: its head is that kernel.
	if !regexp.MustCompile(`<h3 class="flush"><span class="mono" title="[^"]+">`).MatchString(page) {
		t.Errorf("a held task heads with the holding kernel's name: %s", page)
	}
	if resp, err := cl.Get(srv.URL + "/inbox/SO"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("a task has no page of its own: %v %v", resp.StatusCode, err)
	}
}

// Who may send you tasks (kernel U52) stands in two places, each status once:
// the pending ones as requests above your tasks, with Approve and Block; the
// approved and blocked ones as contacts in Settings, with Remove and Block,
// or Unblock. Block alone sits behind a confirm, an unknown verb is no
// command, and a decision returns to the page it was made on.
func TestRequestsAndContacts(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	verbs := func(page, addr string) []string {
		var got []string
		for _, v := range []string{"approve", "revoke", "block"} {
			if strings.Contains(page, `action="/inbox/requesters/`+addr+`/`+v+`"`) {
				got = append(got, v)
			}
		}
		return got
	}
	inbox := getBody(t, cl, srv.URL+"/inbox")
	if !strings.Contains(inbox, "<h2>Requests</h2>") || !strings.Contains(inbox, `<span class="mono">mallory@bravo</span>`) || !strings.Contains(inbox, "since 2026-08-06") {
		t.Error("a pending requester is a request, named with its date")
	}
	if got := verbs(inbox, "mallory@bravo"); !slices.Equal(got, []string{"approve", "block"}) {
		t.Errorf("a request offers %v, want approve and block", got)
	}
	if !strings.Contains(inbox, `<button class="btn" type="submit">Approve</button>`) || !strings.Contains(inbox, `data-confirm="Block mallory@bravo?"`) {
		t.Error("Approve is neutral; Block asks for a confirm")
	}
	for _, addr := range []string{"alice@testnet", "eve@testnet"} {
		if len(verbs(inbox, addr)) != 0 {
			t.Errorf("%s is a contact, not a request", addr)
		}
	}

	settings := getBody(t, cl, srv.URL+"/settings")
	a, e := strings.Index(settings, `<td class="mono">alice@testnet</td>`), strings.Index(settings, `<td class="mono">eve@testnet</td>`)
	if !strings.Contains(settings, "<h2>Contacts</h2>") || a < 0 || e < a {
		t.Errorf("contacts list approved before blocked: %d %d", a, e)
	}
	if len(verbs(settings, "mallory@bravo")) != 0 {
		t.Error("a pending requester is not a contact")
	}
	row := func(i int) string { return settings[i : i+strings.Index(settings[i:], "</tr>")] }
	if r := row(a); !slices.Equal(verbs(r, "alice@testnet"), []string{"revoke", "block"}) || !strings.Contains(r, ">Remove</button>") || !strings.Contains(r, `data-confirm="Block alice@testnet?"`) {
		t.Errorf("an approved contact offers Remove and Block: %s", r)
	}
	if r := row(e); !slices.Equal(verbs(r, "eve@testnet"), []string{"revoke"}) || !strings.Contains(r, ">Unblock</button>") || strings.Contains(r, "data-confirm") {
		t.Errorf("a blocked contact offers Unblock alone: %s", r)
	}
	if n := strings.Count(settings, `<input type="hidden" name="back" value="/settings">`); n != 3 {
		t.Errorf("each contact's decision returns to Settings: %d forms say so", n)
	}

	resp := postTo(t, cl, srv.URL+"/inbox/requesters/mallory@bravo/approve", inbox, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/inbox" {
		t.Errorf("approve = %d → %q, want a redirect to the inbox", resp.StatusCode, resp.Header.Get("Location"))
	}
	if f := flashOf(t, cl, srv.URL); f != "success|Approved." {
		t.Errorf("flash = %q", f)
	}
	resp = postTo(t, cl, srv.URL+"/inbox/requesters/eve@testnet/revoke", settings, url.Values{"back": {"/settings"}})
	resp.Body.Close()
	if resp.Header.Get("Location") != "/settings" {
		t.Errorf("unblock lands on %q, want Settings", resp.Header.Get("Location"))
	}
	resp = postTo(t, cl, srv.URL+"/inbox/requesters/eve@testnet/revoke", settings, url.Values{"back": {"https://evil.example"}})
	resp.Body.Close()
	if resp.Header.Get("Location") != "/inbox" {
		t.Errorf("an unknown back lands on %q, want the inbox", resp.Header.Get("Location"))
	}
	resp = postTo(t, cl, srv.URL+"/inbox/requesters/mallory@bravo/delete", inbox, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown verb = %d, want 404", resp.StatusCode)
	}
	if got := strings.Join(taskDecisions, ","); got != "mallory@bravo approve,eve@testnet revoke,eve@testnet revoke" {
		t.Errorf("decisions = %q", got)
	}
}

// The attention badge counts the waiting tasks that require the viewer and
// each pending requester, whose tasks are withheld.
func TestTasksBadgeCountsPendingRequesters(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	// SP, SO, SH, SR wait on bob; mallory@bravo is pending. The heartbeat
	// answers the badge when the page shows another count, and nothing when it
	// shows this one; an unknown kind is ignored.
	if got := getBody(t, cl, srv.URL+"/tick?slow=inbox:badge:0&slow=nobody:x:y"); !strings.Contains(got, ">5</span>") || !strings.Contains(got, `value="inbox:badge:5"`) {
		t.Errorf("badge = %q, want 5", got)
	}
	if got := getBody(t, cl, srv.URL+"/tick?slow=inbox:badge:5"); strings.TrimSpace(got) != "" {
		t.Errorf("an unchanged badge must not be swapped, got %q", got)
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

// The inbox's three tabs hold each task once (§6.3): To do the cards waiting
// on the viewer, with Decline; Sent what the viewer asked of others, Cancel
// while it waits; Done what was addressed to the viewer and no longer waits.
// A row opens its answer only where the viewer owns the task, since only the
// owner may read the completion's transaction.
func TestInboxTabs(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL) // bob@testnet

	ids := func(page string) []string {
		var got []string
		for _, id := range []string{"SP", "SO", "SH", "SR", "SX", "SD", "SA", "SS"} {
			if strings.Contains(page, `title="`+id+`"`) || strings.Contains(page, `action="/inbox/`+id+`/complete"`) {
				got = append(got, id)
			}
		}
		return got
	}
	todo := getBody(t, cl, srv.URL+"/inbox")
	if got := ids(todo); !slices.Equal(got, []string{"SP", "SO", "SH", "SR"}) {
		t.Errorf("To do holds %v, want the four waiting on bob", got)
	}
	icon := func(id string) string {
		return `<svg class="ico ico-inline" aria-hidden="true"><use href="/static/icons.svg#` + id + `"/></svg> `
	}
	if !strings.Contains(todo, `<a class="tab active" href="/inbox" aria-current="page">`+icon("inbox")+`To do</a>`) ||
		!strings.Contains(todo, `<a class="tab" href="/inbox?tab=sent">`+icon("arrow-up")+`Sent</a>`) ||
		!strings.Contains(todo, `<a class="tab" href="/inbox?tab=done">`+icon("check")+`Done</a>`) {
		t.Error("the inbox has three tabs, each a place with its icon and its word, To do current by default")
	}
	if !strings.Contains(getBody(t, cl, srv.URL+"/inbox?tab=sent"), "<th>Runs</th>") {
		t.Error("a Sent row names what runs")
	}
	// A filter tab is a word: icons mark places only.
	if regexp.MustCompile(`class="tab[^"]*"[^>]*><svg`).MatchString(getBody(t, cl, srv.URL+"/workshop")) {
		t.Error("a filter tab carries no icon")
	}
	// Decline stands in the card's command row, beside Complete, behind its
	// plain confirm, submitting its own form through the form attribute.
	if !regexp.MustCompile(`(?s)<div class="cluster">\s*<button class="btn[^"]*" type="submit">Complete[^<]*</button>\s*<button class="btn-danger" type="submit" form="decline-SP">Decline</button>`).MatchString(todo) ||
		!strings.Contains(todo, `<form id="decline-SP" method="post" action="/inbox/SP/cancel" data-confirm="Decline this task?">`) {
		t.Error("Decline must sit beside Complete in one row, behind a confirm")
	}

	sent := getBody(t, cl, srv.URL+"/inbox?tab=sent")
	if got := ids(sent); !slices.Equal(got, []string{"SX", "SA"}) {
		t.Errorf("Sent holds %v, want what bob asked of others", got)
	}
	if !strings.Contains(sent, "<th>To</th>") || !strings.Contains(sent, `<td class="mono">carol@testnet</td>`) {
		t.Error("a Sent row names the one asked")
	}
	if !strings.Contains(sent, `<form id="decline-SX" method="post" action="/inbox/SX/cancel" data-confirm="Cancel this task?">`) ||
		!strings.Contains(sent, `<button class="btn-danger" type="submit" form="decline-SX">Cancel</button>`) {
		t.Error("a waiting Sent row is cancelled by the one who asked, behind a confirm")
	}
	if strings.Contains(sent, `/inbox/SA/cancel`) || strings.Contains(sent, `/inbox/SX/complete`) {
		t.Error("a finished task offers no Cancel, and only the one asked may complete")
	}
	if !strings.Contains(sent, `<a href="/transactions/TA" title="SA">SA</a>`) || strings.Contains(sent, `href="/transactions/TX`) {
		t.Error("a done Sent row opens its answer; a waiting one has none")
	}

	done := getBody(t, cl, srv.URL+"/inbox?tab=done")
	if got := ids(done); !slices.Equal(got, []string{"SD", "SS"}) {
		t.Errorf("Done holds %v, want what was addressed to bob and finished", got)
	}
	if !strings.Contains(done, "<th>From</th>") || !strings.Contains(done, `<td class="mono">alice@testnet</td>`) {
		t.Error("a Done row names the one who asked")
	}
	if strings.Contains(done, `href="/transactions/TD"`) || !strings.Contains(done, `<span title="SD">SD</span>`) {
		t.Error("a task alice asked opens no answer: bob cannot read its transaction")
	}
	if !strings.Contains(done, `<a href="/transactions/TS" title="SS">SS</a>`) || strings.Contains(done, "/cancel") {
		t.Error("a task bob sent himself opens its answer, once, on Done")
	}
	for _, p := range []string{"/inbox/SX", "/inbox/SD"} {
		if resp, err := cl.Get(srv.URL + p); err != nil || resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: a task has no page of its own", p)
		}
	}

	if !strings.Contains(sent, `<input type="hidden" name="verb" value="Cancel">`) {
		t.Error("the cancel form names its verb, for the flash")
	}
	resp := postTo(t, cl, srv.URL+"/inbox/SX/cancel", sent, url.Values{"verb": {"Cancel"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/inbox" {
		t.Errorf("cancel = %d → %q, want a redirect to the inbox", resp.StatusCode, resp.Header.Get("Location"))
	}
	if f := flashOf(t, cl, srv.URL); f != "success|Task cancelled." {
		t.Errorf("flash = %q, want the sender's word", f)
	}
	if strings.Join(taskCancels, ",") != "SX" {
		t.Errorf("kernel cancels = %v", taskCancels)
	}
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
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") == "sys@testnet/lookup" {
			w.Write([]byte(`[{"id":"L","action":"sys@testnet/lookup","name":"lookup","kind":"native","active":true,"price":0}]`))
			return
		}
		w.Write([]byte(`[]`))
	})
	// sys/lookup: one unresolved discovery hit and one RESOLVED proxy. Both
	// carry a kernel-qualified ref and an observed_at; only the second has a
	// local row. A third is local and fails its read. A lookup row carries no
	// `active`, as the kernel serves it.
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"results":[
			{"action_id":"remote-unresolved","action":"vendor@bravo/fresh","description":"never called","price":111,"observed_at":"2026-08-18T17:00:10Z"},
			{"action_id":"proxy-local","action":"vendor@bravo/bought","description":"already resolved","price":111,"observed_at":"2026-08-18T17:00:10Z"},
			{"action_id":"gone","action":"alice@testnet/gone","title":"Gone","description":"vendor of nothing","price":0}
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
	// The lookup ranks active actions only, so a hit whose read failed — remote
	// or local — is not stated inactive: nothing said it is.
	if !strings.Contains(body, `href="/actions/gone"`) || strings.Contains(body, ">inactive<") {
		t.Errorf("a lookup hit is never stated inactive for want of a read: %s", body)
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
			fmt.Fprintf(w, `[{"id":"D","action":"sys@testnet/llm/decide","name":"decide","kind":"native","active":true,"price":%d,"quote_hash":"DQ"}]`, decidePrice)
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
		ranActions = append(ranActions, in.Action)
		if in.Action == "sys@testnet/llm/decide" {
			lastDecide = string(b)
			fmt.Fprintf(w, `{"result":%s,"tx_id":"T1"}`, decision)
			return
		}
		w.Write([]byte(`{"result":{"results":[{"action_id":"WEB","action":"sys@testnet/web","description":"Fetch a public web page","price":0}]},"tx_id":"T0"}`))
	})
	return httptest.NewServer(mux)
}

var (
	lastDecide string
	ranActions []string
)

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

// An operator may price the planner. Then the command is offered on the money
// button naming its price, and the run is pinned to the quote shown: asking is
// a spend the person authorizes, never a silent one (§8.2).
func TestPricedPlannerIsPaidOnItsButton(t *testing.T) {
	lastDecide = ""
	kernel := proposalKernel(t, 9, `{"action":"sys@testnet/web","args":{}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse?q=fetch+a+page")
	if !strings.Contains(page, `<button class="btn-money" type="submit">Choose for me (9 credits)</button>`) || !strings.Contains(page, `name="planner_quote_hash" value="DQ"`) {
		t.Fatalf("a priced planner is offered on the money button, pinned: %s", page)
	}
	resp := postTo(t, cl, srv.URL+"/browse/propose", page, url.Values{"q": {"fetch a page"}, "ref": {"sys@testnet/web"}, "planner_quote_hash": {"DQ"}})
	resp.Body.Close()
	if !strings.Contains(lastDecide, `"quote_hash":"DQ"`) {
		t.Errorf("the planner's run is pinned to the quote shown: %s", lastDecide)
	}
}

// convApp is a conversations app for the core's tests: a room "r1" whose
// conversation is fixed, refused for any other room.
type convApp struct{ launchApp }

func (convApp) Rail(*http.Request) (template.HTML, error) { return `<nav class="rail"></nav>`, nil }
func (convApp) Landing() string                           { return "/browse" }
func (convApp) Context(_ *http.Request, room string) ([]client.Message, error) {
	if room != "r1" {
		return nil, fmt.Errorf("not a member")
	}
	return []client.Message{{Role: "user", Content: "fetch example.com for me"}}, nil
}

// Fill from chat prepares an action's fields from the conversation the panel
// was opened from — the planner with this one action, pinned to its quote; an
// argument the conversation does not state stays blank; nothing runs but the
// planner.
func TestFillPreparesTheFieldsFromTheConversation(t *testing.T) {
	kernel := proposalKernel(t, 0, `{"action":"sys@testnet/web","args":{"url":"example.com"}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) { w.apps = []apps.App{convApp{}} }))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	panel := getBody(t, cl, srv.URL+"/actions/WEB/panel?room=r1")
	for _, want := range []string{`name="room" value="r1"`, `name="fill_quote_hash" value="DQ"`, `<button type="button" class="btn-icon" data-fill="/actions/WEB/fill" title="Fill from chat" aria-label="Fill from chat"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#sparkles"/></svg></button>`, `<span>Filling…</span>`} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel lacks %s", want)
		}
	}
	// Fill is a tool of the form, in its top row; Run is the foot's one button.
	tools := panel[strings.Index(panel, `<div class="form-tools">`):]
	tools = tools[:strings.Index(tools, "</div>\n")]
	if !strings.Contains(tools, "data-fill") || !strings.Contains(tools, "data-json-switch") {
		t.Errorf("the top row holds the switch and Fill: %s", tools)
	}
	foot := panel[strings.LastIndex(panel, `<div class="cluster">`):]
	if strings.Contains(foot[:strings.Index(foot, "</div>")], "data-fill") {
		t.Error("Fill must not sit beside Run")
	}
	if bare := getBody(t, cl, srv.URL+"/actions/WEB/panel"); strings.Contains(bare, "data-fill") || strings.Contains(bare, "Filling…") {
		t.Error("with no conversation there is nothing to fill from")
	}

	lastDecide, ranActions = "", nil
	resp := postTo(t, cl, srv.URL+"/actions/WEB/fill", panel, url.Values{"room": {"r1"}, "fill_quote_hash": {"DQ"}})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `name="f_/url" rows="1">example.com`) {
		t.Fatalf("fill = %d %s", resp.StatusCode, b)
	}
	for _, want := range []string{`"role":"system"`, `"actions":["sys@testnet/web"]`, `"quote_hash":"DQ"`, "fetch example.com for me"} {
		if !strings.Contains(lastDecide, want) {
			t.Errorf("the planner was not given %s: %s", want, lastDecide)
		}
	}
	if len(ranActions) != 1 || ranActions[0] != "sys@testnet/llm/decide" {
		t.Errorf("a fill runs the planner alone, ran %v", ranActions)
	}
	resp = postTo(t, cl, srv.URL+"/actions/WEB/fill", panel, url.Values{"fill_quote_hash": {"DQ"}})
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(b), "nothing to prepare") {
		t.Errorf("with no conversation there is nothing to prepare from: %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	resp = postTo(t, cl, srv.URL+"/actions/WEB/fill", panel, url.Values{"room": {"elsewhere"}})
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(b), "Not a member") {
		t.Errorf("a room the viewer is not in is refused: %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

// An argument the conversation does not state comes back empty and stays
// blank: the fill never invents a value to complete the form.
func TestFillLeavesTheUnstatedBlank(t *testing.T) {
	kernel := proposalKernel(t, 0, `{"action":"sys@testnet/web","args":{"url":""}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) { w.apps = []apps.App{convApp{}} }))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	panel := getBody(t, cl, srv.URL+"/actions/WEB/panel?room=r1")
	resp := postTo(t, cl, srv.URL+"/actions/WEB/fill", panel, url.Values{"room": {"r1"}})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `name="f_/url" rows="1"></textarea>`) {
		t.Errorf("an unstated argument stays blank: %s", b)
	}
}

// A priced planner prices the fill: the tool is the money button naming it.
func TestPricedFillIsAMoneyButton(t *testing.T) {
	kernel := proposalKernel(t, 9, `{"action":"sys@testnet/web","args":{}}`)
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) { w.apps = []apps.App{convApp{}} }))
	defer srv.Close()
	panel := getBody(t, loginClient(t, srv.URL), srv.URL+"/actions/WEB/panel?room=r1")
	if !strings.Contains(panel, `<button type="button" class="btn-money" data-fill="/actions/WEB/fill" title="Fill from chat — 9 credits" aria-label="Fill from chat — 9 credits"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#sparkles"/></svg><span>9 credits</span></button>`) {
		t.Errorf("a priced fill is authorized on the money button: %s", panel)
	}
}

// Feedback the UI collects has to come back out somewhere, or it is a number on
// a card and nothing more. The signals the kernel already publishes — the
// ratings and the price — order the list, so a buyer can choose by them. The
// rating order is the bars' own: by the share of good ratings each confirms
// (§6.2).
func TestBrowseSortsByTheSignalsTheKernelServes(t *testing.T) {
	rows := map[string]string{
		"CHEAP": `{"id":"CHEAP","action":"alice@testnet/cheap","name":"cheap","kind":"wasm","active":true,"price":1}`,
		"LOVED": `{"id":"LOVED","action":"alice@testnet/loved","name":"loved","kind":"wasm","active":true,"price":50}`,
		"MEH":   `{"id":"MEH","action":"alice@testnet/meh","name":"meh","kind":"wasm","active":true,"price":10}`,
		"BAD":   `{"id":"BAD","action":"alice@testnet/bad","name":"bad","kind":"wasm","active":true,"price":20}`,
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
		w.Write([]byte("[" + rows["CHEAP"] + "," + rows["LOVED"] + "," + rows["MEH"] + "," + rows["BAD"] + "]"))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		switch id {
		case "LOVED":
			w.Write([]byte(withEvidence(rows[id], `{"uses":9,"successes":9,"rating_count":9,"rating_estimate":0.95}`)))
		case "MEH":
			w.Write([]byte(withEvidence(rows[id], `{"uses":9,"successes":4,"rating_count":9,"rating_estimate":0.2}`)))
		case "BAD":
			w.Write([]byte(withEvidence(rows[id], `{"uses":3,"successes":0,"rating_count":3,"rating_estimate":0}`)))
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
		out := []string{"/actions/CHEAP", "/actions/LOVED", "/actions/MEH", "/actions/BAD"}
		sort.Slice(out, func(i, j int) bool { return strings.Index(body, out[i]) < strings.Index(body, out[j]) })
		return out
	}
	if got := order(getBody(t, cl, srv.URL+"/browse?sort=price")); got[0] != "/actions/CHEAP" || got[3] != "/actions/LOVED" {
		t.Errorf("price order = %v", got)
	}
	if got := order(getBody(t, cl, srv.URL+"/browse?sort=rating")); got[0] != "/actions/LOVED" {
		t.Errorf("rating order must put the best-rated first: %v", got)
	}
	// A row nobody has rated has nothing confirmed good, so it follows every
	// row with anything confirmed; among rows with nothing confirmed good, it
	// precedes the one confirmed poor.
	if got := order(getBody(t, cl, srv.URL+"/browse?sort=rating")); got[1] != "/actions/MEH" || got[2] != "/actions/CHEAP" || got[3] != "/actions/BAD" {
		t.Errorf("rating order must be what each bar confirms: %v", got)
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
		if strings.Contains(sent, `"signature":"forged"`) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"unauthorized","error":"signature was not made by that address"}`))
			return
		}
		w.Write([]byte(`{"blockchain_address":"0xalice","attributed":[{"amount":5,"from":"sys@testnet","to":"alice@testnet"}]}`))
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
	form.Set("signature", "forged")
	resp = postTo(t, cl, srv.URL+"/wallet/address", after, form)
	resp.Body.Close()
	if forged := getBody(t, cl, srv.URL+"/wallet"); !strings.Contains(forged, "The signature was not made by that address.") {
		t.Errorf("a signature by another key must say so: %s", forged)
	}
	form.Set("signature", "sig")
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

// catalogKernel is a fake kernel for the catalog and the type-ahead. It counts
// action-list reads and runs, so a test can assert what each page asked the
// kernel for.
type catalogKernel struct {
	*httptest.Server
	actionLists int
	runs        int
	lookup      string // the sys/lookup row a ref read answers; none when empty
}

func newCatalogKernel(t *testing.T) *catalogKernel {
	k := &catalogKernel{}
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
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		if ref := r.URL.Query().Get("ref"); ref != "" {
			if ref == "sys@testnet/lookup" && k.lookup != "" {
				w.Write([]byte("[" + k.lookup + "]"))
				return
			}
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

// The root address renders nothing: it sends a session to where it begins,
// the catalog when no conversations app is mounted (§6.1).
func TestRootSendsToTheLanding(t *testing.T) {
	k := newCatalogKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/browse" {
		t.Errorf("/ = %d to %q, want 303 to /browse", resp.StatusCode, resp.Header.Get("Location"))
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
	Catalog []catalogEntry `json:"catalog"`
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

// The launcher's actions come from one catalog file, as a static site's search
// index does: every action the viewer may see — local and discovered — with
// its normalized key, its id (none for a discovered one) and its address,
// ordered by key. It is read in one catalog read, never runs anything, and is
// never stored by the browser (it holds the account's private titles).
func TestSearchCatalogServesEveryAction(t *testing.T) {
	k := newCatalogKernel(t)
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
	var file catalogFile
	if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	got := file.Actions
	if file.Search != nil {
		t.Errorf("a kernel without a lookup offers no search: %+v", file.Search)
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
		if e.Ref == "" {
			t.Errorf("%q carries no address", e.T)
		}
		if e.K == "weather forecast" && e.ID != "" {
			t.Errorf("a discovered action has no local id, got %q", e.ID)
		}
	}
	if keys["weather forecast"] != 1 {
		t.Error("a discovered action belongs in the catalog")
	}
	if keys["summarize a page"] != 2 || keys["greet 0"] != 1 || len(got) != 15 {
		t.Errorf("every action, two sharing a title, all 15 of them: %+v", got)
	}
}

// The catalog file carries what submitting a search costs: the lookup's price
// as the launcher's row names it and the quote that pins it, an empty price
// when free, and no search at all when the lookup is absent, switched off, or
// priced without a quote to pin — a priced search is never offered unpinned.
func TestSearchCatalogNamesTheSearchsTerms(t *testing.T) {
	k := newCatalogKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	terms := func() *searchTerms {
		t.Helper()
		var file catalogFile
		if err := json.Unmarshal([]byte(getBody(t, cl, srv.URL+"/search/catalog.json")), &file); err != nil {
			t.Fatal(err)
		}
		return file.Search
	}
	const row = `{"id":"L","action":"sys@testnet/lookup","name":"lookup","kind":"native","active":%t,"price":%d,"quote_hash":%q}`
	k.lookup = fmt.Sprintf(row, true, 7, "LQ")
	if got := terms(); got == nil || got.Price != "7 credits" || got.QuoteHash != "LQ" {
		t.Errorf("a priced lookup is offered at its price and pin: %+v", got)
	}
	k.lookup = fmt.Sprintf(row, true, 0, "LQ")
	if got := terms(); got == nil || got.Price != "" || got.QuoteHash != "LQ" {
		t.Errorf("a free lookup names no price: %+v", got)
	}
	k.lookup = fmt.Sprintf(row, true, 7, "")
	if got := terms(); got != nil {
		t.Errorf("a priced lookup with no quote to pin is never offered: %+v", got)
	}
	k.lookup = fmt.Sprintf(row, false, 0, "LQ")
	if got := terms(); got != nil {
		t.Errorf("a switched-off lookup is not offered: %+v", got)
	}
	if k.runs != 0 {
		t.Errorf("reading the terms ran %d actions", k.runs)
	}
}

// The browse catalog renders a discovered row in its discovered form — linked
// to resolve on open, never to a local action page it does not have — and
// the workshop, which manages this kernel's actions, leaves it out.
func TestCatalogDiscoveredRows(t *testing.T) {
	k := newCatalogKernel(t)
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
// null box that nulls its subtree, an escaped key, a JSON field sent as the
// value it holds, and an untouched one, omitted like any blank field (§6.2).
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
		"f_/meta": {""}, "t_/meta": {"json"},
		"f_/any": {"5"}, "t_/any": {"json"},
		"f_/none": {"null"}, "t_/none": {"json"},
		"f_/a~1b": {"slash"}, "t_/a~1b": {"string"},
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw, err := buildArgs(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a/b":"slash","any":5,"messages":[{"content":"hi","role":"user"},{"content":"later","role":"system"}],"none":null,"options":{"depth":3},"tool":null}`
	if string(raw) != want {
		t.Errorf("args = %s\nwant %s", raw, want)
	}
	form = url.Values{"has_fields": {"1"}, "f_/any": {"{bad"}, "t_/any": {"json"}}
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := buildArgs(req); err == nil || !strings.Contains(err.Error(), "must be valid JSON") {
		t.Errorf("invalid JSON = %v", err)
	}
}

// A required container of an object being sent is sent even when nothing in
// it was filled — empty, or holding its own required containers — since
// absence is invalid for it; a wholly blank row and an optional empty
// container are still dropped (§6.2).
func TestBuildArgsSendsRequiredContainers(t *testing.T) {
	form := url.Values{
		"has_fields": {"1"},
		"t_/eps":     {"array"}, "r_/eps": {"1"},
		"t_/eps/0": {"object"}, "f_/eps/0/name": {"a"}, "t_/eps/0/name": {"string"},
		"t_/eps/0/models": {"array"}, "r_/eps/0/models": {"1"},
		"t_/eps/0/models/0": {"object"}, "f_/eps/0/models/0/kind": {""}, "t_/eps/0/models/0/kind": {"string"},
		"t_/eps/1": {"object"}, "f_/eps/1/name": {""}, "t_/eps/1/name": {"string"},
		"t_/eps/1/models": {"array"}, "r_/eps/1/models": {"1"},
		"t_/opt": {"array"}, "f_/opt/0": {""}, "t_/opt/0": {"string"},
		"t_/need": {"object"}, "r_/need": {"1"},
		"t_/need/inner": {"object"}, "r_/need/inner": {"1"},
		"t_/need/list": {"array"},
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw, err := buildArgs(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"eps":[{"models":[],"name":"a"}],"need":{"inner":{}}}`
	if string(raw) != want {
		t.Errorf("args = %s\nwant %s", raw, want)
	}
}

// The JSON switch converts what is on screen: form → JSON is the args a submit
// would send, JSON → form the form's own fields filled exactly, or the switch
// is refused with each value the form cannot hold (§6.2).
func TestJSONSwitchConverts(t *testing.T) {
	kernel := taskKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/inbox")
	if !strings.Contains(page, `data-to-json="/form/json" data-to-form="/inbox/SO/form"`) {
		t.Fatal("a completion form names its two conversions")
	}

	post := func(path string, v url.Values) (int, string) {
		resp := postTo(t, cl, srv.URL+path, page, v)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := post("/form/json", url.Values{"has_fields": {"1"}, "f_/answer": {"yes"}, "t_/answer": {"string"}}); code != 200 || body != "{\n  \"answer\": \"yes\"\n}" {
		t.Errorf("form → JSON = %d %q", code, body)
	}
	if code, body := post("/form/json", url.Values{"has_fields": {"1"}, "f_/n": {"x"}, "t_/n": {"integer"}}); code != 422 || !strings.Contains(body, "must be an integer") {
		t.Errorf("a bad form = %d %q", code, body)
	}
	if code, body := post("/inbox/SO/form", url.Values{"raw_args": {`{"answer":"yes"}`}}); code != 200 || !strings.Contains(body, `name="f_/answer"`) || !strings.Contains(body, `>yes</textarea>`) {
		t.Errorf("JSON → form = %d %s", code, body)
	}
	if code, body := post("/inbox/SO/form", url.Values{"raw_args": {`{"answer":"","extra":1}`}}); code != 422 ||
		body != "/answer: an empty string can only be written in JSON; /extra: the form has no field for it.\n" {
		t.Errorf("an inexact JSON = %d %q", code, body)
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
	for _, want := range []string{`<label>q</label>`, `<div class="box">&lt;b&gt;x&lt;/b&gt;</div>`, `<summary>items <span class="muted small">2 items</span></summary>`, `<div class="rows">`, `<div class="box muted">null</div>`, `href="/transactions/T1/result.json" download`, "line1\nline2", `data-json-switch`} {
		if !strings.Contains(page, want) {
			t.Errorf("receipt page missing %q", want)
		}
	}
}

// TestPageReadsTheViewerOnce: a page reads /v1/me once, however many of its
// parts need the viewer (the envelope's balance, the handler's address).
func TestPageReadsTheViewerOnce(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	var reads atomic.Int32
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			reads.Add(1)
		}
		kernel.Config.Handler.ServeHTTP(w, r)
	}))
	defer counting.Close()
	srv := httptest.NewServer(newTestHandler(t, counting.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	for _, page := range []string{"/", "/wallet", "/inbox", "/settings", "/workshop", "/transactions"} {
		reads.Store(0)
		getBody(t, cl, srv.URL+page)
		if n := reads.Load(); n != 1 {
			t.Errorf("%s read /v1/me %d times, want 1", page, n)
		}
	}
}

// A refused proposal says why: the kernel's message for a typed error, the
// planner's or the conversation's own words otherwise.
func TestRefusalSaysWhy(t *testing.T) {
	if got := refusal(fmt.Errorf("model did not select an action")); got != "Model did not select an action." {
		t.Errorf("refusal = %q", got)
	}
	if got := refusal(&client.KernelError{Kind: client.KindInsufficientFunds}); strings.Contains(got, "Something went wrong") {
		t.Errorf("a typed error reads as its own sentence: %q", got)
	}
}

// The heartbeat's balance watch answers nothing while the amount shown holds,
// and the new line, out of band, when it moved.
func TestBalanceWatch(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	if got := getBody(t, cl, srv.URL+"/tick?watch=balance::500"); strings.Contains(got, "balance") {
		t.Errorf("an unchanged balance answers nothing: %s", got)
	}
	got := getBody(t, cl, srv.URL+"/tick?watch=balance::400")
	if !strings.Contains(got, `id="balance" hx-swap-oob="true">500 credits</p>`) || !strings.Contains(got, `value="balance::500" hx-swap-oob="true"`) {
		t.Errorf("a moved balance answers the new line: %s", got)
	}
}

// searchKernel is a fake kernel for the search submitted from a
// conversation: sys/lookup at a price and quote of the test's choosing, one
// local action and one known only through a peer, which resolves to a proxy.
// It records every run's body, and refuses a run pinned to a stale quote as
// the kernel does.
type searchKernel struct {
	*httptest.Server
	price     int64
	quote     string
	runs      []string
	failList  bool
	failTerms bool // the lookup's own row cannot be read
}

func newSearchKernel(t *testing.T) *searchKernel {
	k := &searchKernel{quote: "LQ"}
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
	const hello = `{"id":"A1","action":"alice@testnet/hello","name":"hello","title":"Hello","kind":"wasm","active":true,"price":3,"description":"says hello",
		"evidence":{"local_experience":{"rating_count":2,"rating_estimate":0.5}}}`
	const proxy = `{"id":"P1","action":"tom@BRAVOKEY/hola","name":"hola","title":"Hola","kind":"remote_proxy","active":true,"price":1}`
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "A1":
			w.Write([]byte(hello))
		case "P1":
			w.Write([]byte(proxy))
		default:
			http.Error(w, `{"code":"not_found","error":"no such action"}`, http.StatusNotFound)
		}
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if pastEnd(w, r) {
			return
		}
		switch r.URL.Query().Get("ref") {
		case "sys@testnet/lookup":
			if k.failTerms {
				http.Error(w, `{"code":"internal","error":"boom"}`, http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, `[{"id":"L","action":"sys@testnet/lookup","name":"lookup","kind":"native","active":true,"price":%d,"quote_hash":%q}]`, k.price, k.quote)
		case "tom@BRAVOKEY/hola":
			w.Write([]byte("[" + proxy + "]"))
		case "":
			if k.failList {
				http.Error(w, `{"code":"internal","error":"boom"}`, http.StatusInternalServerError)
				return
			}
			w.Write([]byte("[" + hello + "]"))
		default:
			w.Write([]byte(`[]`))
		}
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		k.runs = append(k.runs, string(b))
		var in struct {
			QuoteHash string `json:"quote_hash"`
		}
		_ = json.Unmarshal(b, &in)
		if in.QuoteHash != "" && in.QuoteHash != k.quote {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"code":"terms_changed","error":"the action's terms changed","meta":{"price":"%d","quote_hash":%q}}`, k.price, k.quote)
			return
		}
		w.Write([]byte(`{"result":{"results":[
			{"action_id":"A1","action":"alice@testnet/hello","title":"Hello","price":3},
			{"action_id":"R9","action":"tom@BRAVOKEY/hola","title":"Hola","price":1,"observed_at":"2026-10-24T10:00:00Z"}]},"tx_id":"TL"}`))
	})
	k.Server = httptest.NewServer(mux)
	t.Cleanup(k.Close)
	return k
}

// postHTMX posts a form as htmx does, so a failure answers as a fragment.
func postHTMX(t *testing.T, cl *http.Client, urlStr, page string, form url.Values) (int, string) {
	t.Helper()
	form.Set("gorilla.csrf.Token", csrfTokenFrom(t, page))
	req, _ := http.NewRequest(http.MethodPost, urlStr, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// A search submitted from a conversation runs the lookup once, pinned to the
// quote its row named, and answers the panel with Discover's results: the
// rows ranked under every order, the sort switch linking Discover, and no
// pager. A priced lookup without a pin is never run: the panel falls back to
// the catalog and says why. A kernel that cannot be read is an error, never
// an empty result.
func TestSearchFromTheComposerAnswersThePanel(t *testing.T) {
	k := newSearchKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse")

	k.price = 7
	code, panel := postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"hello"}, "lookup_quote_hash": {"LQ"}})
	if code != 200 || len(k.runs) != 1 || !strings.Contains(k.runs[0], `"quote_hash":"LQ"`) || !strings.Contains(k.runs[0], `"query":"hello"`) {
		t.Fatalf("one pinned lookup: %d runs %v", code, k.runs)
	}
	for _, want := range []string{
		`<h2>Search “hello”</h2>`, `data-panel-close`,
		`data-href="/actions/A1" data-rank-relevance="0" data-rank-rating="0" data-rank-price="1"`,
		`data-href="/discovered?ref=tom%40BRAVOKEY%2fhola" data-rank-relevance="1" data-rank-rating="1" data-rank-price="0"`,
		`href="/browse?q=hello&amp;sort=rating"`, `href="/browse?q=hello"`,
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel lacks %s:\n%s", want, panel)
		}
	}
	if strings.Contains(panel, `class="pager"`) || strings.Contains(panel, "Choose for me") || strings.Contains(panel, "Semantic search unavailable") {
		t.Errorf("a search in the panel is one ranked page, with no planner and no fallback: %s", panel)
	}

	k.runs = nil
	_, panel = postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"hello"}})
	if len(k.runs) != 0 {
		t.Errorf("a priced lookup ran without a pin: %v", k.runs)
	}
	if !strings.Contains(panel, "priced on this kernel") || !strings.Contains(panel, `data-href="/actions/A1"`) {
		t.Errorf("an unpinned priced search falls back to the catalog and says why: %s", panel)
	}

	// A failed search is answered in the panel's own head, so it can be
	// closed (htmx swaps a 2xx answer alone).
	if code, body := postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"  "}}); code != 200 || !strings.Contains(body, "Type what to search for.") || !strings.Contains(body, "data-panel-close") {
		t.Errorf("an empty query is refused in the panel: %d %s", code, body)
	}

	k.failList = true
	if _, body := postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"hello"}}); !strings.Contains(body, "banner-error") || strings.Contains(body, "No matches") ||
		!strings.Contains(body, "<h2>Search “hello”</h2>") || !strings.Contains(body, "data-panel-close") {
		t.Errorf("a kernel that cannot be read is an error, not an empty result, and the panel still closes: %s", body)
	}
}

// Only a lookup a read confirms free runs without a pin: one whose price
// cannot be read is never run unpinned — the kernel would charge it whatever
// it costs — on Discover's GET or from the panel, and the search falls back
// to the catalog saying why. A pinned run is the person's authorization and
// goes ahead.
func TestUnreadableLookupPriceNeverRunsUnpinned(t *testing.T) {
	k := newSearchKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse")

	k.failTerms = true
	body := getBody(t, cl, srv.URL+"/browse?q=hello")
	_, panel := postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"hello"}})
	if len(k.runs) != 0 {
		t.Fatalf("an unreadable price ran the lookup unpinned: %v", k.runs)
	}
	for _, got := range []string{body, panel} {
		if !strings.Contains(got, "Its price could not be read.") || !strings.Contains(got, `data-href="/actions/A1"`) {
			t.Errorf("the search falls back and says why: %s", got)
		}
	}
	postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"hello"}, "lookup_quote_hash": {"LQ"}})
	if len(k.runs) != 1 {
		t.Errorf("a pinned search runs once: %v", k.runs)
	}
}

// A pinned search refused because the lookup's price moved is said as such —
// nothing charged, the new price named — and asked again on one money button
// pinned to the new quote, never shown as a fallback.
func TestChangedSearchTermsAskAgainAtTheNewPrice(t *testing.T) {
	k := newSearchKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	page := getBody(t, cl, srv.URL+"/browse")

	k.price, k.quote = 9, "LQ2"
	_, panel := postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"hello"}, "lookup_quote_hash": {"LQ"}})
	for _, want := range []string{"Terms changed — you were not charged.", "Searching now costs 9 credits.",
		`name="q" value="hello"`, `name="lookup_quote_hash" value="LQ2"`, `<button class="btn-money" type="submit">Search “hello” (9 credits)</button>`,
		`hx-post="/browse/search" hx-target="#panel"`, `name="gorilla.csrf.Token"`} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel lacks %s:\n%s", want, panel)
		}
	}
	if strings.Contains(panel, "Semantic search unavailable") || strings.Contains(panel, "action-row") {
		t.Errorf("changed terms are not a fallback: %s", panel)
	}
	k.runs = nil
	_, panel = postHTMX(t, cl, srv.URL+"/browse/search", page, url.Values{"q": {"hello"}, "lookup_quote_hash": {"LQ2"}})
	if len(k.runs) != 1 || !strings.Contains(panel, `data-href="/actions/A1"`) {
		t.Errorf("accepting the new price searches once: %v", k.runs)
	}
}

// A row of a search opens in the panel whether or not this kernel holds it: a
// discovered one is resolved with its home kernel first, as its page is. A
// panel opened from results carries the way back, and the room for the fill.
func TestDiscoveredOpensInThePanel(t *testing.T) {
	k := newSearchKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	panel := getBody(t, cl, srv.URL+"/discovered/panel?ref="+url.QueryEscape("tom@BRAVOKEY/hola")+"&room=r1&back=1")
	for _, want := range []string{`data-panel-back`, `<h2>Hola`, `action="/actions/P1/run"`, `name="room" value="r1"`} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel lacks %s:\n%s", want, panel)
		}
	}
	if plain := getBody(t, cl, srv.URL+"/actions/A1/panel?room=r1"); strings.Contains(plain, "data-panel-back") {
		t.Error("a panel opened from the launcher has no results to return to")
	}
	missing := getBody(t, cl, srv.URL+"/discovered/panel?ref="+url.QueryEscape("tom@BRAVOKEY/gone")+"&back=1")
	for _, want := range []string{"That action could not be resolved from its kernel.", "data-panel-back", "data-panel-close"} {
		if !strings.Contains(missing, want) {
			t.Errorf("a failed open over results keeps the way back and the close, lacks %s: %s", want, missing)
		}
	}
	if gone := getBody(t, cl, srv.URL+"/actions/NOPE/panel"); strings.Contains(gone, "data-panel-back") || !strings.Contains(gone, "data-panel-close") {
		t.Errorf("a failed open from the launcher closes, with nothing to go back to: %s", gone)
	}
}

// rankCards numbers each row under every order by sortCards, the one rule: a
// rated row before an unrated one, the cheaper first, the served order kept.
func TestRankCardsFollowTheOneSortRule(t *testing.T) {
	card := func(price int64, rating float64, count int) actionCardView {
		a := client.Action{Price: price}
		if count > 0 {
			a.Evidence = &client.ActionEvidence{LocalExperience: &client.Stats{RatingCount: int64(count), RatingEstimate: rating}}
		}
		return actionCardView{Action: a}
	}
	cards := []actionCardView{card(3, .9, 4), card(1, 0, 0), card(2, .5, 4)}
	rankCards(cards)
	for i, want := range []cardRank{{0, 0, 2}, {1, 2, 0}, {2, 1, 1}} {
		if *cards[i].Rank != want {
			t.Errorf("row %d ranks %+v, want %+v", i, *cards[i].Rank, want)
		}
	}
}
