package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/server"
	svc "github.com/daios-ai/juice-ui/services/chat"
	"github.com/daios-ai/juice-ui/session"
	"github.com/daios-ai/juice-ui/supervisor"
)

func testAddress(h string) string { return h + "@testnet" }

// addr is a room's address at home: a thread of this kernel's chat, by its
// key. An address already whole is kept.
func addr(id string) string {
	if strings.Contains(id, "@") {
		return id
	}
	return id + "@" + testKey
}

// fakeKernel answers what chat asks of a kernel: the account's sign-in, the
// action catalog and its mutations, task creation through a capability,
// the task list, completions, runs and grants. It records every write.
type fakeKernel struct {
	srv         *httptest.Server
	mu          sync.Mutex
	seq         int
	acts        map[string]*fakeAction
	creates     []client.CreateActionInput
	updates     map[string][]string // id -> PUT bodies
	enables     []string
	deletes     []string
	tasks       []map[string]any // POST /v1/tasks bodies, served back by GET /v1/tasks
	completes   []string
	cancels     []string
	ungrants    []string
	runs        []string
	grants      []string // the bearer, "|", the body
	decided     []string
	me          string            // GET /v1/me body for the browser session's token
	mes         map[string]string // GET /v1/me body by any other token
	expired     map[string]bool   // tokens the kernel refuses
	refreshed   []string          // refresh tokens rotated
	svc         *svc.Service      // what a run dispatches to, as the kernel does
	keys        map[string]string // the chat key connected, by session token
	granted     map[string]map[string]bool
	failGrant   bool // POST /v1/grants refuses
	failRuns    bool // POST /v1/run cannot reach chat
	taskReads   int
	meReads     int                     // GET /v1/me
	taskReaders []string                // the token of each GET /v1/tasks
	refs        []string                // references resolved, GET /v1/actions?ref=
	names       map[string]client.Party // how this kernel names a principal by sys/resolve, where it differs from chat's kernel
	resolved    []string                // the principals a viewer asked this kernel to name
	// away is a chat on another kernel, chat@away: its runs are forwarded, and
	// it relays neither a refusal's status nor its reason.
	away         *svc.Service
	awayDown     bool              // chat@away fails every run
	failNext     map[string]int    // runs of an action of chat@away to fail, while the chat still answers others
	revokedThere map[string]bool   // grants the kernel holding chat revoked, as a contract change does, which this kernel still lists
	requesters   map[string]string // requester -> pending|approved|blocked
	procs        []string          // the viewer's open processes
	stale        map[string]bool   // actions whose cached contract moved: refused once, for a refresh
	regrants     []string          // selectors granted again from a kept key
	failStart    bool              // POST /v1/grants/start cannot reach the kernel holding the chat
}

// home is this kernel's chat.
const home = "chat@testnet"

// pr is the principal of address in these tests: an id derived from the
// handle, at a key derived from the kernel's name, which the test kernel names
// by address again (addressOf).
func pr(address string) string {
	h, k, _ := strings.Cut(address, "@")
	p := fmt.Sprintf("%08x-0000-4000-8000-000000000000", crc32.ChecksumIEEE([]byte(h))) + "@" + (k + strings.Repeat("K", client.KeyLen))[:client.KeyLen]
	addresses.LoadOrStore(p, address)
	return p
}

var addresses sync.Map

// names is the addresses of members, in order of address.
func names(members []string) string {
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = party(m).Address
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// party is how the test kernel names an address, of a user or an action, or a
// principal.
func party(a string) client.Party {
	if !svc.IsPrincipal(a) {
		handle, rest, _ := strings.Cut(a, "@")
		kernel, path, ok := strings.Cut(rest, "/")
		if ok {
			return client.Party{Address: a, Principal: pr(handle+"@"+kernel) + "/" + path}
		}
		return client.Party{Address: a, Principal: pr(a)}
	}
	addr, _ := addresses.Load(a)
	return client.Party{Address: addr.(string), Principal: a}
}

// served is a task as the kernel lists it: its parties named.
func served(t map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range t {
		out[k] = v
		if s, ok := v.(string); ok && (k == "required_caller" || k == "owner" || k == "requester" || k == "action") {
			out[k] = party(s)
		}
	}
	return out
}

// The test kernels' keys: this one's, testnet, and the one on which chat@away
// runs. A chat is identified by its kernel's key (farChat, homeChat), whatever
// the viewer's kernel calls it.
var (
	testKey, awayKey  = kernelKey("testnet"), kernelKey("away")
	homeChat, farChat = "chat@" + testKey, "chat@" + awayKey
)

// kernelKey is the key of the test kernel named kernel.
func kernelKey(kernel string) string {
	return (kernel + strings.Repeat("K", client.KeyLen))[:client.KeyLen]
}

// norm is a reference to a chat as the test kernel holds it, by its kernel's
// name: the kernel takes a name and a key alike.
func norm(ref string) string {
	return strings.NewReplacer(farChat, "chat@away", homeChat, "chat@testnet").Replace(ref)
}

// chatFor is the chat service an action address names, and whether it is held
// on another kernel.
func (f *fakeKernel) chatFor(action string) (*svc.Service, bool) {
	if strings.HasPrefix(action, "chat@away/") {
		return f.away, true
	}
	return f.svc, false
}

type fakeAction struct {
	ID, Name, Title, Desc, URL, Vis, Scheme string
	Active                                  bool
	In, Out                                 string
}

func (a *fakeAction) wire() string {
	in, out := a.In, a.Out
	if in == "" {
		in = "{}"
	}
	if out == "" {
		out = "{}"
	}
	scheme := ""
	if a.Scheme != "" {
		scheme = `,"auth_scheme":"` + a.Scheme + `","requires_grant":` + strconv.FormatBool(a.Scheme == "delegated_bearer")
	}
	return `{"id":"` + a.ID + `","name":"` + a.Name + `","title":"` + a.Title + `","kind":"http","active":` + strconv.FormatBool(a.Active) +
		`,"visibility":"` + a.Vis + `","description":` + strconv.Quote(a.Desc) + scheme + `,"http":{"method":"POST","url":"` + a.URL + `"},"input_schema":` + in + `,"output_schema":` + out + `}`
}

func newFakeKernel(seed ...fakeAction) *fakeKernel {
	f := &fakeKernel{acts: map[string]*fakeAction{}, updates: map[string][]string{}, me: `{"address":{"address":"ann@testnet","principal":"ann@testnet"},"connectors":[]}`,
		mes: map[string]string{}, expired: map[string]bool{}, keys: map[string]string{}, granted: map[string]map[string]bool{},
		requesters: map[string]string{}}
	for i := range seed {
		a := seed[i]
		f.acts[a.ID] = &a
	}
	mux := http.NewServeMux()
	ok := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }
	}
	mux.HandleFunc("GET /health", ok(`{"status":"ok","handle":"testnet","public_key":"`+testKey+`"}`))
	mux.HandleFunc("POST /v1/users", ok(`{}`))
	mux.HandleFunc("POST /v1/auth/authorize", ok(`{"redirect":"?code=c"}`))
	mux.HandleFunc("POST /v1/auth/token", ok(`{"access_token":"t","refresh_token":"r"}`))
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.meReads++
		switch {
		case f.expired[tok]:
			http.Error(w, `{"error":"token expired"}`, http.StatusUnauthorized)
		case f.mes[tok] != "":
			_, _ = w.Write([]byte(withGrants(f.mes[tok], f.granted[tok])))
		default:
			_, _ = w.Write([]byte(withGrants(f.me, f.granted[tok])))
		}
	})
	mux.HandleFunc("POST /v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.expired[in.RefreshToken] {
			http.Error(w, `{"error":"refresh token expired"}`, http.StatusUnauthorized)
			return
		}
		f.refreshed = append(f.refreshed, in.RefreshToken)
		_, _ = w.Write([]byte(`{"access_token":"NEW-` + in.RefreshToken + `","refresh_token":"NEXT-` + in.RefreshToken + `"}`))
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		if ref := norm(r.URL.Query().Get("ref")); ref != "" {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.refs = append(f.refs, ref)
			for _, a := range f.acts {
				if "chat@testnet/"+a.Name == ref || "chat@away/"+a.Name == ref {
					_, _ = w.Write([]byte("[" + a.wire() + "]"))
					return
				}
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Query().Get("offset") != "" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var parts []string
		for _, a := range f.acts {
			parts = append(parts, a.wire())
		}
		_, _ = w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
	})
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var in client.CreateActionInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seq++
		a := &fakeAction{ID: "new" + strconv.Itoa(f.seq), Name: in.Name, Title: in.Title, Desc: in.Description, URL: in.Source, Vis: "private", In: string(in.InputSchema), Out: string(in.OutputSchema), Scheme: schemeOf(in.Auth)}
		f.acts[a.ID] = a
		f.creates = append(f.creates, in)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(a.wire()))
	})
	mux.HandleFunc("PUT /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in client.UpdateActionInput
		_ = json.Unmarshal(b, &in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.updates[in.Target] = append(f.updates[in.Target], string(b))
		if a := f.acts[in.Target]; a != nil {
			if in.Visibility != nil {
				a.Vis = *in.Visibility
			}
			if in.Title != nil {
				a.Title = *in.Title
			}
			if in.Source != nil {
				a.Active = false
				a.URL, a.Desc, a.In, a.Out, a.Scheme = *in.Source, *in.Description, string(in.InputSchema), string(in.OutputSchema), schemeOf(in.Auth)
			}
			_, _ = w.Write([]byte("[" + a.wire() + "]"))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /v1/actions/enable", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Target string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.enables = append(f.enables, in.Target)
		if a := f.acts[in.Target]; a != nil {
			a.Active = true
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("DELETE /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletes = append(f.deletes, r.URL.Query().Get("target"))
		delete(f.acts, r.URL.Query().Get("target"))
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(client.CapabilityHeader) == "" {
			http.Error(w, `{"error":"a task is created through the capability"}`, http.StatusForbidden)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.Contains(in["required_caller"].(string), "@nowhere") {
			http.Error(w, `{"error":"kernel \"nowhere\" is not a known name or key"}`, http.StatusNotFound)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seq++
		in["id"] = "task" + strconv.Itoa(f.seq)
		in["status"] = "waiting"
		var by string // the capability names the caller and the chat creating the task
		in["owner"], by, _ = strings.Cut(r.Header.Get(client.CapabilityHeader), "#")
		in["requester"] = home
		if strings.HasPrefix(by, "chat@") {
			in["requester"] = by
		}
		f.tasks = append(f.tasks, in)
		b, _ := json.Marshal(served(in))
		w.WriteHeader(201)
		_, _ = w.Write(b)
	})
	// sys/resolve, composed by chat through its capability: an address or a
	// principal, named; a kernel nobody knows, not.
	mux.HandleFunc("POST /v1/call", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
			Args   struct {
				Address string `json:"address"`
			} `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get(client.CapabilityHeader) == "" || in.Action != client.Resolve || strings.Contains(in.Args.Address, "@nowhere") {
			http.Error(w, `{"code":"not_found","error":"remote user not found"}`, http.StatusNotFound)
			return
		}
		b, _ := json.Marshal(party(in.Args.Address))
		_, _ = w.Write([]byte(`{"result":` + string(b) + `,"tx_id":"tx"}`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.taskReads++
		f.taskReaders = append(f.taskReaders, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		open := []map[string]any{}
		for _, t := range f.tasks {
			if t["status"] == "waiting" {
				open = append(open, served(t))
			}
		}
		b, _ := json.Marshal(open)
		_, _ = w.Write(b)
	})
	// A completion runs the task's onward, with chat's bearer, as the kernel does.
	mux.HandleFunc("POST /v1/tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		var onward, args any
		for _, t := range f.tasks {
			if t["id"] == r.PathValue("id") {
				t["status"] = "done"
				onward, args = t["action"], t["partial_args"]
			}
		}
		if !strings.HasSuffix(onward.(string), "/accept") {
			f.completes = append(f.completes, r.PathValue("id"))
		}
		f.mu.Unlock()
		b, _ := json.Marshal(args)
		req := httptest.NewRequest(http.MethodPost, "/"+onward.(string), strings.NewReader(string(b)))
		s, away := f.chatFor(onward.(string))
		req.Header.Set("Authorization", "Bearer "+s.Secret)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		switch {
		case rec.Code/100 != 2 && away:
			http.Error(w, `{"code":"execution_failed","error":"remote call failed","meta":{"charge":"0"}}`, http.StatusInternalServerError)
		case rec.Code/100 != 2:
			http.Error(w, `{"code":"execution_failed","error":"upstream returned status `+strconv.Itoa(rec.Code)+`","meta":{"charge":"0"}}`, http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{"task_id":"` + r.PathValue("id") + `"}`))
		}
	})
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, t := range f.tasks {
			if t["id"] == r.PathValue("id") && t["status"] == "waiting" {
				t["status"] = "cancelled"
				f.cancels = append(f.cancels, r.PathValue("id"))
			}
		}
		_, _ = w.Write([]byte(`{"id":"` + r.PathValue("id") + `","status":"cancelled"}`))
	})
	mux.HandleFunc("DELETE /v1/grants", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		sel := norm(r.URL.Query().Get("selector"))
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ungrants = append(f.ungrants, sel)
		for a := range f.granted[tok] {
			if a == sel || strings.HasPrefix(a, sel+"/") {
				delete(f.granted[tok], a)
			}
		}
		if f.awayDown && sel == "chat@away" {
			http.Error(w, `{"code":"peer_unreachable","error":"peer unreachable"}`, http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/grants/plan", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.expired[tok] {
			http.Error(w, `{"error":"token expired"}`, http.StatusUnauthorized)
			return
		}
		var actions []client.GrantPlanAction
		for _, a := range f.delegated() {
			actions = append(actions, client.GrantPlanAction{Action: client.Party{Address: a}, Granted: f.granted[tok][a]})
		}
		b, _ := json.Marshal(client.GrantPlan{Groups: []client.GrantGroup{{Actions: actions}}})
		_, _ = w.Write(b)
	})
	// A run is the kernel's dispatch: the grant is checked, the input checked,
	// the action's endpoint called with the connected key and the capability,
	// and of a refusal only the status comes back.
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		b = []byte(norm(string(b)))
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var in struct {
			Action string          `json:"action"`
			Args   json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(b, &in)
		f.mu.Lock()
		needs := false
		for _, a := range f.delegated() {
			needs = needs || a == in.Action
		}
		if needs && (!f.granted[tok][in.Action] || f.revokedThere[in.Action]) {
			f.mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"grant_required","error":"grant required","meta":{"action":"` + in.Action + `"}}`))
			return
		}
		if f.stale[in.Action] {
			delete(f.stale, in.Action)
			f.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"execution_failed","error":"the provider changed this action","meta":{"charge":"0","retry":"refresh"}}`))
			return
		}
		// sys/resolve, run by a viewer: a principal as this kernel names it; a
		// kernel nobody knows, not.
		if in.Action == client.Resolve {
			var a struct{ Address string }
			_ = json.Unmarshal(in.Args, &a)
			f.resolved = append(f.resolved, a.Address)
			p, ok := f.names[a.Address]
			f.mu.Unlock()
			if !ok {
				p = party(a.Address)
			}
			if strings.Contains(p.Address, "@nowhere") {
				http.Error(w, `{"code":"not_found","error":"remote user not found"}`, http.StatusNotFound)
				return
			}
			out, _ := json.Marshal(p)
			_, _ = w.Write([]byte(`{"result":` + string(out) + `,"tx_id":"tx"}`))
			return
		}
		f.runs = append(f.runs, string(b))
		key := f.keys[tok+" "+serviceOf(in.Action)]
		failing := f.failRuns
		chat, away := f.chatFor(in.Action)
		awayDown := f.awayDown || f.failNext[in.Action] > 0
		if f.failNext[in.Action] > 0 {
			f.failNext[in.Action]--
		}
		f.mu.Unlock()
		remoteFailed := func() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"execution_failed","error":"remote call failed","meta":{"charge":"0","tx_id":"tx"}}`))
		}
		if away && awayDown {
			remoteFailed()
			return
		}
		if failing {
			http.Error(w, `{"code":"execution_failed","error":"upstream unreachable","meta":{"charge":"0","tx_id":"tx"}}`, http.StatusBadGateway)
			return
		}
		if strings.Contains(string(b), "/msg/post") && !strings.Contains(string(b), `"message"`) {
			http.Error(w, `{"code":"invalid_input","error":"message: required field missing"}`, http.StatusUnprocessableEntity)
			return
		}
		if strings.Contains(string(b), "refuse me") {
			http.Error(w, `{"code":"execution_failed","error":"upstream returned status 409","meta":{"charge":"0","tx_id":"tx"}}`, http.StatusInternalServerError)
			return
		}
		member, _ := chat.Store.Member(key)
		req := httptest.NewRequest(http.MethodPost, "/"+in.Action, strings.NewReader(string(in.Args)))
		req.Header.Set(client.CallbackHeader, f.srv.URL)
		req.Header.Set(client.CapabilityHeader, member+"#"+serviceOf(in.Action))
		caller := pr(f.addressOf(tok))
		req.Header.Set(client.CallerHeader, caller)
		req.Header.Set(client.OwnerHeader, caller)
		if needs {
			req.Header.Set(svc.CallerKey, key)
		}
		if strings.HasSuffix(in.Action, "/"+joinName) { // join carries chat's own bearer
			req.Header.Set("Authorization", "Bearer "+chat.Secret)
		}
		rec := httptest.NewRecorder()
		chat.ServeHTTP(rec, req)
		if rec.Code/100 != 2 && away {
			remoteFailed()
			return
		}
		if rec.Code/100 != 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"execution_failed","error":"upstream returned status ` + strconv.Itoa(rec.Code) + `","meta":{"charge":"0","tx_id":"tx"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":` + rec.Body.String() + `,"tx_id":"tx"}`))
	})
	mux.HandleFunc("POST /v1/grants", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		b = []byte(norm(string(b)))
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var in struct {
			Selector string `json:"selector"`
			Token    string `json:"token"`
		}
		_ = json.Unmarshal(b, &in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failGrant {
			http.Error(w, `{"code":"invalid_state","error":"no"}`, http.StatusConflict)
			return
		}
		f.grants = append(f.grants, tok+"|"+string(b))
		for a := range f.revokedThere {
			if strings.HasPrefix(a, in.Selector+"/") {
				delete(f.revokedThere, a)
			}
		}
		f.keys[tok+" "+serviceOf(in.Selector)] = in.Token
		if f.granted[tok] == nil {
			f.granted[tok] = map[string]bool{}
		}
		for _, a := range f.delegated() {
			if a == in.Selector || strings.HasPrefix(a, in.Selector+"/") {
				f.granted[tok][a] = true
			}
		}
		_, _ = w.Write([]byte(`{"actions":["chat@testnet/msg/post"]}`))
	})
	// A consent started for a selector whose key the token connected before is
	// granted at once from that key, as the kernel keeps a connection when it
	// deletes the grants on it.
	mux.HandleFunc("POST /v1/grants/start", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var in struct {
			Selector string `json:"selector"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		in.Selector = norm(in.Selector)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failStart {
			http.Error(w, `{"code":"peer_unreachable","error":"peer unreachable"}`, http.StatusBadGateway)
			return
		}
		if f.failGrant || f.keys[tok+" "+serviceOf(in.Selector)] == "" {
			http.Error(w, `{"code":"invalid_input","error":"action does not use delegated OAuth; supply its token via POST /v1/grants"}`, http.StatusUnprocessableEntity)
			return
		}
		f.regrants = append(f.regrants, in.Selector)
		for a := range f.revokedThere {
			if strings.HasPrefix(a, in.Selector+"/") {
				delete(f.revokedThere, a)
			}
		}
		for _, a := range f.delegated() {
			if a == in.Selector || strings.HasPrefix(a, in.Selector+"/") {
				f.granted[tok][a] = true
			}
		}
		_, _ = w.Write([]byte(`{"status":"granted"}`))
	})
	mux.HandleFunc("POST /v1/me/requesters/{address}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		q := r.PathValue("address") // by name, unless the kernel lists it by key
		if _, ok := f.requesters[q]; !ok {
			q = norm(q)
		}
		f.decided = append(f.decided, q+" "+r.PathValue("verb"))
		f.requesters[q] = map[string]string{"approve": "approved", "revoke": "pending", "block": "blocked"}[r.PathValue("verb")]
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"requester":"` + r.PathValue("address") + `","status":"approved"}`))
	})
	mux.HandleFunc("GET /v1/me/requesters", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []client.Requester{}
		if r.URL.Query().Get("offset") == "" {
			for q, st := range f.requesters {
				p := party(q)
				if kernelOf(q) == awayKey {
					p.Nickname = "away"
				}
				out = append(out, client.Requester{Requester: p, Status: st})
			}
		}
		b, _ := json.Marshal(out)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []client.Process{}
		if r.URL.Query().Get("offset") == "" {
			for _, id := range f.procs {
				out = append(out, client.Process{ID: id, Status: "open"})
			}
		}
		b, _ := json.Marshal(out)
		_, _ = w.Write(b)
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeKernel) close() { f.srv.Close() }

// addressOf is the address of the session token stands for, as GET /v1/me
// names it.
func (f *fakeKernel) addressOf(tok string) string {
	f.mu.Lock()
	body := f.me
	if f.mes[tok] != "" {
		body = f.mes[tok]
	}
	f.mu.Unlock()
	var me struct {
		Address client.Party `json:"address"`
	}
	_ = json.Unmarshal([]byte(body), &me)
	return me.Address.Address
}

// withGrants is a GET /v1/me body with its connectors those of the grants a
// token holds, one per action, as the kernel lists them.
func withGrants(body string, granted map[string]bool) string {
	var me map[string]any
	_ = json.Unmarshal([]byte(body), &me)
	if a, ok := me["address"].(map[string]any); ok {
		a["principal"] = pr(a["address"].(string))
	}
	var grants []map[string]any
	for a, ok := range granted {
		if ok {
			grants = append(grants, map[string]any{"action": party(a)})
		}
	}
	if len(grants) > 0 {
		me["connectors"] = []map[string]any{{"directory": map[string]string{"address": "chat", "principal": "chat"}, "actions": grants}}
	}
	b, _ := json.Marshal(me)
	return string(b)
}

// seated is a room whose members all accepted their invitations, each named
// as the test kernel names it.
func seated(s *svc.Store, creator, name string, members []string) (svc.Room, error) {
	for _, m := range append([]string{creator}, members...) {
		if err := s.Named(party(m)); err != nil {
			return svc.Room{}, err
		}
	}
	r, err := s.CreateRoom(creator, name, members)
	if err != nil {
		return r, err
	}
	for _, m := range r.Invited {
		if err := s.Join(r.ID, m); err != nil {
			return r, err
		}
	}
	r.Members, r.Invited = append(r.Members, r.Invited...), nil
	return r, nil
}

// connect is ann's Connect on a chat elsewhere, from the rail.
func connect(t *testing.T, c *Chat, sm *scs.SessionManager, service string) *httptest.ResponseRecorder {
	t.Helper()
	return chatVerb(c, sm, service, "connect")
}

func chatVerb(c *Chat, sm *scs.SessionManager, service, verb string) *httptest.ResponseRecorder {
	return chatVerbFrom(c, sm, service, verb, "")
}

// chatVerbFrom is a chat's command sent with script from the page at from.
func chatVerbFrom(c *Chat, sm *scs.SessionManager, service, verb, from string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/chat/chats/"+service+"/"+verb, nil)
	req.SetPathValue("chat", service)
	req.SetPathValue("verb", verb)
	if from != "" {
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Current-URL", "http://ui.test"+from)
	}
	return as(c, sm, c.postChat, req)
}

// invitation is ann's Accept or Decline of an invitation to room, sent with
// script from the page at from when there is one.
func invitation(c *Chat, sm *scs.SessionManager, room, verb, from string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/chat/invites/"+room+"/"+verb, nil)
	req.SetPathValue("room", room)
	req.SetPathValue("verb", verb)
	if from != "" {
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Current-URL", "http://ui.test"+from)
	}
	return as(c, sm, c.postInvite, req)
}

// tasksReadBy is how many times a token listed its tasks.
func (f *fakeKernel) tasksReadBy(tok string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.taskReaders {
		if t == tok {
			n++
		}
	}
	return n
}

// goesTo is where a reply sends the browser: HX-Redirect under htmx, else
// Location.
func goesTo(rec *httptest.ResponseRecorder) string {
	if to := rec.Header().Get("HX-Redirect"); to != "" {
		return to
	}
	return rec.Header().Get("Location")
}

// openTaskIDs is the ids of the tasks still waiting.
func (f *fakeKernel) openTaskIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, t := range f.tasks {
		if t["status"] == "waiting" {
			out = append(out, t["id"].(string))
		}
	}
	return out
}

// delegated is every action a member runs with its key, by address: home's,
// and chat@away's when there is one.
func (f *fakeKernel) delegated() []string {
	var out []string
	for _, a := range f.acts {
		if a.Scheme == "delegated_bearer" {
			out = append(out, "chat@testnet/"+a.Name)
			if f.away != nil {
				out = append(out, "chat@away/"+a.Name)
			}
		}
	}
	return out
}

// serviceOf is the chat an action address or selector names.
func serviceOf(ref string) string {
	service, _, _ := strings.Cut(ref, "/")
	return service
}

// ran is the arguments of every dispatched run of one of chat's actions.
func (f *fakeKernel) ran(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, run := range f.runs {
		var in struct {
			Action string          `json:"action"`
			Args   json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal([]byte(run), &in)
		if in.Action == "chat@testnet/"+name {
			out = append(out, string(in.Args))
		}
	}
	return out
}

func schemeOf(auth json.RawMessage) string {
	var a struct{ Scheme string }
	_ = json.Unmarshal(auth, &a)
	return a.Scheme
}

func (f *fakeKernel) taskBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.tasks...)
}

// testChat is a chat with an in-memory store and a fake kernel, signed in
// as ann@testnet for the browser routes.
func testChat(t *testing.T, f *fakeKernel) (*Chat, *scs.SessionManager) {
	t.Helper()
	c := New()
	root := t.TempDir()
	cl := client.New(f.srv.URL, nil)
	sm := scs.New()
	c.st = chatState{Password: "pw"}
	prog := &fakePrograms{}
	t.Cleanup(prog.close)
	c.register = prog.register
	store, err := svc.Open("", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.svc = &svc.Service{Store: store, Secret: "S", Kernel: cl}
	st := &svc.State{Secret: "S", AuthRev: map[string]string{}}
	if err := st.Register(context.Background(), cl, "t", "chat@testnet", "http://chat.test"); err != nil {
		t.Fatal(err)
	}
	viewers := struct {
		sync.Mutex
		m map[context.Context]client.User
	}{m: map[context.Context]client.User{}}
	c.deps = apps.Deps{
		Address:    testAddress,
		KeyAddress: func(h string) string { return h + "@" + testKey },
		Label:      client.Party.Label,
		Client:     cl,
		Sessions:   session.NewManager(sm, cl),
		Home:       filepath.Join(root, "ui"),
		Programs:   prog,
		Me:         func(*http.Request) string { return "ann@testnet" },
		Viewer: func(r *http.Request) (u client.User, err error) { // read once per request, as the server's is
			viewers.Lock()
			defer viewers.Unlock()
			if u, ok := viewers.m[r.Context()]; ok {
				return u, nil
			}
			err = c.deps.Sessions.Do(r.Context(), func(tok string) (e error) { u, e = cl.Me(r.Context(), tok); return e })
			if err == nil {
				viewers.m[r.Context()] = u
			}
			return u, err
		},
		CSRFField:         func(*http.Request) template.HTML { return "" },
		IsHTMX:            func(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" },
		HandleErr:         func(http.ResponseWriter, *http.Request, error) bool { return false },
		HandleErrFragment: func(http.ResponseWriter, *http.Request, error) bool { return false },
		RenderPage: func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
			w.WriteHeader(status)
		},
		RenderFragment: func(w http.ResponseWriter, status int, name string, data any) error {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, name)
			return nil
		},
		Fragment: func(name string, data any) (template.HTML, error) { return template.HTML(name), nil },
		Redirect: func(w http.ResponseWriter, r *http.Request, to string) {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", to)
				return
			}
			http.Redirect(w, r, to, http.StatusSeeOther)
		},
		FlashRedirect: func(w http.ResponseWriter, r *http.Request, to, kind, msg string) {
			w.Header().Set("X-Flash", kind+": "+msg)
			w.Header().Set("X-Flash-To", to)
			http.Redirect(w, r, to, http.StatusSeeOther)
		},
		FormArgs: func(r *http.Request) (json.RawMessage, error) {
			raw := r.FormValue("raw_args")
			if !json.Valid([]byte(raw)) {
				return nil, errors.New("invalid JSON")
			}
			return json.RawMessage(raw), nil
		},
		LoadAppState: func(string) (json.RawMessage, error) { return nil, nil },
		SaveAppState: func(string, json.RawMessage) error { return nil },
	}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, "pw")
	return c, sm
}

// fakePrograms stands in for the supervisor and the juice-chat program: a
// declared server holds its lock, as the real one does.
type fakePrograms struct {
	calls []string
	fail  error
	prog  map[string]supervisor.Program
	lock  *os.File
}

func (f *fakePrograms) Set(name string, p supervisor.Program) error {
	f.calls = append(f.calls, "set "+name)
	if f.prog == nil {
		f.prog = map[string]supervisor.Program{}
	}
	f.prog[name] = p
	if err := os.MkdirAll(filepath.Dir(p.Lock), 0o700); err != nil {
		return err
	}
	var err error
	if f.lock, err = os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		return err
	}
	return syscall.Flock(int(f.lock.Fd()), syscall.LOCK_EX)
}

func (f *fakePrograms) close() {
	if f.lock != nil {
		f.lock.Close()
	}
}

func (f *fakePrograms) Remove(string) error { return nil }
func (f *fakePrograms) Start(string) error  { return nil }
func (f *fakePrograms) Stop(string) error   { return nil }
func (f *fakePrograms) Status(name string) (supervisor.Status, bool) {
	_, ok := f.prog[name]
	return supervisor.Status{Running: ok}, ok
}

func (f *fakePrograms) register(login string) error {
	f.calls = append(f.calls, "register "+login)
	return f.fail
}

// sessions is each session manager's cookie, so requests in a test share one
// session, as a browser's do.
var sessions sync.Map

// inSession serves req inside the session signed in as ann, signing in on its
// first request.
func inSession(c *Chat, sm *scs.SessionManager, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	if ck, ok := sessions.Load(sm); ok {
		req.AddCookie(ck.(*http.Cookie))
	}
	rec := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.deps.Sessions.Tokens(r.Context()); !ok {
			_ = c.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "ann@testnet", session.Pin{})
		}
		h(w, r)
	})).ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		sessions.Store(sm, ck)
	}
	return rec
}

// as runs a browser handler in ann's session.
func as(c *Chat, sm *scs.SessionManager, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	return inSession(c, sm, h, req)
}

// dispatch posts to an action the way the kernel does, with the capability
// naming the caller and, for a member action, the member's key.
func (f *fakeKernel) dispatch(path, key string, body any) *httptest.ResponseRecorder {
	return f.dispatchAt(f.svc, home, path, key, body)
}

// dispatchAt is dispatch to the chat s, at address service.
func (f *fakeKernel) dispatchAt(s *svc.Service, service, path, key string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	member, _ := s.Store.Member(key)
	req := httptest.NewRequest(http.MethodPost, "/"+service+path, strings.NewReader(string(b)))
	req.Header.Set(client.CallbackHeader, f.srv.URL)
	req.Header.Set(client.CapabilityHeader, member+"#"+service)
	req.Header.Set(svc.CallerKey, key)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// agentHome makes a local agent: its home under $JUICE_HOME/agents and its
// session in client/credentials, as the agents app leaves them.
func agentHome(t *testing.T, c *Chat, login, token string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(c.root(), "agents", login), 0o700); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(c.root(), "client", "credentials", login+".json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"token": token, "refresh_token": "R-" + token, "principal_id": "P"})
	if err := os.WriteFile(cred, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return cred
}

// keyOf is the key a join connected with token.
func (f *fakeKernel) keyOf(token string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.grants {
		tok, body, _ := strings.Cut(g, "|")
		if tok == token {
			var in struct {
				Token string `json:"token"`
			}
			_ = json.Unmarshal([]byte(body), &in)
			return in.Token
		}
	}
	return ""
}

func (f *fakeKernel) joined(t *testing.T, member string) string {
	t.Helper()
	return joinedAt(t, f.svc, member)
}

// joinedAt is a key of member, by address, at the chat s.
func joinedAt(t *testing.T, s *svc.Service, member string) string {
	t.Helper()
	key, err := s.Store.IssueKey(pr(member))
	if err == nil {
		err = s.Store.Named(party(member))
	}
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// Chat is the workspace, not a destination: it contributes no menu entry, it
// is the core's conversations (the rail, the landing, a room's context).
func TestChatIsTheWorkspace(t *testing.T) {
	c := New()
	if n := c.Nav("ann@testnet", true); n != nil {
		t.Fatalf("Nav = %v", n)
	}
	var conv apps.Conversations = c
	if conv.Landing() != "/chat" {
		t.Errorf("Landing = %q", conv.Landing())
	}
	if c.Slug() != "chat" {
		t.Errorf("Slug = %q", c.Slug())
	}
	for _, f := range []string{"_rail.html", "room.html", "_entries.html", "landing.html"} {
		if _, err := c.Templates().Open("templates/" + f); err != nil {
			t.Errorf("%s not embedded", f)
		}
	}
}

// Start provisions the password once and keeps it.
func TestStartProvisionsOnce(t *testing.T) {
	store := map[string]json.RawMessage{}
	deps := apps.Deps{
		Address: testAddress, Client: client.New("http://127.0.0.1:0", nil),
		LoadAppState: func(slug string) (json.RawMessage, error) { return store[slug], nil },
		SaveAppState: func(slug string, v json.RawMessage) error { store[slug] = v; return nil },
	}
	first := chatState{}
	for i := 0; i < 2; i++ {
		c := New()
		c.deps = deps
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		_ = c.Stop(context.Background())
		var st chatState
		_ = json.Unmarshal(store["chat"], &st)
		if st.Password == "" {
			t.Fatalf("not provisioned: %+v", st)
		}
		if i == 0 {
			first = st
		} else if st != first {
			t.Errorf("state changed on restart: %+v vs %+v", st, first)
		}
	}
}

// Setup records the provider's session where any program finds a login, restarts
// the server, has it register, and joins the agents; a registration that fails is
// setup's failure, so the sign-in loop retries it.
func TestSetupBringsTheServerUp(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	f.me = `{"id":"P1","address":{"address":"chat@testnet","principal":"chat@testnet"}}`
	if err := c.setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := c.deps.Programs.(*fakePrograms)
	if got := strings.Join(l.calls, ","); got != "set chat,register chat@testnet" {
		t.Errorf("calls = %s", got)
	}
	p := l.prog["chat"]
	if strings.Join(p.Command, " ") != "juice-chat serve" || p.Env["JUICE_HOME"] != c.root() ||
		p.Lock != filepath.Join(c.root(), "services", "chat", "serve.lock") {
		t.Errorf("program = %+v", p)
	}
	b, err := os.ReadFile(filepath.Join(c.root(), "client", "credentials", "chat@testnet.json"))
	if err != nil || !strings.Contains(string(b), `"principal_id":"P1"`) {
		t.Errorf("credential = %s %v", b, err)
	}
	if e, err := client.KernelEndpoint(filepath.Join(c.root(), "client"), "testnet"); err != nil || e != f.srv.URL {
		t.Errorf("kernel record = %q %v", e, err)
	}
	l.fail = errors.New("refused")
	if err := c.setup(context.Background()); err == nil {
		t.Error("a failed registration is setup's failure")
	}
	if got := strings.Count(strings.Join(l.calls, ","), "set chat"); got != 1 {
		t.Errorf("the program is declared once: %v", l.calls)
	}
	c.deps.Home = ""
	if err := c.setup(context.Background()); err == nil {
		t.Error("no home, no server")
	}
}

// The landing is the viewer's conversation with the assistant, made once by
// the direct action. The viewer is joined with its session and the assistant
// with its own, each through the kernel alone: approving chat, running register,
// completing the verification task, connecting the key at chat's root. A joined
// principal is not joined again. Without the assistant's home the landing is the
// rooms.
func TestLandingJoinsAndOpensTheAssistant(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	land := func() *httptest.ResponseRecorder {
		return as(c, sm, c.getLanding, httptest.NewRequest(http.MethodGet, "/chat", nil))
	}
	if rec := land(); rec.Code != 200 {
		t.Fatalf("no assistant and no room: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if m, ok := f.svc.Store.Member(f.keyOf("t")); !ok || m != pr("ann@testnet") {
		t.Fatalf("the viewer is joined: %q %v %v", m, ok, f.grants)
	}
	if !strings.Contains(f.grants[0], `"selector":"chat@testnet"`) {
		t.Errorf("a join connects at chat's root: %s", f.grants[0])
	}
	if len(f.ran(joinName)) != 1 || f.ran(joinName)[0] != `{}` {
		t.Errorf("a join runs join: %v", f.ran(joinName))
	}
	agentHome(t, c, "assistant@testnet", "AT")
	f.mes["AT"] = `{"address":{"address":"assistant@testnet","principal":"assistant@testnet"},"connectors":[]}`
	rec := land()
	to := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.HasPrefix(to, "/chat/room/") || rec.Header().Get("X-Flash") != "" {
		t.Fatalf("landing: %d %s %s", rec.Code, to, rec.Header().Get("X-Flash"))
	}
	if m, ok := f.svc.Store.Member(f.keyOf("AT")); !ok || m != pr("assistant@testnet") {
		t.Fatalf("the assistant is joined with its own session: %q %v", m, ok)
	}
	if len(f.decided) != 2 || f.decided[1] != "chat@testnet approve" || len(f.completes) != 0 {
		t.Errorf("join = decided %v completes %v", f.decided, f.completes)
	}
	if got := f.ran(roomCreateName); len(got) != 1 || !strings.Contains(got[0], `"members":["assistant@testnet"]`) || !strings.Contains(got[0], `"name":"assistant"`) {
		t.Errorf("the first visit starts the room: %v", got)
	}
	rooms, _ := f.svc.Store.Rooms(pr("ann@testnet"))
	if len(rooms) != 1 || rooms[0].Name != "assistant" || names(rooms[0].Members) != "ann@testnet,assistant@testnet" || to != "/chat/room/"+addr(rooms[0].ID) {
		t.Fatalf("rooms = %+v", rooms)
	}
	agentReads := f.tasksReadBy("AT")
	if rec := land(); rec.Header().Get("Location") != to || len(f.grants) != 2 || len(f.ran(roomCreateName)) != 1 {
		t.Errorf("a second landing joins nobody and opens the same room: %s %v %v", rec.Header().Get("Location"), f.grants, f.ran(roomCreateName))
	}
	if n := f.tasksReadBy("AT") - agentReads; n != 0 {
		t.Errorf("a landing with the assistant joined reads none of its tasks: %d", n)
	}
	// A room with a third member is not the conversation with the assistant.
	if _, err := seated(f.svc.Store, pr("ann@testnet"), "three", []string{pr("assistant@testnet"), pr("bob@testnet")}); err != nil {
		t.Fatal(err)
	}
	if rec := land(); rec.Header().Get("Location") != to {
		t.Errorf("the landing opened %s, not %s", rec.Header().Get("Location"), to)
	}
	// A join that cannot be made is said on the room the landing opens.
	f.expired["AT"], f.expired["R-AT"] = true, true
	if rec := land(); rec.Header().Get("X-Flash-To") != to || !strings.Contains(rec.Header().Get("X-Flash"), "assistant@testnet is not connected to chat") {
		t.Errorf("refused join: %s %s", rec.Header().Get("X-Flash-To"), rec.Header().Get("X-Flash"))
	}
}

// An agent's expired session is refreshed in its credential file, under the
// file's lock, and the join is made with the new token; setup joins every
// local agent of this kernel.
func TestJoinAgentRefreshesItsSession(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	cred := agentHome(t, c, "helper@testnet", "OLD")
	agentHome(t, c, "remy@elsewhere", "X")
	f.expired["OLD"] = true
	f.mes["NEW-R-OLD"] = `{"address":{"address":"helper@testnet","principal":"helper@testnet"},"connectors":[]}`
	if err := c.setup(context.Background()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var saved map[string]string
	b, _ := os.ReadFile(cred)
	_ = json.Unmarshal(b, &saved)
	if saved["token"] != "NEW-R-OLD" || saved["refresh_token"] != "NEXT-R-OLD" || saved["principal_id"] != "P" {
		t.Fatalf("credential = %v", saved)
	}
	if m, ok := f.svc.Store.Member(f.keyOf("NEW-R-OLD")); !ok || m != pr("helper@testnet") {
		t.Fatalf("helper joined with the refreshed token: %q %v", m, ok)
	}
	if f.keyOf("X") != "" {
		t.Error("an agent of another kernel is not joined here")
	}
	if c.isLocalAgent("../helper@testnet") || c.isLocalAgent("helper@testnet/x") || !c.isLocalAgent("helper@testnet") {
		t.Error("a local agent is a home under agents/ named by a login here")
	}
}

// The composer is the post action's form: what it carries runs msg/post as
// the viewer with the room from the path, never from the form; a refusal,
// the kernel's or chat's, is a 422 beside the composer, so the draft stays;
// an htmx submit appends the tail and no-JS reloads the room.
func TestPostMessageRunsThePostForm(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	r, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	shown := "0"
	send := func(raw string, htmx bool) *httptest.ResponseRecorder {
		form := url.Values{"raw_args": {raw}, "after": {shown}}
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(r.ID)+"/message", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", addr(r.ID))
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		return as(c, sm, c.postMessage, req)
	}
	if rec := send(`{"message":"hello there"}`, true); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "_chat_tick") {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	// The answer starts past what the page shows.
	var after []int64
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(entriesView); ok {
			for _, e := range v.Entries {
				after = append(after, e.Seq)
			}
		}
		return template.HTML(name), nil
	}
	shown = "1"
	if rec := send(`{"message":"elsewhere","thread_id":"other-room-1"}`, true); rec.Code != 200 {
		t.Fatalf("sealed: %d", rec.Code)
	}
	if len(after) != 1 || after[0] != 2 {
		t.Errorf("the answer repeats the room: %v", after)
	}
	for _, refused := range []string{`{}`, `{"message":"refuse me"}`, `"hi"`, `{bad`} {
		if rec := send(refused, true); rec.Code != http.StatusUnprocessableEntity || rec.Body.String() != "_chat_refused" {
			t.Errorf("%s must be refused beside the composer: %d %s", refused, rec.Code, rec.Body)
		}
	}
	if rec := send(`{"message":"bye"}`, false); rec.Code != 303 || rec.Header().Get("Location") != "/chat/room/"+addr(r.ID) {
		t.Fatalf("no-JS: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	args := f.ran(postName)
	room := `"thread_id":"` + r.ID + `"`
	want := []string{`{"message":"hello there",` + room + `}`, `{"message":"elsewhere",` + room + `}`, `{` + room + `}`, `{"message":"refuse me",` + room + `}`, `{"message":"bye",` + room + `}`}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Errorf("runs =\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
	}
}

// The room page's composer is the post action's form drawn as a bar: the
// schema's own message control under its own name and type hint, has_fields,
// no label, no switch, no sealed view (the room is the page's address); [/],
// the arrow that sends, the balance beneath; [@] only where there is someone
// to choose among. Every entry is the view of its value.
func TestRoomRendersTheComposerBar(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		if err := rnd.Render(w, status, page, server.Page{Title: title, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	page := func(id string) string {
		req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(id), nil)
		req.SetPathValue("id", addr(id))
		return as(c, sm, c.getRoom, req).Body.String()
	}
	pair, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", []string{pr("bob@testnet")})
	if _, _, err := f.svc.Store.Post(pair.ID, pr("ann@testnet"), json.RawMessage(`"hello"`)); err != nil {
		t.Fatal(err)
	}
	got := page(pair.ID)
	for _, want := range []string{`<form class="composer"`, `data-composer`, `<div class="composer-bar">`, `name="has_fields" value="1"`,
		`name="t_/message" value="string"`, `name="f_/message"`, `<label class="sr-only" for="f_/message">message</label>`,
		`data-launcher`, `#slash`, `class="btn-send" data-send aria-label="Send"`, `#arrow-up`, `id="balance"`, `name="watch" value="balance::0"`,
		`id="launcher-data"`, `"room":"` + addr(pair.ID) + `"`, `"members":["bob@testnet"]`,
		`class="view" data-json`, `<aside class="panel" id="panel"`, `id="chat-working"`} {
		if !strings.Contains(got, want) {
			t.Errorf("room page lacks %s", want)
		}
	}
	bar := got[strings.Index(got, `<form class="composer"`):]
	bar = bar[:strings.Index(bar, "</form>")]
	for _, gone := range []string{"data-json-switch", `class="view locked"`, `name="raw_args"`, "f_/thread_id", "data-address", "<label>message"} {
		if strings.Contains(bar, gone) {
			t.Errorf("the bar carries %s", gone)
		}
	}
	trio, _ := seated(f.svc.Store, pr("ann@testnet"), "trio", []string{pr("bob@testnet"), pr("cy@testnet")})
	if !strings.Contains(page(trio.ID), `data-address aria-label="Address a member"`) {
		t.Error("a room of three offers [@]")
	}
}

// tick is one heartbeat of a watch in ann's session, in a visible tab.
func tick(c *Chat, sm *scs.SessionManager, key string, st watchState) (out template.HTML, err error) {
	if key != railKey {
		key = addr(key)
	}
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) { out, err = c.watch(r, key, st.String()) }, httptest.NewRequest(http.MethodGet, "/tick", nil))
	return out, err
}

// open opens a room in ann's session and answers the state its watch rendered.
func open(t *testing.T, c *Chat, sm *scs.SessionManager, id string) watchState {
	t.Helper()
	var view roomView
	render := c.deps.RenderPage
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		view = data.(roomView)
	}
	defer func() { c.deps.RenderPage = render }()
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(id), nil)
	req.SetPathValue("id", addr(id))
	as(c, sm, c.getRoom, req)
	return view.Watch.State
}

// The heartbeat runs nothing while nothing changed, one read when a delivery
// arrives or is answered, and it settles the viewer's deliveries for a room in
// view, as opening the room does; the room is then seen.
func TestTheHeartbeatRunsOnlyOnAChange(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(watchView); ok && name == "_chat_watch" {
			return template.HTML(name + "(" + v.State.String() + ")"), nil
		}
		if v, ok := data.(entriesView); ok && name == "_chat_tick" {
			return template.HTML(name + "(" + v.Watch.State.String() + ")"), nil
		}
		return template.HTML(name), nil
	}
	f.requesters[home] = "approved" // ann joined chat, so the kernel shows her its deliveries
	room, _ := seated(f.svc.Store, pr("bob@testnet"), "cafe", []string{pr("ann@testnet")})
	bob := f.joined(t, "bob@testnet")
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	st := open(t, c, sm, room.ID)
	if len(f.completes) != 1 || len(f.ran(roomReadName)) != 1 {
		t.Fatalf("opening settles ann's delivery and reads once: %v %v", f.completes, f.ran(roomReadName))
	}
	reads := func() int { return len(f.ran(roomReadName)) }
	for range 10 {
		if h, err := tick(c, sm, room.ID, st); h != "" || err != nil {
			t.Fatalf("nothing changed answers nothing: %s %v", h, err)
		}
	}
	if reads() != 1 {
		t.Fatalf("ten quiet ticks ran %d reads", reads()-1)
	}
	// Bob posts: ann's page settles the delivery and reads once.
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "again"})
	h, err := tick(c, sm, room.ID, st)
	if err != nil || !strings.HasPrefix(string(h), "_chat_tick") || reads() != 2 || len(f.completes) != 2 {
		t.Fatalf("a delivery: %s %v, %d reads, %v", h, err, reads(), f.completes)
	}
	if rooms, _ := f.svc.Store.Rooms(pr("ann@testnet")); rooms[0].Unread {
		t.Error("a watched visible room stays unread")
	}
	st = parseWatch(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(string(h), "_rail"), "_chat_tick("), ")"))
	if st.After != 2 {
		t.Fatalf("the cursor advanced: %+v", st)
	}
	if h, _ := tick(c, sm, room.ID, st); h != "" || reads() != 2 {
		t.Errorf("once shown, quiet again: %s", h)
	}
	// A delivery ann owns is answered: the task leaves the list, and her page reads.
	ann := f.keyOf("t")
	f.dispatch("/msg/post", ann, map[string]any{"thread_id": room.ID, "message": "bob@testnet well?"})
	before := open(t, c, sm, room.ID)
	var owned string
	for _, tk := range f.taskBodies() {
		if tk["required_caller"] == pr("bob@testnet") && tk["status"] == "waiting" {
			owned = tk["id"].(string)
		}
	}
	if owned == "" {
		t.Fatal("no delivery to bob")
	}
	if _, err := client.New(f.srv.URL, nil).CompleteTask(context.Background(), "b", owned, nil); err != nil {
		t.Fatal(err)
	}
	n := reads()
	if h, _ := tick(c, sm, room.ID, before); !strings.HasPrefix(string(h), "_chat_tick") || reads() != n+1 {
		t.Errorf("an answered delivery: %s, %d reads", h, reads()-n)
	}
}

// A tab read while hidden sees the room and leaves its delivery; once visible it
// reads once more, which settles the delivery and marks the room seen.
func TestAHiddenTabSettlesOnceVisible(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	room, _ := seated(f.svc.Store, pr("bob@testnet"), "cafe", []string{pr("ann@testnet")})
	bob := f.joined(t, "bob@testnet")
	st := open(t, c, sm, room.ID)
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	var state watchState
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(entriesView); ok {
			state = v.Watch.State
		}
		return template.HTML(name), nil
	}
	hidden := httptest.NewRequest(http.MethodGet, "/tick", nil)
	hidden.Header.Set("X-Visible", "0")
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) { _, _ = c.watch(r, addr(room.ID), st.String()) }, hidden)
	if len(f.completes) != 0 || state.After != 1 {
		t.Fatalf("hidden: %v %+v", f.completes, state)
	}
	if h, _ := tick(c, sm, room.ID, state); !strings.HasPrefix(string(h), "_chat_tick") || len(f.completes) != 1 {
		t.Errorf("visible: %q %v", h, f.completes)
	}
	if rooms, _ := f.svc.Store.Rooms(pr("ann@testnet")); rooms[0].Unread {
		t.Error("a room seen stays unread")
	}
}

// A delivery that arrives and is settled by the person's other tab between two
// ticks of a page leaves the open set as it was. The count of what this UI changed
// moves all the same, so the page still reads.
func TestAChangeMadeByAnotherTabIsNotMissed(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	room, _ := seated(f.svc.Store, pr("bob@testnet"), "cafe", []string{pr("ann@testnet")})
	bob := f.joined(t, "bob@testnet")
	st := open(t, c, sm, room.ID)
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	open(t, c, sm, room.ID) // the other tab
	if h, _ := tick(c, sm, room.ID, st); !strings.HasPrefix(string(h), "_chat_tick") {
		t.Errorf("the first tab reads: %q", h)
	}
}

// A read that fails acknowledges nothing: the watch keeps the signal and the
// cursor, the next read waits out a growing delay, and the first that succeeds
// shows the change. A room gone is answered once, with the new signal.
func TestAFailedReadIsRetriedUntilShown(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	var states []watchState
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(watchView); ok {
			states = append(states, v.State)
		}
		return template.HTML(name), nil
	}
	room, _ := seated(f.svc.Store, pr("bob@testnet"), "cafe", []string{pr("ann@testnet")})
	bob := f.joined(t, "bob@testnet")
	st := open(t, c, sm, room.ID)
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "ann@testnet hello"})
	f.failRuns = true
	reads := func() int { return len(f.ran(roomReadName)) }
	n, completed := reads(), len(f.completes)
	h, err := tick(c, sm, room.ID, st)
	if err != nil || h != "_chat_watch" || reads() != n+1 {
		t.Fatalf("a failure answers the watch alone: %s %v", h, err)
	}
	if len(f.completes) != completed {
		t.Error("a delivery is settled only after a read that succeeded")
	}
	failed := states[len(states)-1]
	if failed.Signal != st.Signal || failed.After != st.After || failed.Fails != 1 || failed.Retry < time.Now().Unix()+4 {
		t.Fatalf("nothing is acknowledged: %+v, was %+v", failed, st)
	}
	if h, _ := tick(c, sm, room.ID, failed); h != "" || reads() != n+1 {
		t.Errorf("before the delay runs nothing: %q", h)
	}
	f.failRuns = false
	failed.Retry = time.Now().Unix() - 1
	if h, _ := tick(c, sm, room.ID, failed); h != "_chat_tick" || reads() != n+2 || len(f.completes) != completed+1 {
		t.Errorf("after it the change is shown and settled: %q", h)
	}
	for fails, want := range map[int]int64{0: 5, 1: 10, 2: 20, 3: 40, 4: 60, 5: 60, 9: 60} {
		w := watchState{Fails: fails}.failed()
		if got := w.Retry - time.Now().Unix(); got < want-1 || got > want || w.Fails != fails+1 {
			t.Errorf("after %d failures the delay is %d, want %d", fails, got, want)
		}
	}
	// The room goes: the page leaves for the landing.
	if err := f.svc.Store.DeleteRoom(pr("bob@testnet"), room.ID); err != nil {
		t.Fatal(err)
	}
	if h, err = tick(c, sm, room.ID, watchState{After: 1, Signal: "stale"}); h != "" || !errors.Is(err, apps.Moved("/chat")) {
		t.Errorf("a room gone: %s %v", h, err)
	}
}

// A room the viewer was removed from, with their delivery still open, sends the
// page to the landing at once, at home and elsewhere, so the open delivery does
// not make every tick read it again.
func TestAGoneRoomIsNotReadAgain(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	f.requesters[home] = "approved"
	f.requesters["chat@away"] = "pending"
	connect(t, c, sm, farChat)
	cafe, _ := seated(f.svc.Store, pr("bob@testnet"), "cafe", []string{pr("ann@testnet")})
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet")})
	for _, room := range []struct {
		chat         *svc.Service
		service, bob string
		id, addr     string
	}{{f.svc, home, "bob@testnet", cafe.ID, cafe.ID + "@" + testKey}, {away, "chat@away", "bob@away", far.ID, far.ID + "@" + awayKey}} {
		st := open(t, c, sm, room.addr)
		f.dispatchAt(room.chat, room.service, "/msg/post", joinedAt(t, room.chat, room.bob), map[string]any{"thread_id": room.id, "message": "ann@testnet hello"})
		if err := room.chat.Store.RemoveMember(pr(room.bob), room.id, pr("ann@testnet")); err != nil {
			t.Fatal(err)
		}
		if h, err := tick(c, sm, room.addr, st); h != "" || !errors.Is(err, apps.Moved("/chat")) {
			t.Errorf("%s gone with a delivery open: %q %v", room.addr, h, err)
		}
	}
}

// A chat the viewer removed or blocked runs nothing: an open tab leaves its
// room, the room's page is not found, and a post from the tab is refused with
// the reason. Nothing reaches that chat, and nothing approves it again.
func TestARemovedChatRunsNothing(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	f.requesters["chat@away"] = "pending"
	connect(t, c, sm, farChat)
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet")})
	room := far.ID + "@" + awayKey
	st := open(t, c, sm, room)
	delete(f.requesters, "chat@away") // ann removed the contact
	f.mu.Lock()
	runs, decided := len(f.runs), len(f.decided)
	f.mu.Unlock()

	if h, err := tick(c, sm, room, watchState{After: st.After, Signal: "stale"}); h != "" || !errors.Is(err, apps.Moved("/chat")) {
		t.Errorf("the open tab leaves: %q %v", h, err)
	}
	var notFound bool
	c.deps.HandleErr = func(w http.ResponseWriter, r *http.Request, err error) bool {
		ke, ok := client.AsKernelError(err)
		notFound = notFound || (ok && ke.Kind == client.KindNotFound)
		return err != nil
	}
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+room, nil)
	req.SetPathValue("id", room)
	if as(c, sm, c.getRoom, req); !notFound {
		t.Error("its page is not found")
	}
	var note string
	c.deps.RenderFragment = func(w http.ResponseWriter, status int, name string, data any) error {
		if v, ok := data.(entriesView); ok {
			note = v.Error
		}
		w.WriteHeader(status)
		return nil
	}
	form := url.Values{"raw_args": {`{"message":"still here?"}`}, "after": {"0"}}
	req = httptest.NewRequest(http.MethodPost, "/chat/room/"+room+"/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.SetPathValue("id", room)
	if rec := as(c, sm, c.postMessage, req); rec.Code != http.StatusUnprocessableEntity || note != "chat@away is no longer one of your chats." {
		t.Errorf("a post from the tab: %d %q", rec.Code, note)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) != runs || len(f.decided) != decided || f.requesters["chat@away"] != "" {
		t.Errorf("chat@away was run or approved again: %v %v", f.runs[runs:], f.decided[decided:])
	}
}

// A page that is not a room renders the rail from what the session last read, with
// no run; its slow watch runs room/list only on a change, and swaps the rail only
// when the rooms differ.
func TestTheRailCostsNoRun(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	mine, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	rail := func() string {
		var h template.HTML
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { h, err = c.Rail(r) }, httptest.NewRequest(http.MethodGet, "/settings", nil))
		if err != nil {
			t.Fatal(err)
		}
		return string(h)
	}
	if got := rail(); strings.Contains(got, "cafe") || len(f.grants) != 0 {
		t.Fatalf("a session not joined shows no rooms and joins nobody: %s %v", got, f.grants)
	}
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(mine.ID), nil)
	req.SetPathValue("id", addr(mine.ID))
	as(c, sm, c.getRoom, req)
	lists := len(f.ran(roomListName))
	got := rail()
	if !strings.Contains(got, ">cafe<") || !strings.Contains(got, `id="watch-rail" name="slow" value="chat:rail:`) {
		t.Fatalf("the rail is what the room's read answered: %s", got)
	}
	if len(f.ran(roomListName)) != lists {
		t.Errorf("a warm rail ran room/list")
	}
	st := watchState{Signal: "old", Rail: "old"}
	h, err := tick(c, sm, railKey, st)
	if err != nil || !strings.Contains(string(h), `class="rail"`) || !strings.Contains(string(h), `id="watch-rail"`) || len(f.ran(roomListName)) != lists+1 {
		t.Fatalf("a change swaps the rail: %s %v", h, err)
	}
	if strings.Contains(string(h), `<dialog id="form-new"`) {
		t.Error("the dialog is never swapped under a click")
	}
	var cache railCache
	as(c, sm, func(w http.ResponseWriter, r *http.Request) {
		b, _ := c.deps.Sessions.AppCache(r.Context(), c.Slug())
		_ = json.Unmarshal([]byte(b), &cache)
	}, httptest.NewRequest(http.MethodGet, "/", nil))
	quiet := watchState{Signal: cache.Signal, Rail: railDigest(cache)}
	if h, _ := tick(c, sm, railKey, quiet); h != "" || len(f.ran(roomListName)) != lists+1 {
		t.Errorf("no change runs nothing: %s", h)
	}
	if h, _ := tick(c, sm, railKey, watchState{Signal: "old", Rail: quiet.Rail}); strings.Contains(string(h), `class="rail"`) {
		t.Errorf("the same rooms keep the rail: %s", h)
	}
}

// The state a page carries survives its one input, and one from before the
// upgrade keeps its cursor, so nothing is shown twice.
func TestWatchStateRoundTrips(t *testing.T) {
	st := watchState{After: 7, Signal: "ab", Rail: "cd", Fails: 2, Retry: 99}
	if got := parseWatch(st.String()); got != st {
		t.Errorf("parse = %+v", got)
	}
	if got := parseWatch("12"); got.After != 12 || got.Signal != "" {
		t.Errorf("an old value: %+v", got)
	}
	if (watchState{Signal: "a"}).due("a") || !(watchState{Signal: "a"}).due("b") || (watchState{Signal: "a", Fails: 1, Retry: time.Now().Unix() + 9}).due("b") {
		t.Error("due")
	}
	if !(watchState{Signal: "a", Fails: 1, Retry: time.Now().Unix() - 1}).due("a") {
		t.Error("a failed read is owed even when the signal returned to the one shown")
	}
}

// as2 runs fn in ann's session, joined as a chat page would have.
func as2(c *Chat, sm *scs.SessionManager, req *http.Request, fn func(*http.Request) (template.HTML, error)) (template.HTML, error) {
	var out template.HTML
	var err error
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) {
		if jerr := c.joinViewer(r); jerr != nil {
			err = jerr
			return
		}
		out, err = fn(r)
	}, req)
	return out, err
}

// Every room command is a run of chat's action as the viewer: a creator
// renames, adds, removes and deletes; a member leaves; an address is required;
// a refusal is said on the page it was asked from.
func TestRoomRoutes(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	create := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=cafe&members=bob%40testnet+carol%40testnet&agent=helper%40testnet"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := as(c, sm, c.postCreateRoom, create)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/chat/room/") {
		t.Fatalf("create: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Header().Get("X-Flash"))
	}
	rooms, _ := f.svc.Store.Rooms(pr("ann@testnet"))
	if len(rooms) != 1 || len(rooms[0].Members) != 1 || len(rooms[0].Invited) != 3 {
		t.Fatalf("the others are invited: %+v", rooms)
	}
	id := rooms[0].ID
	do := func(h http.HandlerFunc, path, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(id)+path, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", addr(id))
		return as(c, sm, h, req)
	}
	if rec := do(c.postAddMember, "/add-member", "member=bare"); !strings.Contains(rec.Header().Get("X-Flash"), "address") {
		t.Errorf("bare member accepted: %s", rec.Header().Get("X-Flash"))
	}
	do(c.postAddMember, "/add-member", "member=dave%40testnet")
	do(c.postRemoveMember, "/remove-member", "member="+url.QueryEscape(pr("carol@testnet")))
	if rec := do(c.postLeave, "/leave", ""); !strings.Contains(rec.Header().Get("X-Flash"), "You may not do that") {
		t.Errorf("creator leaving: %s", rec.Header().Get("X-Flash"))
	}
	room, _, _ := f.svc.Store.Room(pr("ann@testnet"), id)
	if names(room.Members) != "ann@testnet" || names(room.Invited) != "bob@testnet,dave@testnet,helper@testnet" {
		t.Errorf("members = %v, invited = %v", room.Members, room.Invited)
	}
	if rec := do(c.postRename, "/rename", "name=+tea+room+"); rec.Code != 303 || rec.Header().Get("X-Flash") != "" {
		t.Errorf("rename: %d %s", rec.Code, rec.Header().Get("X-Flash"))
	}
	if room, _, _ := f.svc.Store.Room(pr("ann@testnet"), id); room.Name != "tea room" {
		t.Errorf("renamed = %q", room.Name)
	}
	if rec := do(c.postRename, "/rename", "name=+"); !strings.Contains(rec.Header().Get("X-Flash"), "needs a name") {
		t.Errorf("a blank name: %s", rec.Header().Get("X-Flash"))
	}
	for name, want := range map[string]string{roomCreateName: "name", roomAddName: "dave@testnet", roomRemoveName: pr("carol@testnet"), roomLeaveName: id, roomRenameName: "tea room"} {
		if got := f.ran(name); len(got) != 1 || !strings.Contains(got[0], want) {
			t.Errorf("%s ran %v", name, got)
		}
	}
	if rec := do(c.postDeleteRoom, "/delete", ""); rec.Code != 303 || rec.Header().Get("Location") != "/chat" {
		t.Errorf("delete: %d", rec.Code)
	}
	nobody := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=cafe"))
	nobody.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bad := as(c, sm, c.postCreateRoom, nobody); !strings.Contains(bad.Header().Get("X-Flash"), "Choose who") {
		t.Errorf("a conversation with nobody: %s", bad.Header().Get("X-Flash"))
	}
	// The participants come first; a name left blank is theirs.
	unnamed := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=&agent=helper%40testnet&members=bob%40testnet"))
	unnamed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := as(c, sm, c.postCreateRoom, unnamed); rec.Code != 303 {
		t.Fatalf("unnamed: %d %s", rec.Code, rec.Header().Get("X-Flash"))
	}
	if rooms, _ := f.svc.Store.Rooms(pr("ann@testnet")); len(rooms) != 1 || rooms[0].Name != "helper, bob" {
		t.Errorf("a blank name is the participants': %+v", rooms)
	}
}

// The routes: the landing and the rooms are the viewer's; there is no
// connect gesture any more.
func TestRoutes(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	d := c.deps
	d.RequireAuth = func(h http.HandlerFunc) http.HandlerFunc { return h }
	d.Watch = func(string, apps.Watcher) {}
	mux := http.NewServeMux()
	c.Mount(mux, d)
	for _, rt := range []struct {
		method, path string
		want         int
	}{{"POST", "/chat/connect", 404}, {"GET", "/chat/connect", 404}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, nil))
		if rec.Code != rt.want {
			t.Errorf("%s %s = %d, want %d", rt.method, rt.path, rec.Code, rt.want)
		}
	}
}

// The rail is the viewer's rooms, newest activity first, the one in view
// current, an unread one weighted, with its watch; ?form=new opens the
// new-conversation dialog on any page.
func TestRail(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	old, _ := seated(f.svc.Store, pr("bob@testnet"), "old", []string{pr("ann@testnet")})
	f.svc.Store.Post(old.ID, pr("bob@testnet"), raw("x"))
	cafe, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	f.svc.Store.Post(cafe.ID, pr("ann@testnet"), raw("y"))
	f.svc.Store.MarkRead(pr("ann@testnet"), cafe.ID, 1)
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(cafe.ID)+"?form=new", nil)
	h, err := as2(c, sm, req, c.Rail)
	if err != nil {
		t.Fatal(err)
	}
	got := string(h)
	for _, want := range []string{`<a href="/chat/room/` + addr(cafe.ID) + `" aria-current="page">cafe</a>`, `<a href="/chat/room/` + addr(old.ID) + `" class="unread">old</a>`,
		`id="form-new" class="modal-form" open`,
		`data-modal="form-new" aria-label="New conversation"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#plus"/></svg><span>New conversation</span></a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("rail lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, `id="watch-rail"`) {
		t.Error("a room page keeps one watch, the room's")
	}
	if strings.Index(got, ">cafe<") > strings.Index(got, ">old<") {
		t.Error("newest activity first")
	}
}

// Who the room waits on: the members other than the viewer that hold an
// unanswered delivery in the room, named in one line.
func TestWorkingNamesWhoTheRoomWaitsOn(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	r, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", []string{pr("bob@testnet"), pr("cy@testnet")})
	bob, cy := f.joined(t, "bob@testnet"), f.joined(t, "cy@testnet")
	_, _ = bob, cy
	ann := f.joined(t, "ann@testnet")
	f.dispatch("/msg/post", ann, map[string]any{"thread_id": r.ID, "message": "hello"})
	ids, _ := f.svc.Store.Working(r.ID, pr("ann@testnet"))
	waiting, _ := f.svc.Store.Parties(ids)
	v := entriesView{Working: c.labelled(waiting)}
	if got := v.Waiting(); got != "Waiting on bob@testnet and cy@testnet" {
		t.Errorf("Waiting = %q", got)
	}
	if ws, _ := f.svc.Store.Working(r.ID, pr("bob@testnet")); len(ws) != 1 || ws[0] != pr("cy@testnet") {
		t.Errorf("the viewer is never among those waited on: %v", ws)
	}
	if (entriesView{}).Waiting() != "" || (entriesView{Working: []string{"a"}}).Waiting() != "Waiting on a" {
		t.Error("nobody is no line; one is named alone")
	}
	if got := (entriesView{Working: []string{"a", "b", "c"}}).Waiting(); got != "Waiting on a, b and c" {
		t.Errorf("Waiting = %q", got)
	}
	// A line of the room names a member as a row does: an unnamed kernel by
	// its nickname, without the key.
	far := client.Party{Address: "dan@" + awayKey, Principal: pr("dan@away"), Nickname: "away"}
	f.names = map[string]client.Party{far.Principal: far}
	if got := c.labelled([]client.Party{far}); got[0] != "dan@~away" {
		t.Errorf("labelled = %q", got)
	}
	var got string
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) { got = c.undelivered(r, []client.Party{far}) }, httptest.NewRequest(http.MethodGet, "/chat", nil))
	if got != "Not delivered to dan@~away." {
		t.Errorf("undelivered = %q", got)
	}
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	line, _ := rnd.HTML("_working", v)
	if !strings.Contains(string(line), `>Waiting on bob@testnet and cy@testnet <span class="dots" aria-hidden="true">`) {
		t.Errorf("the sentence stands with its dots beside it: %s", line)
	}
	if line, _ := rnd.HTML("_working", entriesView{}); !strings.Contains(string(line), `id="chat-working" hx-swap-oob="true"></p>`) {
		t.Errorf("nobody is an empty line: %s", line)
	}
}

// A room's context is what the planner reads: the viewer's entries as the
// user's, everyone else's as the assistant's, named; a non-member is refused.
func TestContextIsTheRoomAsAModelReadsIt(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	r, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", []string{pr("bob@testnet")})
	f.svc.Store.Post(r.ID, pr("ann@testnet"), raw("find suppliers"))
	f.svc.Store.Post(r.ID, pr("bob@testnet"), json.RawMessage(`{"found":3}`))
	var msgs []client.Message
	var err error
	as2(c, sm, httptest.NewRequest(http.MethodGet, "/", nil), func(rq *http.Request) (template.HTML, error) {
		msgs, err = c.Context(rq, addr(r.ID))
		return "", err
	})
	if err != nil || len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "find suppliers" ||
		msgs[1].Role != "assistant" || msgs[1].Content != `bob@testnet: {"found":3}` {
		t.Fatalf("context = %+v %v", msgs, err)
	}
	// A room elsewhere names the viewer as its kernel does, here by this
	// kernel's key: the viewer's entries are still the user's, and the viewer is
	// not among the room's others.
	away := withAway(t, f)
	f.requesters["chat@away"] = "pending"
	connect(t, c, sm, farChat)
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet")})
	if err := away.Store.Named(client.Party{Address: "ann@" + testKey, Principal: pr("ann@testnet")}); err != nil {
		t.Fatal(err)
	}
	away.Store.Post(far.ID, pr("ann@testnet"), raw("hello far"))
	as2(c, sm, httptest.NewRequest(http.MethodGet, "/", nil), func(rq *http.Request) (template.HTML, error) {
		msgs, err = c.Context(rq, far.ID+"@"+awayKey)
		return "", err
	})
	if err != nil || len(msgs) != 1 || msgs[0].Role != "user" {
		t.Errorf("elsewhere, the viewer's entries are the user's: %+v %v", msgs, err)
	}
	if st := open(t, c, sm, far.ID+"@"+awayKey); st.After == 0 {
		t.Error("the room elsewhere opens")
	}
	other, _ := seated(f.svc.Store, pr("bob@testnet"), "private", nil)
	as2(c, sm, httptest.NewRequest(http.MethodGet, "/", nil), func(rq *http.Request) (template.HTML, error) {
		_, err = c.Context(rq, other.ID)
		return "", nil
	})
	if err == nil {
		t.Fatal("a room the viewer is not in is refused")
	}
	if strings.ContainsAny(err.Error(), "0123456789") {
		t.Errorf("a person never reads a raw status: %q", err)
	}
}

// A person who holds only the grant an earlier version made is uncovered, and
// is joined again; a person covered joins nobody; one left uncovered after
// joining is granted the key kept again.
func TestJoinFollowsTheKernelsPlan(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	f.granted["t"] = map[string]bool{"chat@testnet/msg/post": true}
	f.keys["t"] = "OLD"
	join := func() error {
		var err error
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { err = c.joinViewer(r) }, httptest.NewRequest(http.MethodGet, "/chat", nil))
		return err
	}
	if err := join(); err != nil {
		t.Fatal(err)
	}
	if len(f.grants) != 1 || !f.granted["t"]["chat@testnet/room/rename"] {
		t.Fatalf("an uncovered person is joined at the root: %v %v", f.grants, f.granted["t"])
	}
	if err := join(); err != nil || len(f.grants) != 1 {
		t.Errorf("covered joins nobody: %v %v", err, f.grants)
	}
	// A new action leaves the person uncovered until the next join.
	delete(f.granted["t"], "chat@testnet/room/add")
	if err := join(); err != nil || len(f.grants) != 1 || len(f.regrants) != 1 || !f.granted["t"]["chat@testnet/room/add"] {
		t.Errorf("an ungranted action is granted the key kept again: %v %v %v", err, f.grants, f.regrants)
	}
}

// A join whose connect fails retires nothing: the key already connected keeps
// working; the failure is the join's.
func TestAFailedJoinKeepsTheKeyWorking(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	join := func() (err error) {
		as(c, sm, func(w http.ResponseWriter, r *http.Request) { err = c.joinViewer(r) }, httptest.NewRequest(http.MethodGet, "/chat", nil))
		return err
	}
	if err := join(); err != nil {
		t.Fatal(err)
	}
	held := f.keyOf("t")
	f.failGrant = true
	delete(f.granted["t"], "chat@testnet/room/add")
	if err := join(); err == nil {
		t.Error("a refused connect is the join's failure")
	}
	if m, ok := f.svc.Store.Member(held); !ok || m != pr("ann@testnet") {
		t.Errorf("the earlier key still works: %q %v", m, ok)
	}
}

// A run joins the viewer when the kernel holds no grant, and when chat no longer
// knows the key connected, and runs once more. A joined person's sign-in mints
// nothing: the session holds no key at all.
func TestARunJoinsWhenItMust(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	room, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	read := func() (out roomRead, err error) {
		as(c, sm, func(w http.ResponseWriter, r *http.Request) {
			out, err = c.readRoom(r, homeChat, room.ID, latest, false)
		}, httptest.NewRequest(http.MethodGet, "/", nil))
		return out, err
	}
	if out, err := read(); err != nil || out.Room.ID != room.ID || len(f.grants) != 1 {
		t.Fatalf("no grant joins: %+v %v %v", out, err, f.grants)
	}
	if _, err := read(); err != nil || len(f.grants) != 1 {
		t.Errorf("joined, it joins nobody: %v %v", err, f.grants)
	}
	f.keys["t "+home] = "UNKNOWN"
	if _, err := read(); err != nil || len(f.grants) != 2 {
		t.Errorf("an unknown key joins again: %v %v", err, f.grants)
	}
}

// Of a refusal only the status comes through the kernel, so the UI's sentence is
// chosen by it, and a room gone is the not-found page.
func TestRefusalsAreSaidByStatus(t *testing.T) {
	for status, want := range map[string]string{
		"400": "not valid", "401": "not joined", "403": "may not", "404": "no longer exists", "409": "already pending",
	} {
		err := &client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "upstream returned status " + status}
		if got := refusal(err, home); !strings.Contains(got, want) {
			t.Errorf("%s = %q", status, got)
		}
	}
	for _, err := range []error{
		&client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "upstream returned status 500"},
		&client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "model did not select"},
		&client.KernelError{Kind: client.KindUnauthorized},
		errors.New("transport"),
	} {
		if got := refusal(err, home); got != "" {
			t.Errorf("%v must be no refusal: %q", err, got)
		}
	}
	if got := refusal(&client.KernelError{Kind: client.KindPeerUnreachable}, "chat@away"); got != "chat@away is not answering. Try again shortly." {
		t.Errorf("a kernel unreachable: %q", got)
	}
	if got := refusal(&client.KernelError{Kind: client.KindTimeout}, "chat@away"); got != "chat@away has not answered yet. It is done once it does." {
		t.Errorf("a call parked: %q", got)
	}
	if got := refusal(&client.KernelError{Kind: client.KindGrantRequired}, "chat@away"); got != "You disconnected this from chat@away in Settings." {
		t.Errorf("a grant removed: %q", got)
	}
	if got := refusal(&client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "upstream request failed"}, home); !strings.Contains(got, "not answering") {
		t.Errorf("chat down: %q", got)
	}
	if got := refusal(&client.KernelError{Kind: client.KindExecutionFailed, KernelMessage: "remote call failed"}, "chat@away"); got != "chat@away refused or failed; a chat on another kernel gives no reason." {
		t.Errorf("a chat elsewhere relays no reason: %q", got)
	}
	if got := refusal(&client.KernelError{Kind: client.KindInvalidInput, KernelMessage: "message: required field missing"}, home); got != "message: required field missing" {
		t.Errorf("the kernel's reason for input: %q", got)
	}

	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	var notFound bool
	c.deps.HandleErr = func(w http.ResponseWriter, r *http.Request, err error) bool {
		ke, ok := client.AsKernelError(err)
		notFound = notFound || (ok && ke.Kind == client.KindNotFound)
		return err != nil
	}
	other, _ := seated(f.svc.Store, pr("bob@testnet"), "theirs", []string{pr("ann@testnet")})
	do := func(h http.HandlerFunc, id, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(id), strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", addr(id))
		return as(c, sm, h, req)
	}
	if rec := do(c.postRename, other.ID, "name=mine"); !strings.Contains(rec.Header().Get("X-Flash"), "may not") {
		t.Errorf("a member renaming the creator's room: %s", rec.Header().Get("X-Flash"))
	}
	if room, _, _ := f.svc.Store.Room(pr("ann@testnet"), other.ID); room.Name != "theirs" {
		t.Errorf("renamed anyway: %s", room.Name)
	}
	if do(c.postRename, "no-such-1", "name=x"); !notFound {
		t.Error("a room gone is the not-found page")
	}
	notFound = false
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr("no-such-1"), nil)
	req.SetPathValue("id", addr("no-such-1"))
	if as(c, sm, c.getRoom, req); !notFound {
		t.Error("a room that is not there is the not-found page")
	}
	theirs, _ := seated(f.svc.Store, pr("bob@testnet"), "private", nil)
	notFound = false
	req = httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(theirs.ID), nil)
	req.SetPathValue("id", addr(theirs.ID))
	if as(c, sm, c.getRoom, req); !notFound {
		t.Error("a room the viewer is not in is the not-found page")
	}
}

// The creator is offered Rename, a dialog with the name in it, reachable without
// script; a member is offered neither.
func TestRenameIsTheCreators(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		if err := rnd.Render(w, status, page, server.Page{Title: title, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	page := func(path string, id string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetPathValue("id", addr(id))
		return as(c, sm, c.getRoom, req).Body.String()
	}
	mine, _ := seated(f.svc.Store, pr("ann@testnet"), "ca<fe", []string{pr("bob@testnet")})
	got := page("/chat/room/"+addr(mine.ID), mine.ID)
	if !strings.Contains(got, `<dialog id="form-rename" class="modal-form" aria-label="Rename" hx-swap-oob="true">`) || strings.Contains(got, `action="/chat/room/`+addr(mine.ID)+`/rename"`) {
		t.Error("a room page holds the dialog closed and empty")
	}
	got = page("/chat/room/"+addr(mine.ID)+"?form=rename", mine.ID)
	for _, want := range []string{`hx-swap-oob="true" open>`, `action="/chat/room/` + addr(mine.ID) + `/rename"`, `name="name" value="ca&lt;fe" required`} {
		if !strings.Contains(got, want) {
			t.Errorf("?form=rename opens the dialog without script: lacks %s", want)
		}
	}
	theirs, _ := seated(f.svc.Store, pr("bob@testnet"), "theirs", []string{pr("ann@testnet")})
	got = page("/chat/room/"+addr(theirs.ID)+"?form=rename", theirs.ID)
	if strings.Contains(got, `hx-swap-oob="true" open>`) {
		t.Error("a member is offered no rename")
	}
}

// A room's header is its name alone. The members' dialog lists them for everyone
// and changes them for the creator.
func TestRoomHeader(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		if err := rnd.Render(w, status, page, server.Page{Title: title, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	page := func(id, query string) string {
		req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(id)+query, nil)
		req.SetPathValue("id", addr(id))
		return as(c, sm, c.getRoom, req).Body.String()
	}
	mine, _ := seated(f.svc.Store, pr("ann@testnet"), "ca<fe", []string{pr("bob@testnet")})
	got := page(mine.ID, "")
	if want := `<nav class="crumb" id="chat-crumb" hx-swap-oob="true"><b class="stretch" title="ca&lt;fe">ca&lt;fe</b></nav>`; !strings.Contains(got, want) {
		t.Errorf("the crumb is not the name alone: %s", got)
	}
	for _, want := range []string{`<dialog id="form-members" class="modal-form" aria-label="Members" hx-swap-oob="true">`, ">bob@testnet<", `/remove-member"`, `/add-member"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the creator's dialog lacks %s", want)
		}
	}
	if !strings.Contains(page(mine.ID, "?form=members"), `<dialog id="form-members" class="modal-form" aria-label="Members" hx-swap-oob="true" open>`) {
		t.Error("?form=members opens the dialog")
	}
	theirs, _ := seated(f.svc.Store, pr("bob@testnet"), "theirs", []string{pr("ann@testnet")})
	got = page(theirs.ID, "")
	if !strings.Contains(got, ">bob@testnet<") || strings.Contains(got, `/remove-member"`) || strings.Contains(got, `/add-member"`) {
		t.Error("a member sees the list and cannot change it")
	}
}

// rigCommands is a chat that renders its real templates, with a warm rail.
func rigCommands(t *testing.T, f *fakeKernel) (*Chat, *scs.SessionManager) {
	t.Helper()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	c.deps.RenderFragment = rnd.Fragment
	return c, sm
}

// command sends a room command as htmx does from a page, with its chat watch.
func command(c *Chat, sm *scs.SessionManager, h http.HandlerFunc, id, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(id)+"/x", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.SetPathValue("id", addr(id))
	return as(c, sm, h, req)
}

func railWatch(st watchState) string { return "slow=" + url.QueryEscape("chat:rail:"+st.String()) }
func roomWatch(id string, st watchState) string {
	return "watch=" + url.QueryEscape("chat:"+addr(id)+":"+st.String())
}

// A room's menu offers its creator rename and delete, any other member leave,
// each sent in place with the page's watch.
func TestRailMenus(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	mine, _ := seated(f.svc.Store, pr("ann@testnet"), "mi<ne", nil)
	theirs, _ := seated(f.svc.Store, pr("bob@testnet"), "theirs", []string{pr("ann@testnet")})
	h, err := as2(c, sm, httptest.NewRequest(http.MethodGet, "/settings", nil), c.Rail)
	if err != nil {
		t.Fatal(err)
	}
	got := string(h)
	for _, want := range []string{`id="menu-room-` + addr(mine.ID) + `"`, `data-menu="menu-room-` + addr(mine.ID) + `"`, `hx-get="/chat/room/` + addr(mine.ID) + `/rename"`,
		`hx-post="/chat/room/` + addr(mine.ID) + `/delete"`, `data-confirm="Delete mi&lt;ne for everyone?"`,
		`id="menu-room-` + addr(theirs.ID) + `"`, `hx-post="/chat/room/` + addr(theirs.ID) + `/leave"`, `data-confirm="Leave theirs?"`, `Leave…`,
		`<button class="btn-icon inline" data-menu="menu-room-` + addr(mine.ID) + `"`, `icons.svg#ellipsis`,
		`<dialog id="form-rename"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the rail lacks %s", want)
		}
	}
	for _, not := range []string{`/chat/room/` + addr(mine.ID) + `/leave`, `/chat/room/` + addr(theirs.ID) + `/rename`, `/chat/room/` + addr(theirs.ID) + `/delete`} {
		if strings.Contains(got, not) {
			t.Errorf("the rail offers %s", not)
		}
	}
	// Every menu offers the members: in place for the room in view, whose dialog is
	// on the page; any other opens that room.
	if strings.Count(got, ">Members…</a>") != 2 || strings.Contains(got, `data-modal="form-members"`) {
		t.Errorf("no room is in view, so every Members… goes to its room: %s", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(mine.ID), nil)
	h, err = as2(c, sm, req, c.Rail)
	if err != nil {
		t.Fatal(err)
	}
	got = string(h)
	if !strings.Contains(got, `href="/chat/room/`+addr(mine.ID)+`?form=members" data-modal="form-members">Members…</a>`) ||
		!strings.Contains(got, `href="/chat/room/`+addr(theirs.ID)+`?form=members">Members…</a>`) {
		t.Errorf("only the room in view opens the members in place: %s", got)
	}
}

// Opening the dialog runs nothing, and only a room the viewer made has one.
func TestGetRename(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	mine, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	theirs, _ := seated(f.svc.Store, pr("bob@testnet"), "theirs", []string{pr("ann@testnet")})
	if _, err := as2(c, sm, httptest.NewRequest(http.MethodGet, "/settings", nil), c.Rail); err != nil {
		t.Fatal(err)
	}
	runs := len(f.runs)
	get := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(id)+"/rename", nil)
		req.SetPathValue("id", addr(id))
		return as(c, sm, c.getRename, req)
	}
	rec := get(mine.ID)
	for _, want := range []string{`hx-swap-oob="true" open>`, `value="cafe"`, `hx-post="/chat/room/` + addr(mine.ID) + `/rename"`, "autofocus"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the dialog lacks %s: %s", want, rec.Body)
		}
	}
	if get(theirs.ID).Code != 404 || get("no-such-room").Code != 404 {
		t.Error("a room not the viewer's own has no dialog")
	}
	if len(f.runs) != runs {
		t.Errorf("opening the dialog ran %v", f.runs[runs:])
	}
}

// A rename in place is one run and one read: the dialog closes, the rail takes the
// name, the crumb too when the room is in view, and the next tick has nothing to do.
func TestRenameInPlace(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	room, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	other, _ := seated(f.svc.Store, pr("ann@testnet"), "other", nil)

	lists, renames := len(f.ran(roomListName)), len(f.ran(roomRenameName))
	got := command(c, sm, c.postRename, room.ID, "name=tea&"+railWatch(watchState{Rail: "old"})).Body.String()
	for _, want := range []string{`<dialog id="form-rename" class="modal-form" aria-label="Rename" hx-swap-oob="true">`, `hx-swap-oob="outerHTML:.rail"`, ">tea<", `id="watch-rail"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the answer lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "chat-crumb") {
		t.Error("a page not showing the room has no crumb to change")
	}
	if len(f.ran(roomRenameName)) != renames+1 || len(f.ran(roomListName)) != lists+1 {
		t.Errorf("one run and one read: %d %d", len(f.ran(roomRenameName))-renames, len(f.ran(roomListName))-lists)
	}
	m := regexp.MustCompile(`value="chat:rail:([^"]+)"`).FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("no watch: %s", got)
	}
	if h, err := tick(c, sm, railKey, parseWatch(m[1])); err != nil || h != "" {
		t.Errorf("the tick after the answer swaps nothing: %s %v", h, err)
	}

	// In view, the crumb follows; another room's rename from that page leaves it.
	got = command(c, sm, c.postRename, room.ID, "name=coffee&"+roomWatch(room.ID, watchState{Rail: "old"})).Body.String()
	for _, want := range []string{`<nav class="crumb" id="chat-crumb" hx-swap-oob="true"><b class="stretch" title="coffee">coffee</b></nav>`, `data-confirm="Delete coffee for everyone?"`, `hx-swap-oob="outerHTML:.rail"`} {
		if !strings.Contains(got, want) {
			t.Errorf("in view, the crumb and the rail follow, lacking %s: %s", want, got)
		}
	}
	got = command(c, sm, c.postRename, other.ID, "name=else&"+roomWatch(room.ID, watchState{Rail: "old"})).Body.String()
	if strings.Contains(got, "chat-crumb") || !strings.Contains(got, ">else<") {
		t.Errorf("another room's rename leaves the crumb: %s", got)
	}

	// A watch waiting out a failed read still reads after a command.
	wait := watchState{Rail: "old", Fails: 3, Retry: time.Now().Unix() + 60}
	got = command(c, sm, c.postRename, room.ID, "name=late&"+railWatch(wait)).Body.String()
	if !strings.Contains(got, ">late<") || strings.Contains(got, ".3.") {
		t.Errorf("a failed watch is read past and cleared: %s", got)
	}
}

// A refused rename comes back open, with the reason and the name typed.
func TestRenameRefusedInPlace(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	theirs, _ := seated(f.svc.Store, pr("bob@testnet"), "theirs", []string{pr("ann@testnet")})
	for form, reason := range map[string]string{"name=mi%3Cne": "You may not do that in this room.", "name=+": "A room needs a name."} {
		rec := command(c, sm, c.postRename, theirs.ID, form+"&"+railWatch(watchState{}))
		got := rec.Body.String()
		for _, want := range []string{`hx-swap-oob="true" open>`, reason} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: lacks %s: %s", form, want, got)
			}
		}
		if rec.Code != 200 || strings.Contains(got, `class="rail"`) {
			t.Errorf("%s: %d %s", form, rec.Code, got)
		}
	}
	if rec := command(c, sm, c.postRename, theirs.ID, "name=mi%3Cne"); !strings.Contains(rec.Body.String(), `value="mi&lt;ne"`) {
		t.Errorf("the name typed is kept: %s", rec.Body)
	}
	if rooms, _ := f.svc.Store.Rooms(pr("ann@testnet")); rooms[0].Name != "theirs" {
		t.Errorf("rooms = %+v", rooms)
	}
}

// Deleting or leaving answers the rail in place; the room in view is left for the
// landing; a room already gone is done, though no change was counted.
func TestDropInPlace(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	keep, _ := seated(f.svc.Store, pr("ann@testnet"), "keep", nil)
	mk := func(creator, name string) string {
		r, _ := seated(f.svc.Store, pr(creator), name, []string{pr("ann@testnet")})
		return r.ID
	}
	gone := func(h http.HandlerFunc, id, watch, name string) {
		t.Helper()
		rec := command(c, sm, h, id, watch)
		if rec.Code != 200 || rec.Header().Get("HX-Redirect") != "" || !strings.Contains(rec.Body.String(), `class="rail"`) ||
			strings.Contains(rec.Body.String(), ">"+name+"<") || !strings.Contains(rec.Body.String(), ">keep<") {
			t.Errorf("%s: %d %q %s", name, rec.Code, rec.Header().Get("HX-Redirect"), rec.Body)
		}
	}
	gone(c.postDeleteRoom, mk("ann@testnet", "del"), railWatch(watchState{Rail: "old"}), "del")
	gone(c.postLeave, mk("bob@testnet", "left"), railWatch(watchState{Rail: "old"}), "left")
	gone(c.postDeleteRoom, mk("ann@testnet", "del2"), roomWatch(keep.ID, watchState{Rail: "old"}), "del2")
	gone(c.postLeave, mk("bob@testnet", "left2"), roomWatch(keep.ID, watchState{Rail: "old"}), "left2")

	for _, in := range []struct {
		h       http.HandlerFunc
		creator string
	}{{c.postDeleteRoom, "ann@testnet"}, {c.postLeave, "bob@testnet"}} {
		id := mk(in.creator, "shown")
		rec := command(c, sm, in.h, id, roomWatch(id, watchState{}))
		if rec.Code != 200 || rec.Header().Get("HX-Redirect") != "/chat" {
			t.Errorf("the room in view goes to the landing: %d %v", rec.Code, rec.Header())
		}
	}

	// Already gone, from both kinds of page: the run failed, so nothing moved the
	// signal, and the read is made all the same.
	for _, watch := range []string{railWatch(watchState{Rail: "old"}), roomWatch(keep.ID, watchState{Rail: "old"})} {
		id := mk("ann@testnet", "ghost")
		if err := f.svc.Store.DeleteRoom(pr("ann@testnet"), id); err != nil {
			t.Fatal(err)
		}
		before := c.changes["ann@testnet"]
		gone(c.postDeleteRoom, id, watch, "ghost")
		if c.changes["ann@testnet"] != before {
			t.Error("a failed run counts no change")
		}
	}
	if rec := command(c, sm, c.postDeleteRoom, "no-such", roomWatch("no-such", watchState{})); rec.Header().Get("HX-Redirect") != "/chat" {
		t.Errorf("the room in view already gone goes to the landing: %v", rec.Header())
	}
}

// A page renders no id twice, the rail and the room together.
func TestRoomPageIDsAreUnique(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	room, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", []string{pr("bob@testnet")})
	seated(f.svc.Store, pr("bob@testnet"), "other", []string{pr("ann@testnet")})
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		rail, err := c.Rail(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := rnd.Render(w, status, page, server.Page{Title: title, Data: data, Rail: rail, Me: "ann@testnet"}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(room.ID), nil)
	req.SetPathValue("id", addr(room.ID))
	got := as(c, sm, c.getRoom, req).Body.String()
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).FindAllStringSubmatch(got, -1) {
		if seen[m[1]] {
			t.Errorf("id %s is rendered twice", m[1])
		}
		seen[m[1]] = true
	}
	for _, want := range []string{"chat-crumb", "form-rename", "form-members", "menu-room-" + addr(room.ID)} {
		if !seen[want] {
			t.Errorf("no id %s: %s", want, got)
		}
	}
}

// A change of members refused comes back in the open dialog with the reason and
// the address typed; done, the page reloads with the dialog open.
func TestMembersInPlace(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	mine, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", []string{pr("bob@testnet")})
	theirs, _ := seated(f.svc.Store, pr("bob@testnet"), "theirs", []string{pr("ann@testnet")})
	if _, err := as2(c, sm, httptest.NewRequest(http.MethodGet, "/settings", nil), c.Rail); err != nil {
		t.Fatal(err)
	}
	rec := command(c, sm, c.postAddMember, mine.ID, "member=bare")
	got := rec.Body.String()
	for _, want := range []string{`<dialog id="form-members" class="modal-form" aria-label="Members" hx-swap-oob="true" open>`, "A member is an address", `value="bare"`, ">bob@testnet<"} {
		if !strings.Contains(got, want) {
			t.Errorf("a bare address: lacks %s: %s", want, got)
		}
	}
	if rec.Code != 200 || rec.Header().Get("HX-Redirect") != "" {
		t.Errorf("a refusal stays on the page: %d %v", rec.Code, rec.Header())
	}
	got = command(c, sm, c.postAddMember, theirs.ID, "member=dave%40testnet").Body.String()
	for _, want := range []string{" open>", "You may not do that"} {
		if !strings.Contains(got, want) {
			t.Errorf("not the creator: lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, `/add-member"`) {
		t.Errorf("a member's dialog offers no adding: %s", got)
	}
	got = command(c, sm, c.postRemoveMember, theirs.ID, "member=bob%40testnet").Body.String()
	if !strings.Contains(got, " open>") || !strings.Contains(got, "You may not do that") {
		t.Errorf("a refused removal: %s", got)
	}
	to := "/chat/room/" + addr(mine.ID) + "?form=members"
	if rec := command(c, sm, c.postAddMember, mine.ID, "member=dave%40testnet"); rec.Header().Get("HX-Redirect") != to {
		t.Errorf("an addition: %v", rec.Header())
	}
	if rec := command(c, sm, c.postRemoveMember, mine.ID, "member=dave%40testnet"); rec.Header().Get("HX-Redirect") != to {
		t.Errorf("a removal: %v", rec.Header())
	}
	if room, _, _ := f.svc.Store.Room(pr("ann@testnet"), mine.ID); !slices.Equal(room.Members, []string{pr("ann@testnet"), pr("bob@testnet")}) && !slices.Equal(room.Members, []string{pr("bob@testnet"), pr("ann@testnet")}) {
		t.Errorf("members = %v", room.Members)
	}
}

// A conversation refused keeps the dialog open with the reason and all that was
// chosen and typed; started, the page goes to it.
func TestNewRoomInPlace(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	agentHome(t, c, "helper@testnet", "AH")
	post := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		return as(c, sm, c.postCreateRoom, req)
	}
	rec := post("name=tea")
	got := rec.Body.String()
	for _, want := range []string{`<dialog id="form-new" class="modal-form" open aria-label="New conversation" hx-swap-oob="true">`, "Choose who the conversation is with.", `name="name" value="tea"`, `value="helper@testnet">`} {
		if !strings.Contains(got, want) {
			t.Errorf("nobody chosen: lacks %s: %s", want, got)
		}
	}
	if rec.Code != 200 || rec.Header().Get("HX-Redirect") != "" || len(f.ran(roomCreateName)) != 0 {
		t.Errorf("a refusal stays on the page and runs nothing: %d %v %v", rec.Code, rec.Header(), f.ran(roomCreateName))
	}
	got = post("agent=helper%40testnet&members=bare&name=tea").Body.String()
	for _, want := range []string{"bare is not an address", `value="helper@testnet" checked>`, ">bare</textarea>", `name="name" value="tea"`} {
		if !strings.Contains(got, want) {
			t.Errorf("a bad address: lacks %s: %s", want, got)
		}
	}
	rec = post("members=bob%40testnet&name=tea")
	if to := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(to, "/chat/room/") {
		t.Errorf("a start goes to the room: %d %v", rec.Code, rec.Header())
	}
}

// A failure no sentence of chat's explains is said too: a page answered in place
// could not show an error page.
func TestUnexplainedFailureIsSaid(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := rigCommands(t, f)
	room, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(room.ID)+"/delete", nil)
	req.Header.Set("HX-Request", "true")
	rec := as(c, sm, func(w http.ResponseWriter, r *http.Request) {
		c.then(w, r, &client.KernelError{Kind: client.KindInsufficientFunds}, "/chat")
	}, req)
	if !strings.Contains(rec.Header().Get("X-Flash"), "balance") {
		t.Errorf("flash = %q", rec.Header().Get("X-Flash"))
	}
}

// The room routes are mounted, the rename among them.
func TestRenameRouteIsMounted(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	d := c.deps
	d.RequireAuth = func(h http.HandlerFunc) http.HandlerFunc { return h }
	d.Watch = func(string, apps.Watcher) {}
	mux := http.NewServeMux()
	c.Mount(mux, d)
	room, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(room.ID)+"/rename", strings.NewReader("name=tea"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := inSession(c, sm, mux.ServeHTTP, req); rec.Code != 303 || rec.Header().Get("Location") != "/chat/room/"+addr(room.ID) {
		t.Fatalf("rename: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_kernel/chat/msg/post", nil))
	if rec.Code != 404 {
		t.Errorf("the UI serves no part of chat: %d", rec.Code)
	}
}

// The UI reaches the chat server by no route of its own: the app dials nothing,
// and only the program itself imports the service.
func TestTheUIReachesChatOnlyThroughTheKernel(t *testing.T) {
	b, err := os.ReadFile("chat.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"http.Client", "http.NewRequest", "http.Get", "http.Post", "services/chat"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("chat.go holds %s", gone)
		}
	}
	root := filepath.Join("..", "..")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(src), `juice-ui/services/chat"`) && filepath.ToSlash(path) != "../../cmd/juice-chat/main.go" {
			t.Errorf("%s imports the service", path)
		}
		return nil
	})
}

// withAway gives the fake kernel a chat on another kernel, chat@away, with a
// store of its own.
func withAway(t *testing.T, f *fakeKernel) *svc.Service {
	t.Helper()
	store, err := svc.Open("", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.away = &svc.Service{Store: store, Secret: "S2", Kernel: f.svc.Kernel}
	return f.away
}

// ranAt is the arguments of every run of an action of the chat at service.
func (f *fakeKernel) ranAt(service, name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, run := range f.runs {
		var in struct {
			Action string          `json:"action"`
			Args   json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal([]byte(run), &in)
		if in.Action == service+"/"+name {
			out = append(out, string(in.Args))
		}
	}
	return out
}

// The rail holds the rooms of every chat the viewer joined, newest activity
// first, a room elsewhere named with its kernel. Every row is its name and its
// menu, no card. A chat elsewhere that has addressed the viewer is a row asking
// to be connected and runs nothing until Connect, which joins by the path home
// takes. A chat that cannot be read is a row under the rooms, its reason its
// title, its menu disconnecting or blocking it, the others still shown, and the
// watch retries it. Disconnect returns a chat to asking and hides its rooms, as
// a grant removed in Settings does, and nothing joins it again; Block removes
// it. A tick reads the viewer once at most.
func TestTheRailHoldsEveryChat(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	f.requesters[home] = "approved"
	cafe, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	f.svc.Store.Post(cafe.ID, pr("ann@testnet"), raw("here"))
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet")})
	away.Store.Post(far.ID, pr("bob@away"), raw("there"))
	f.requesters["chat@away"] = "pending" // chat@away addressed ann

	h, _ := tick(c, sm, railKey, watchState{Signal: "old"})
	got := string(h)
	if !strings.Contains(got, `<div class="rail-row">
  <button class="rail-item unread" data-menu="menu-ask-chat@`+awayKey+`"`) || !strings.Contains(got, `<span>chat@away<span class="sr-only">: chat@`+awayKey+`</span></span>`) ||
		!strings.Contains(got, `action="/chat/chats/chat@`+awayKey+`/connect"`) || !strings.Contains(got, `data-confirm="Block away?"`) ||
		strings.Contains(got, "rail-card") || strings.Contains(got, ">far ") || len(f.ranAt("chat@away", roomListName)) != 0 {
		t.Fatalf("a chat asking is a row whose menu connects or blocks it, and runs nothing: %s", got)
	}

	if rec := connect(t, c, sm, farChat); rec.Code != 303 {
		t.Fatalf("connect: %d %s", rec.Code, rec.Header().Get("X-Flash"))
	}
	if reg := f.ranAt("chat@away", joinName); len(reg) != 1 || reg[0] != `{}` {
		t.Errorf("ann is joined to chat@away by the path home takes: %v", reg)
	}
	if !slices.ContainsFunc(f.grants, func(g string) bool { return strings.Contains(g, `"selector":"chat@away"`) }) {
		t.Errorf("the key is connected at chat@away's root: %v", f.grants)
	}
	reads := f.meReads
	h, err = tick(c, sm, railKey, watchState{Signal: "old"})
	if f.meReads-reads > 1 {
		t.Errorf("a tick reads the viewer once at most: %d", f.meReads-reads)
	}
	got = string(h)
	if err != nil || !strings.Contains(got, `<a href="/chat/room/`+far.ID+`@`+awayKey+`" class="unread">far <span class="muted">· away</span></a>`) ||
		!strings.Contains(got, `href="/chat/room/`+cafe.ID+`@`+testKey+`"`) || !strings.Contains(got, `>cafe</a>`) || strings.Index(got, ">far ") > strings.Index(got, ">cafe<") ||
		strings.Contains(got, "/connect") || !strings.Contains(got, `Disconnect away…`) {
		t.Fatalf("both chats' rooms, newest first, the one elsewhere disconnected from its menu: %s %v", got, err)
	}

	f.awayDown = true
	var st watchState
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(watchView); ok {
			st = v.State
		}
		return rnd.HTML(name, data)
	}
	h, _ = tick(c, sm, railKey, watchState{Signal: "old"})
	got = string(h)
	if !strings.Contains(got, `>cafe</a>`) || strings.Contains(got, ">far ") ||
		!strings.Contains(got, `<button class="rail-item rail-down" data-menu="menu-down-chat@`+awayKey+`" aria-haspopup="menu" aria-expanded="false" title="chat@away refused or failed; a chat on another kernel gives no reason.">`) ||
		!strings.Contains(got, `#alert"/></svg> chat@away`) || strings.Index(got, "rail-down") < strings.Index(got, ">cafe<") ||
		!strings.Contains(got, `Disconnect away…`) || !strings.Contains(got, `Block away…`) || st.Fails != 1 {
		t.Fatalf("a chat down is a row under the rooms, its reason its title, with its menu, and retried; the others stay: %s %+v", got, st)
	}

	// Disconnected while its kernel does not answer: gone here at once, and said.
	if rec := chatVerb(c, sm, farChat, "disconnect"); !strings.Contains(rec.Header().Get("X-Flash"), "did not answer") {
		t.Errorf("a disconnect unanswered is said: %s", rec.Header().Get("X-Flash"))
	}
	f.awayDown = false
	if f.requesters["chat@away"] != "pending" || !slices.Contains(f.ungrants, "chat@away") {
		t.Errorf("disconnect revokes and ungrants: %v %v", f.requesters, f.ungrants)
	}
	lists := len(f.ranAt("chat@away", roomListName))
	h, _ = tick(c, sm, railKey, watchState{Signal: "old"})
	if got = string(h); strings.Contains(got, ">far ") || !strings.Contains(got, `/chat/chats/chat@`+awayKey+`/connect`) || len(f.ranAt("chat@away", roomListName)) != lists {
		t.Errorf("a chat disconnected asks again and runs nothing: %s", got)
	}
	if reg := f.ranAt("chat@away", joinName); len(reg) != 1 {
		t.Errorf("nothing connects it again: %v", reg)
	}

	// A grant removed elsewhere, as Settings does, with the chat still approved:
	// it asks again, and no read joins it. Connect grants the key kept again.
	connect(t, c, sm, farChat)
	if len(f.regrants) != 1 || len(f.ranAt("chat@away", joinName)) != 1 {
		t.Errorf("connecting again grants the key it kept: %v %v", f.regrants, f.ranAt("chat@away", joinName))
	}
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) {
		_ = c.deps.Sessions.Do(r.Context(), func(tok string) error { return c.deps.Client.DeleteGrant(r.Context(), tok, "chat@away") })
	}, httptest.NewRequest(http.MethodGet, "/settings", nil))
	for range 3 {
		h, _ = tick(c, sm, railKey, watchState{Signal: "old"})
	}
	if got = string(h); strings.Contains(got, ">far ") || !strings.Contains(got, `/chat/chats/chat@`+awayKey+`/connect`) || len(f.regrants) != 1 {
		t.Errorf("a grant removed in Settings leaves the chat asking, and nothing joins it: %s %v", got, f.regrants)
	}
	// Part of its grants removed, as Settings does folder by folder: a run of an
	// action whose grant is gone is refused and joins nothing.
	connect(t, c, sm, farChat)
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) {
		_ = c.deps.Sessions.Do(r.Context(), func(tok string) error { return c.deps.Client.DeleteGrant(r.Context(), tok, "chat@away/msg") })
	}, httptest.NewRequest(http.MethodGet, "/settings", nil))
	var postErr error
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) {
		postErr = c.run(r, farChat, postName, map[string]any{"thread_id": far.ID, "message": "hi"}, nil)
	}, httptest.NewRequest(http.MethodPost, "/", nil))
	if refusal(postErr, "chat@away") != "You disconnected this from chat@away in Settings." || len(f.regrants) != 2 {
		t.Errorf("a part disconnected in Settings stays so: %v %v", postErr, f.regrants)
	}

	if rec := chatVerb(c, sm, farChat, "block"); rec.Code != 303 || f.requesters["chat@away"] != "blocked" {
		t.Errorf("block: %d %v", rec.Code, f.requesters)
	}
	h, _ = tick(c, sm, railKey, watchState{Signal: "old"})
	if got = string(h); strings.Contains(got, "away") || !strings.Contains(got, ">cafe</a>") {
		t.Errorf("a chat blocked leaves the rail: %s", got)
	}
	if rec := chatVerb(c, sm, homeChat, "disconnect"); rec.Code == 303 || f.requesters[home] != "approved" {
		t.Errorf("home is never disconnected: %d", rec.Code)
	}
}

// A room elsewhere takes every path a room at home takes, addressed to its
// chat: the page, the post with the room from its address, the heartbeat. A
// post that did not reach a member says so beside the composer. A bare room id
// names no room.
func TestARoomElsewhereTakesTheSamePath(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	f.requesters["chat@away"] = "pending"
	connect(t, c, sm, farChat)
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet"), pr("remy@nowhere")})
	var view roomView
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		view, _ = data.(roomView)
		w.WriteHeader(status)
	}
	req := httptest.NewRequest(http.MethodGet, "/chat/room/"+far.ID+"@"+awayKey, nil)
	req.SetPathValue("id", far.ID+"@"+awayKey)
	if rec := as(c, sm, c.getRoom, req); rec.Code != 200 || view.Room.Addr() != far.ID+"@"+awayKey || view.Room.Elsewhere != "away" || view.Watch.Key != far.ID+"@"+awayKey {
		t.Fatalf("the page: %d %+v", rec.Code, view)
	}
	if got := f.ranAt("chat@away", roomReadName); len(got) != 1 || !strings.Contains(got[0], `"thread_id":"`+far.ID+`"`) {
		t.Errorf("the read goes to chat@away with the thread: %v", got)
	}
	var note string
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(entriesView); ok {
			note = v.Error
		}
		return template.HTML(name), nil
	}
	form := url.Values{"raw_args": {`{"message":"hello far"}`}, "after": {"0"}}
	req = httptest.NewRequest(http.MethodPost, "/chat/room/"+far.ID+"@away/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.SetPathValue("id", far.ID+"@"+awayKey)
	if rec := as(c, sm, c.postMessage, req); rec.Code != 200 || note != "Not delivered to remy@"+client.ShortKey(kernelOf(pr("remy@nowhere")))+"." {
		t.Fatalf("the post and whom it missed: %d %q", rec.Code, note)
	}
	if got := f.ranAt("chat@away", postName); len(got) != 1 || got[0] != `{"message":"hello far","thread_id":"`+far.ID+`"}` {
		t.Errorf("the post goes to chat@away: %v", got)
	}
	if len(f.ranAt(home, postName)) != 0 {
		t.Error("nothing is posted at home")
	}
	req = httptest.NewRequest(http.MethodPost, "/chat/room/"+far.ID+"/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.SetPathValue("id", far.ID)
	if rec := as(c, sm, c.postMessage, req); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a bare id names no room: %d", rec.Code)
	}
}

// A failure of a chat elsewhere relays no status, so it cannot say the room is
// gone: the page backs off and retries. The room is gone only when that chat's
// own list answers without it.
func TestAFailureElsewhereIsNotFinal(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	f.requesters["chat@away"] = "pending"
	connect(t, c, sm, farChat)
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet")})
	st := open(t, c, sm, far.ID+"@"+awayKey)
	var next watchState
	c.deps.Fragment = func(name string, data any) (template.HTML, error) {
		if v, ok := data.(watchView); ok {
			next = v.State
		}
		return template.HTML(name), nil
	}
	f.awayDown = true
	if h, _ := tick(c, sm, far.ID+"@"+awayKey, watchState{After: st.After, Signal: "stale"}); h != "_chat_watch" || next.Fails != 1 {
		t.Fatalf("a chat elsewhere failing backs off: %s %+v", h, next)
	}
	f.awayDown = false
	if err := away.Store.RemoveMember(pr("bob@away"), far.ID, pr("ann@testnet")); err != nil {
		t.Fatal(err)
	}
	if h, err := tick(c, sm, far.ID+"@"+awayKey, watchState{After: st.After, Signal: "stale", Fails: 1}); h != "" || !errors.Is(err, apps.Moved("/chat")) {
		t.Errorf("its list confirms the room gone: %s %v", h, err)
	}
}

// A post in a room elsewhere holds the poster's process open here until its
// deliveries there are answered; the process closing is what the page sees.
func TestAnOpenProcessMovesTheSignal(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	var signals []string
	for _, procs := range [][]string{{"p1"}, nil} {
		f.procs = procs
		inSession(c, sm, func(w http.ResponseWriter, r *http.Request) {
			p, _ := c.pulse(r)
			signals = append(signals, c.signal("ann@testnet", p))
		}, httptest.NewRequest(http.MethodGet, "/tick", nil))
	}
	if signals[0] == signals[1] {
		t.Error("a process that closes moves the signal")
	}
}

// A room's address is its thread at the kernel whose chat holds it; anything
// else names no room.
func TestRoomAddresses(t *testing.T) {
	if s, th, ok := splitRoom("far-owl-1@away"); !ok || s != "chat@away" || th != "far-owl-1" || roomAddr(s, th) != "far-owl-1@away" {
		t.Errorf("an address: %q %q %v", s, th, ok)
	}
	for _, bad := range []string{"far-owl-1", "@away", "far@", "a@b@c", "a@b/c"} {
		if _, _, ok := splitRoom(bad); ok {
			t.Errorf("%q names no room", bad)
		}
	}
}

// A chat whose contract moved since the kernel cached it is refused once, before
// anything runs, and the next call re-resolves it: the post runs again and lands.
// A post the kernel fails for any other reason says so beside the composer.
func TestAMovedContractRunsOnceMore(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	room, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", nil)
	send := func() *httptest.ResponseRecorder {
		form := url.Values{"raw_args": {`{"message":"hello"}`}, "after": {"0"}}
		req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(room.ID)+"/message", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		req.SetPathValue("id", addr(room.ID))
		return as(c, sm, c.postMessage, req)
	}
	f.stale = map[string]bool{home + "/" + postName: true}
	if rec := send(); rec.Code != 200 || len(f.ran(postName)) != 1 {
		t.Fatalf("a moved contract runs once more: %d %v", rec.Code, f.ran(postName))
	}
	f.failRuns = true
	var line string
	c.deps.RenderFragment = func(w http.ResponseWriter, status int, name string, data any) error {
		if v, ok := data.(entriesView); ok {
			line = v.Error
		}
		w.WriteHeader(status)
		return nil
	}
	if rec := send(); rec.Code != http.StatusUnprocessableEntity || line == "" {
		t.Errorf("a failure is said beside the composer, never as a page in the room: %d %q", rec.Code, line)
	}
}

// An invitation is a row on the rail, from chat's own list, whose menu accepts
// or declines it. Accept runs room/join, clears the invitation's task and opens
// the room; Decline runs room/leave, so the inviter sees it declined, and
// declines the task. An invitation chat withdrew since is said withdrawn, its
// task cleared. A local agent added to a room is seated in the same step.
func TestInvitations(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	f.requesters[home] = "approved"
	bob := joinedAt(t, f.svc, "bob@testnet")
	invite := func(name string) string {
		rec := f.dispatch("/room/create", bob, map[string]any{"members": []string{"ann@testnet"}, "name": name})
		var out struct {
			Room svc.Room `json:"room"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Room.ID
	}
	taskOf := func(room string) string {
		for _, tk := range f.taskBodies() {
			if tk["action"] == home+"/accept" && tk["partial_args"].(map[string]any)["thread_id"] == room {
				return tk["id"].(string)
			}
		}
		return ""
	}
	tea, cake, pie := invite("tea"), invite("cake"), invite("pie")

	h, _ := tick(c, sm, railKey, watchState{Signal: "old"})
	got := string(h)
	if !strings.Contains(got, `<button class="rail-item unread" data-menu="menu-invite-`+addr(tea)+`" aria-haspopup="menu" aria-expanded="false" title="bob@testnet">`) ||
		!strings.Contains(got, `action="/chat/invites/`+addr(tea)+`/accept"`) || !strings.Contains(got, `data-confirm="Decline tea?"`) || strings.Contains(got, "rail-card") {
		t.Fatalf("an invitation is a row whose menu accepts or declines it: %s", got)
	}

	if rec := invitation(c, sm, addr(tea), "accept", ""); rec.Code != 303 || rec.Header().Get("Location") != "/chat/room/"+addr(tea) {
		t.Fatalf("accept opens the room: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Header().Get("X-Flash"))
	}
	if rooms, _ := f.svc.Store.Rooms(pr("ann@testnet")); len(rooms) != 1 || rooms[0].ID != tea {
		t.Errorf("accept joins that room alone: %+v", rooms)
	}
	if got := f.ran(roomJoinName); len(got) != 1 || !strings.Contains(got[0], tea) {
		t.Errorf("accept is room/join, run as ann: %v", got)
	}
	if slices.Contains(f.openTaskIDs(), taskOf(tea)) {
		t.Error("the accepted invitation's task is cleared")
	}

	if rec := invitation(c, sm, addr(cake), "decline", "/chat/room/"+addr(tea)); rec.Code != 200 || goesTo(rec) != "" {
		t.Fatalf("decline answers in place: %d %s %s", rec.Code, goesTo(rec), rec.Header().Get("X-Flash"))
	}
	if room, _, _ := f.svc.Store.Room(pr("bob@testnet"), cake); len(room.Invited) != 0 || len(room.Members) != 1 {
		t.Errorf("the inviter sees it declined: %+v", room)
	}
	if !slices.Contains(f.cancels, taskOf(cake)) {
		t.Errorf("the declined invitation's task is declined: %v", f.cancels)
	}

	if err := f.svc.Store.RemoveMember(pr("bob@testnet"), pie, pr("ann@testnet")); err != nil {
		t.Fatal(err)
	}
	rec := invitation(c, sm, addr(pie), "accept", "/chat/room/"+addr(tea))
	if !strings.Contains(rec.Header().Get("X-Flash"), "That invitation was withdrawn.") || rec.Header().Get("Location") != "/chat/room/"+addr(tea) {
		t.Errorf("a withdrawn invitation is said, on the page it was sent from: %s %s", rec.Header().Get("X-Flash"), rec.Header().Get("Location"))
	}
	if !slices.Contains(f.cancels, taskOf(pie)) {
		t.Errorf("a withdrawn invitation's task is cleared: %v", f.cancels)
	}
	if rooms, _ := f.svc.Store.Rooms(pr("ann@testnet")); len(rooms) != 1 {
		t.Errorf("a withdrawn invitation joins nothing: %+v", rooms)
	}

	agentHome(t, c, "helper@testnet", "AH")
	f.mes["AH"] = `{"address":{"address":"helper@testnet","principal":"helper@testnet"},"connectors":[]}`
	create := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=help&agent=helper%40testnet"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := as(c, sm, c.postCreateRoom, create); rec.Code != 303 || rec.Header().Get("X-Flash") != "" {
		t.Fatalf("create with an agent: %d %s", rec.Code, rec.Header().Get("X-Flash"))
	}
	if rooms, _ := f.svc.Store.Rooms(pr("helper@testnet")); len(rooms) != 1 || rooms[0].Name != "help" {
		t.Errorf("the agent is seated in the same step: %+v", rooms)
	}
	for _, tk := range f.taskBodies() {
		if tk["required_caller"] == pr("helper@testnet") && tk["status"] == "waiting" {
			t.Errorf("the agent's invitation task is cleared, so its daemon is not handed it: %v", tk)
		}
	}
}

// A failure on a chat elsewhere destroys nothing: with that chat failing every
// run, Accept and Decline say so on the page they were sent from, decline no
// task and leave the member invited, and the same click works once it answers.
// A withdrawal elsewhere is confirmed by that chat's list alone, which holds the
// invitations: a room absent from the rooms proves nothing. A failed Connect
// leaves a chat that was asking as it was.
func TestAFailureElsewhereDestroysNothing(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	f.requesters["chat@away"] = "pending"
	f.awayDown = true
	if rec := chatVerbFrom(c, sm, farChat, "connect", "/settings"); !strings.Contains(rec.Header().Get("X-Flash"), "chat@away refused or failed") || rec.Header().Get("Location") != "/settings" {
		t.Errorf("a failed Connect is said on the page it was sent from: %d %s %s", rec.Code, rec.Header().Get("X-Flash"), rec.Header().Get("Location"))
	}
	if f.requesters["chat@away"] != "pending" {
		t.Errorf("a failed Connect leaves the chat asking: %v", f.requesters)
	}
	f.awayDown = false
	connect(t, c, sm, farChat)
	bob := joinedAt(t, away, "bob@away")
	var out struct {
		Room svc.Room `json:"room"`
	}
	_ = json.Unmarshal(f.dispatchAt(away, "chat@away", "/room/create", bob, map[string]any{"members": []string{"ann@testnet"}, "name": "far"}).Body.Bytes(), &out)
	far := out.Room.ID + "@" + awayKey
	cancelled := len(f.cancels)

	f.awayDown = true
	for _, verb := range []string{"accept", "decline"} {
		rec := invitation(c, sm, far, verb, "/chat")
		if !strings.Contains(rec.Header().Get("X-Flash"), "chat@away refused or failed") || strings.Contains(rec.Header().Get("X-Flash"), "withdrawn") {
			t.Errorf("%s with chat@away failing says so: %s", verb, rec.Header().Get("X-Flash"))
		}
	}
	if room, _, _ := away.Store.Room(pr("bob@away"), out.Room.ID); len(room.Invited) != 1 || len(f.cancels) != cancelled {
		t.Errorf("nothing is destroyed: %+v, %d declined", room, len(f.cancels)-cancelled)
	}

	f.awayDown = false
	f.failNext = map[string]int{"chat@away/" + roomLeaveName: 1} // one leave fails, the chat still answering
	if rec := invitation(c, sm, far, "decline", "/chat"); strings.Contains(rec.Header().Get("X-Flash"), "withdrawn") || len(f.cancels) != cancelled {
		t.Errorf("a failed leave of a standing invitation is not taken for done: %s, %d declined", rec.Header().Get("X-Flash"), len(f.cancels)-cancelled)
	}
	if rec := invitation(c, sm, far, "accept", "/chat"); goesTo(rec) != "/chat/room/"+far {
		t.Errorf("the same click works once the chat answers: %d %s %s", rec.Code, goesTo(rec), rec.Header().Get("X-Flash"))
	}
}

// A chat on a kernel this one has given no name asks by its key: its row shows
// the name the kernel gives itself as ~name, the whole address in its title and
// its menu's Copy address, connects it by that address, and asks Block naming
// the key. Nothing is dialled to name it: only an operator names a kernel.
func TestAChatAskingByKeyIsShownByKey(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	withAway(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	f.requesters[home] = "approved"
	f.requesters["chat@"+awayKey] = "pending"
	h, _ := tick(c, sm, railKey, watchState{Signal: "old"})
	if got := string(h); !strings.Contains(got, `<span>chat@~away<span class="sr-only">: chat@`+awayKey) || !strings.Contains(got, `title="chat@`+awayKey+`"`) ||
		!strings.Contains(got, `data-copy="chat@`+awayKey+`">Copy address`) || !strings.Contains(got, `data-confirm="Block ~away `+client.ShortKey(awayKey)+`?"`) ||
		!strings.Contains(got, `action="/chat/chats/chat@`+awayKey+`/connect"`) || len(f.refs) != 0 {
		t.Fatalf("the row shows the key and the kernel's own name, and connects it by key: %s", got)
	}
}

// An invitation the kernel could not deliver yet keeps its member, invited, and
// the page says whom it did not reach yet, with the composer's sentence, where
// 0.71.0 dropped the member.
func TestAnInvitationNotDeliveredKeepsTheMember(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	create := httptest.NewRequest(http.MethodPost, "/chat/rooms", strings.NewReader("name=far&members=bob%40testnet+remy%40nowhere"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := as(c, sm, c.postCreateRoom, create)
	if rec.Code != 303 || rec.Header().Get("X-Flash") != "info: Not delivered to remy@nowhere." || !strings.HasPrefix(rec.Header().Get("Location"), "/chat/room/") {
		t.Fatalf("the room starts and says whom it did not reach: %d %s %s", rec.Code, rec.Header().Get("X-Flash"), rec.Header().Get("Location"))
	}
	rooms, _ := f.svc.Store.Rooms(pr("ann@testnet"))
	if len(rooms) != 1 || names(rooms[0].Invited) != "bob@testnet,remy@nowhere" {
		t.Fatalf("both are kept, invited: %+v", rooms)
	}
	id := rooms[0].ID
	req := httptest.NewRequest(http.MethodPost, "/chat/room/"+addr(id)+"/add-member", strings.NewReader("member=cy%40nowhere"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", addr(id))
	if rec := as(c, sm, c.postAddMember, req); rec.Header().Get("X-Flash") != "info: Not delivered to cy@nowhere." || rec.Header().Get("Location") != "/chat/room/"+addr(id)+"?form=members" {
		t.Errorf("adding says so too, the dialog open: %s %s", rec.Header().Get("X-Flash"), rec.Header().Get("Location"))
	}
}

// An invitation task left open for a room the viewer is in, after a join whose
// answer came late, is settled when the room is read, as a delivery is.
func TestALingeringInvitationIsSettledByARead(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	f.requesters[home] = "approved"
	bob := joinedAt(t, f.svc, "bob@testnet")
	var out struct {
		Room svc.Room `json:"room"`
	}
	_ = json.Unmarshal(f.dispatch("/room/create", bob, map[string]any{"members": []string{"ann@testnet"}, "name": "late"}).Body.Bytes(), &out)
	if err := f.svc.Store.Join(out.Room.ID, pr("ann@testnet")); err != nil { // the join landed; its task stayed open
		t.Fatal(err)
	}
	if len(f.openTaskIDs()) != 1 {
		t.Fatalf("one invitation task open: %v", f.openTaskIDs())
	}
	open(t, c, sm, out.Room.ID)
	if ids := f.openTaskIDs(); len(ids) != 0 {
		t.Errorf("reading the room settles it: %v", ids)
	}
}

// A chat is identified by its kernel's key, so naming that kernel, the
// operator's act, changes how the chat is shown and nothing else: a room opened
// while the kernel had no name opens at the same address once it has one.
func TestNamingAKernelKeepsItsRooms(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	f.requesters[farChat] = "pending" // a kernel this one has not named
	connect(t, c, sm, farChat)
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet")})
	away.Store.Post(far.ID, pr("bob@away"), raw("before"))
	room := far.ID + "@" + awayKey
	if st := open(t, c, sm, room); st.After == 0 {
		t.Fatal("the room opens while its kernel has no name")
	}
	delete(f.requesters, farChat) // the operator names it away
	f.requesters["chat@away"] = "approved"
	if st := open(t, c, sm, room); st.After == 0 {
		t.Error("the same room address opens once its kernel is named")
	}
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Fragment = rnd.HTML
	h, _ := tick(c, sm, railKey, watchState{Signal: "old"})
	if got := string(h); !strings.Contains(got, `href="/chat/room/`+room+`"`) || !strings.Contains(got, `<span class="muted">· away</span>`) {
		t.Errorf("the rail keeps the address and shows the name: %s", got)
	}
}

// An upgrade of a chat elsewhere changes its contracts, and the kernel holding
// it revokes the grants on them while the viewer's kernel still lists them: a
// run refused so grants the key kept again, which keeps the member, and joins
// nothing.
func TestAnUpgradeOfAChatElsewhereGrantsAgain(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	away := withAway(t, f)
	f.requesters["chat@away"] = "pending"
	connect(t, c, sm, farChat)
	far, _ := seated(away.Store, pr("bob@away"), "far", []string{pr("ann@testnet")})
	f.revokedThere = map[string]bool{"chat@away/" + roomReadName: true}
	open(t, c, sm, far.ID+"@"+awayKey)
	if len(f.regrants) != 1 || len(f.ranAt("chat@away", joinName)) != 1 || len(f.ranAt("chat@away", roomReadName)) != 1 {
		t.Errorf("the run grants the key again, joins nothing, and reads the room: %v %v %v", f.regrants, f.ranAt("chat@away", joinName), f.ranAt("chat@away", roomReadName))
	}
	// A grant again that fails for want of an answer is the run's failure: a
	// join anew would replace the key kept, and with it a member of an earlier
	// store that only that key binds.
	f.revokedThere = map[string]bool{"chat@away/" + roomReadName: true}
	f.failStart = true
	grants := len(f.grants)
	var err error
	as(c, sm, func(w http.ResponseWriter, r *http.Request) {
		_, err = c.readRoom(r, farChat, far.ID, latest, false)
	}, httptest.NewRequest(http.MethodGet, "/", nil))
	if err == nil || len(f.grants) != grants || len(f.ranAt("chat@away", joinName)) != 1 {
		t.Errorf("a failed grant again joins nothing and connects no key: %v, %d connects, %v", err, len(f.grants)-grants, f.ranAt("chat@away", joinName))
	}
}

// Two chats whose kernels both call themselves away are told apart: the first
// is ~away alone while it claims the nickname alone, and once a second key
// claims it each row names its key.
func TestTwoChatsClaimingOneNicknameNameTheirKeys(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, _ := testChat(t, f)
	rnd, err := server.NewRenderer(nil, c.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c.deps.Label = rnd.Label
	other := kernelKey("other")
	a := c.learn(client.Party{Address: "chat@" + awayKey, Principal: "c1@" + awayKey, Nickname: "away"})
	if got := c.label(a); got != "~away" {
		t.Errorf("one claimant: %q", got)
	}
	b := c.learn(client.Party{Address: "chat@" + other, Principal: "c2@" + other, Nickname: "away"})
	if got := c.label(b); got != "~away "+client.ShortKey(other) {
		t.Errorf("the second claimant: %q", got)
	}
	if got := c.label(a); got != "~away "+client.ShortKey(awayKey) {
		t.Errorf("after the clash the first claimant: %q", got)
	}
}

// The chat page is a view of chat's replies and names every party as the
// viewer's kernel does, asking it once in a while: chat's spelling of a member
// is chat's data. A principal the viewer's kernel cannot name is spelled by
// key, with the nickname its kernel gives itself. What a
// post's first word addresses stays chat's spelling, since chat routes by it.
func TestTheViewerNamesMembersByItsKernel(t *testing.T) {
	f := newFakeKernel()
	defer f.close()
	c, sm := testChat(t, f)
	f.requesters[home] = "approved"
	gone := pr("cy@nowhere")
	f.names = map[string]client.Party{pr("bob@testnet"): {Address: "bob@" + awayKey, Principal: pr("bob@testnet"), Nickname: "home"}}
	room, _ := seated(f.svc.Store, pr("ann@testnet"), "cafe", []string{pr("bob@testnet")})
	bob := f.joined(t, "bob@testnet")
	f.dispatch("/msg/post", bob, map[string]any{"thread_id": room.ID, "message": "hello"})
	var view roomView
	c.deps.RenderPage = func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
		view = data.(roomView)
	}
	get := func() {
		req := httptest.NewRequest(http.MethodGet, "/chat/room/"+addr(room.ID), nil)
		req.SetPathValue("id", addr(room.ID))
		as(c, sm, c.getRoom, req)
	}
	get()
	var shown []string
	for _, m := range view.Members.Room.Members {
		shown = append(shown, m.Label())
	}
	if !slices.Contains(shown, "bob@~home") || view.Entries.Entries[len(view.Entries.Entries)-1].Author.Label() != "bob@~home" {
		t.Errorf("members and authors as the viewer's kernel names them: %v", shown)
	}
	if !slices.Equal(view.Others, []string{"bob@testnet"}) {
		t.Errorf("a post addresses a member in chat's spelling: %v", view.Others)
	}
	asked := len(f.resolved)
	get()
	if len(f.resolved) != asked {
		t.Errorf("a name given lately is not asked again: %v", f.resolved)
	}
	ps := []*client.Party{{Address: "cy@far", Principal: gone, Nickname: "far"}}
	inSession(c, sm, func(w http.ResponseWriter, r *http.Request) { c.name(r, ps) }, httptest.NewRequest(http.MethodGet, "/chat", nil))
	if got := ps[0].Label(); got != "cy@~far" || ps[0].Address != "cy@"+kernelOf(gone) {
		t.Errorf("a principal the viewer's kernel cannot name is spelled by key: %+v", ps[0])
	}
}
