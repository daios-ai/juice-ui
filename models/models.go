// Package models is the language-model setup of this installation's kernel
// (requirements.md §1, §6.6): its chat model, which answers the kernel's chat,
// JSON and decide natives, its embedding model, and the keys they take. The kernel holds the answer in config.json as `native.llm`, a
// flat list of model entries and four bindings (kernel requirements D17);
// this package reads that, asks a provider which models it serves, and
// writes the setup back in the kernel's own shape. A provider is not a
// kernel concept: it is an address and a wire protocol, kept here as data.
//
// It imports only the standard library, so the installer's entrypoint and
// the application reach it alike.
package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider is an address that serves models over one of the kernel's wire
// protocols, `openai` (the OpenAI-compatible API) or `anthropic`.
type Provider struct {
	ID, Name, Protocol, URL string
	Key                     bool // asks for a key
	Local                   bool // runs on this machine: asked for its models whenever the form is shown
}

// The choices that are not a listed provider.
const (
	None  = ""      // no model: the role is left unbound
	Other = "other" // an OpenAI-compatible address the person types; "other-anthropic" is the Anthropic-compatible one
)

// Providers are the providers offered, in the order offered. Each only fills
// in an address and a protocol; one with no address takes any, typed (an
// OpenAI- or Anthropic-compatible gateway or proxy).
var Providers = []Provider{
	{ID: "ollama", Name: "Ollama on this machine", Protocol: "openai", URL: "http://localhost:11434/v1", Local: true},
	{ID: "lmstudio", Name: "LM Studio on this machine", Protocol: "openai", URL: "http://localhost:1234/v1", Local: true},
	{ID: "openai", Name: "OpenAI", Protocol: "openai", URL: "https://api.openai.com/v1", Key: true},
	{ID: "anthropic", Name: "Anthropic", Protocol: "anthropic", URL: "https://api.anthropic.com/v1", Key: true},
	{ID: "openrouter", Name: "OpenRouter", Protocol: "openai", URL: "https://openrouter.ai/api/v1", Key: true},
	{ID: Other, Name: "Other (OpenAI-compatible)", Protocol: "openai"},
	{ID: "other-anthropic", Name: "Other (Anthropic-compatible)", Protocol: "anthropic", Key: true},
}

func provider(id string) (Provider, bool) {
	i := slices.IndexFunc(Providers, func(p Provider) bool { return p.ID == id })
	if i < 0 {
		return Provider{}, false
	}
	return Providers[i], true
}

// embeds reports a protocol that serves embedding models: the kernel's
// anthropic adaptor has none, and refuses an embedding entry on it.
func embeds(protocol string) bool { return protocol != "anthropic" }

// Choice is one block of the form: a provider and the model it serves.
type Choice struct {
	Provider string // a Providers ID, or None
	URL      string // the address typed, for a provider that has none
	Key      string // the key; empty when the provider needs none or none is held
	ID       string // the provider's name for the model
	Price    int64  // what a call costs its caller, in base units
}

// Setup is the whole answer: the chat model, which backs chat, JSON and
// decide, and the embedding model.
type Setup struct {
	Chat, Embed Choice
	Priced      bool // the prices were given, so they are written
}

// address is where a choice's model is served: its protocol, address and key.
func (s Setup) address(c Choice) (protocol, addr, key string) {
	p, ok := provider(c.Provider)
	if !ok {
		return "", "", c.Key
	}
	addr = p.URL
	if addr == "" {
		addr = c.URL
	}
	return p.Protocol, strings.TrimRight(addr, "/"), c.Key
}

// Read is a post's answers as typed. Prices are the caller's to read, since
// only it knows the world's money unit.
func Read(f url.Values) Setup {
	c := func(p string) Choice {
		return Choice{
			Provider: f.Get(p + "_provider"),
			URL:      strings.TrimSpace(f.Get(p + "_url")),
			Key:      strings.TrimSpace(f.Get(p + "_key")),
			ID:       strings.TrimSpace(f.Get(p + "_model")),
		}
	}
	return Setup{Chat: c("chat"), Embed: c("embed")}
}

// Keep fills each block's empty key from held where both are served at the
// same address, so a key is typed once: a key is never sent back to a page,
// and a blank key input keeps the one already held. A key typed in the other
// block for the same address serves too: a provider's key is one key.
func (s *Setup) Keep(held Setup) {
	typed := *s
	for i, c := range []*Choice{&s.Chat, &s.Embed} {
		if c.Key == "" {
			p, a, _ := s.address(*c)
			if c.Key = held.keyAt(p, a, i); c.Key == "" {
				c.Key = typed.keyAt(p, a, i)
			}
		}
	}
}

// keyAt is the key held for an address, or "": the given block's own first,
// so two blocks served at one address with different keys keep theirs.
func (s Setup) keyAt(protocol, addr string, block int) string {
	cs := []Choice{s.Chat, s.Embed}
	if block == 1 {
		cs[0], cs[1] = cs[1], cs[0]
	}
	for _, c := range cs {
		if p, a, k := s.address(c); k != "" && p == protocol && a == addr {
			return k
		}
	}
	return ""
}

// ErrNoList is a provider that serves no list of its models: the model typed
// is then taken as it stands.
var ErrNoList = errors.New("the provider lists no models")

// probe is the client a provider is asked with. It follows no redirect,
// which would carry the key elsewhere, as the kernel follows none.
var probe = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// List is the provider's model ids, asked as the kernel asks at its boot:
// GET <address>/models, the key as a bearer on openai and as x-api-key with
// the API version on anthropic.
func List(ctx context.Context, protocol, addr, key string) ([]string, error) {
	u := strings.TrimRight(addr, "/") + "/models"
	if protocol == "anthropic" {
		u += "?limit=1000" // the API pages; one page holds them all
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%s is not an address", addr)
	}
	if protocol == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := probe.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s does not answer: %v", addr, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoList
	case resp.StatusCode/100 != 2:
		return nil, errors.New(refusal(resp.StatusCode, body))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%s answers with no list of models", addr)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// refusal is a provider's own words for refusing: the message of its error
// object (OpenAI, Anthropic), its error string (Ollama), else its status.
func refusal(status int, body []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.Error) > 0 {
		var o struct {
			Message string `json:"message"`
		}
		var s string
		if json.Unmarshal(e.Error, &o) == nil && o.Message != "" {
			return o.Message
		}
		if json.Unmarshal(e.Error, &s) == nil && s != "" {
			return s
		}
	}
	return "it answers " + strconv.Itoa(status) + " " + http.StatusText(status)
}

// Check refuses a setup the kernel could not use, with what to do: a block
// left incomplete, an embedding model on a protocol without them, or a
// provider that does not answer, refuses its key or does not serve the model.
// Both blocks are read before any provider is asked.
func Check(ctx context.Context, s Setup) error {
	type block struct {
		c                   Choice
		role                string
		protocol, addr, key string
	}
	var asks []block
	for _, b := range []struct {
		c     Choice
		embed bool
	}{{s.Chat, false}, {s.Embed, true}} {
		c := b.c
		if c.Provider == None {
			continue
		}
		role := "chat"
		if b.embed {
			role = "embedding"
		}
		protocol, addr, key := s.address(c)
		if protocol == "" {
			return fmt.Errorf("Choose a provider for the %s model.", role)
		}
		if b.embed && !embeds(protocol) {
			return fmt.Errorf("%s serves no embedding models; choose another provider for the embedding model.", s.name(c))
		}
		if u, err := url.Parse(addr); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("Give the provider's address, starting with http:// or https://.")
		}
		if c.ID == "" {
			return fmt.Errorf("Choose a %s model.", role)
		}
		if p, _ := provider(c.Provider); p.Key && key == "" {
			return fmt.Errorf("%s needs a key.", s.name(c))
		}
		asks = append(asks, block{c, role, protocol, addr, key})
	}
	for _, b := range asks {
		ids, err := List(ctx, b.protocol, b.addr, b.key)
		switch {
		case errors.Is(err, ErrNoList):
		case err != nil:
			return fmt.Errorf("%s: %s.", s.name(b.c), strings.TrimSuffix(err.Error(), "."))
		case !slices.Contains(ids, b.c.ID):
			return fmt.Errorf("%s serves no model %s.", s.name(b.c), b.c.ID)
		}
	}
	return nil
}

// name is how a choice's provider is called in a sentence.
func (s Setup) name(c Choice) string {
	if p, ok := provider(c.Provider); ok && p.URL != "" {
		return p.Name
	}
	_, addr, _ := s.address(c)
	return addr
}

// Form is what the form renders: both blocks, as chosen, with what each
// provider was found to serve.
type Form struct{ Chat, Embed Block }

// Block is the block a field prefix names: "embed" the embedding block, any
// other the chat block.
func (f Form) Block(field string) Block {
	if field == "embed" {
		return f.Embed
	}
	return f.Chat
}

// Refresh names the address a host answers a block's model field from, so a
// change of provider, address or key lists that provider's models in place.
func (f *Form) Refresh(addr string) { f.Chat.Refresh, f.Embed.Refresh = addr, addr }

// Block is one block of the form.
type Block struct {
	Field    string // the inputs' prefix: chat_…, embed_…
	Options  []Option
	Provider string // the chosen option
	URL, ID  string
	Price    int64
	Priced   bool     // the price is asked (the Models page)
	Held     bool     // a key is held for the chosen provider
	Models   []string // the models the chosen provider lists: the model is chosen among them; nil, it is typed
	Unlisted bool     // the model chosen is not among them, as a provider may drop one; it stays chosen
	Problem  string   // why the chosen provider could not be asked
	Refresh  string   // where the host answers this block's model field
	Summary  string   // the choice in a sentence: the model and its provider
	Action   string   // the native the block backs, on the page: its address
	Href     string   // and its page
	// The options each part of the block belongs to, space-separated: the
	// reveal shows a part only for those (grammar §5.4).
	ForURL, ForKey, ForModel string
}

// Option is one provider in a block's list.
type Option struct {
	ID, Name, Note string // Note: what a provider on this machine serves, or that it is not running
}

// Shown reports a part of the block shown for the chosen option.
func (b Block) Shown(forList string) bool {
	return slices.Contains(strings.Fields(forList), b.Provider)
}

// View is the form for s: each block's options, and the models its chosen
// provider serves, asked of it now with the key typed or held; the providers
// on this machine are asked too, so their options say whether they run. A
// block says a key is held only where held, what the server keeps between
// requests, has one for its address: a key typed and not kept is typed again.
func View(ctx context.Context, s, held Setup) Form {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	type ask struct{ protocol, addr, key string }
	type answer struct {
		ids []string
		err error
	}
	asks := map[ask]*answer{}
	for _, p := range Providers {
		if p.Local {
			asks[ask{p.Protocol, p.URL, ""}] = nil
		}
	}
	// A provider that needs a key is asked only once one is held: without it
	// the answer is a refusal, already known.
	for i, c := range []Choice{s.Chat, s.Embed} {
		if p, a, k := s.address(c); p != "" && a != "" && (k != "" || !needsKey(c)) && (i == 0 || embeds(p)) {
			asks[ask{p, a, k}] = nil
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for q := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := List(ctx, q.protocol, q.addr, q.key)
			mu.Lock()
			asks[q] = &answer{ids, err}
			mu.Unlock()
		}()
	}
	wg.Wait()

	options := func(embedding bool) []Option {
		var out []Option
		for _, p := range Providers {
			if embedding && !embeds(p.Protocol) {
				continue
			}
			o := Option{ID: p.ID, Name: p.Name}
			if p.Local {
				switch a := asks[ask{p.Protocol, p.URL, ""}]; {
				case a.err == nil:
					o.Note = strconv.Itoa(len(a.ids)) + " models"
				case !errors.Is(a.err, ErrNoList):
					o.Note = "not running"
				}
			}
			out = append(out, o)
		}
		return append(out, Option{ID: None, Name: "None"})
	}
	block := func(field string, c Choice, embedding bool) Block {
		b := Block{Field: field, Options: options(embedding), Provider: c.Provider, URL: c.URL, ID: c.ID, Price: c.Price, Priced: s.Priced, Summary: s.Summary(c)}
		p, a, _ := s.address(c)
		b.Held = held.keyAt(p, a, map[string]int{"chat": 0, "embed": 1}[field]) != ""
		var url, key, model []string
		for _, o := range b.Options {
			p, _ := provider(o.ID)
			if p.URL == "" && o.ID != None {
				url = append(url, o.ID)
			}
			if p.Key || (p.URL == "" && o.ID != None) {
				key = append(key, o.ID)
			}
			if o.ID != None {
				model = append(model, o.ID)
			}
		}
		b.ForURL, b.ForKey, b.ForModel = strings.Join(url, " "), strings.Join(key, " "), strings.Join(model, " ")
		if p, a, k := s.address(c); p != "" && a != "" {
			if r := asks[ask{p, a, k}]; r != nil {
				switch {
				case r.err == nil && len(r.ids) > 0:
					b.Models = r.ids
					b.Unlisted = c.ID != "" && !slices.Contains(r.ids, c.ID)
				case !errors.Is(r.err, ErrNoList):
					b.Problem = s.name(c) + ": " + strings.TrimSuffix(r.err.Error(), ".") + "."
				}
			}
		}
		return b
	}
	return Form{Chat: block("chat", s.Chat, false), Embed: block("embed", s.Embed, true)}
}

// needsKey reports a choice whose provider asks for a key.
func needsKey(c Choice) bool {
	p, _ := provider(c.Provider)
	return p.Key
}

// Initial is a new machine's first choice: the first provider on this machine
// that runs, with the models the kernel would use by default where it serves
// them, read from the kernel's own schema of its configuration; otherwise no
// model. Nothing else is guessed.
func Initial(ctx context.Context, schema []byte) Setup {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	defaults := defaultIDs(schema)
	for _, p := range Providers {
		if !p.Local {
			continue
		}
		ids, err := List(ctx, p.Protocol, p.URL, "")
		if err != nil {
			continue
		}
		pick := func(kind string) string {
			for _, d := range defaults {
				if d.kind == kind && strings.TrimRight(d.url, "/") == p.URL && slices.Contains(ids, d.id) {
					return d.id
				}
			}
			return ""
		}
		s := Setup{Chat: Choice{Provider: p.ID, ID: pick("chat")}, Embed: Choice{Provider: p.ID, ID: pick("embed")}}
		if s.Embed.ID == "" {
			s.Embed = Choice{}
		}
		return s
	}
	return Setup{}
}

type defaultModel struct{ url, id, kind string }

// defaultIDs are the models the kernel's schema states as its default list.
func defaultIDs(schema []byte) []defaultModel {
	var s struct {
		Properties struct {
			Native struct {
				Properties struct {
					LLM struct {
						Properties struct {
							Models struct {
								Default []struct {
									URL  string `json:"url"`
									ID   string `json:"id"`
									Kind string `json:"kind"`
								} `json:"default"`
							} `json:"models"`
						} `json:"properties"`
					} `json:"llm"`
				} `json:"properties"`
			} `json:"native"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	var out []defaultModel
	for _, m := range s.Properties.Native.Properties.LLM.Properties.Models.Default {
		out = append(out, defaultModel{m.URL, m.ID, m.Kind})
	}
	return out
}

// Supported reports a kernel whose schema of its configuration describes the
// model entries this package writes. An older kernel kept its models in
// another shape, and its file is never written here.
func Supported(schema []byte) bool {
	var s struct {
		Properties struct {
			Native struct {
				Properties struct {
					LLM struct {
						Properties struct {
							Models struct {
								Items struct {
									Properties map[string]json.RawMessage `json:"properties"`
								} `json:"items"`
							} `json:"models"`
						} `json:"properties"`
					} `json:"llm"`
				} `json:"properties"`
			} `json:"native"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return false
	}
	props := s.Properties.Native.Properties.LLM.Properties.Models.Items.Properties
	for _, k := range []string{"name", "enabled", "protocol", "url", "key", "id", "kind", "price"} {
		if _, ok := props[k]; !ok {
			return false
		}
	}
	return true
}

// The four bindings: chat, json and decide name the chat model, embed the
// embedding model.
var chatRoles = []string{"chat", "json", "decide"}

// From is the setup the file holds: the entries its chat and embed bindings
// name.
func From(cfg map[string]any) Setup {
	llm := object(object(cfg, "native"), "llm")
	list := entries(llm)
	choice := func(binding string) (Choice, map[string]any) {
		name, _ := llm[binding].(string)
		i := slices.IndexFunc(list, func(e map[string]any) bool { return str(e, "name") == name })
		if name == "" || i < 0 {
			return Choice{}, nil
		}
		e := list[i]
		c := Choice{Provider: providerOf(str(e, "protocol"), str(e, "url")), Key: str(e, "key"), ID: str(e, "id"), Price: number(e["price"])}
		if p, _ := provider(c.Provider); p.URL == "" {
			c.URL = str(e, "url")
		}
		return c, e
	}
	chat, _ := choice("chat")
	embed, _ := choice("embed")
	return Setup{Chat: chat, Embed: embed}
}

// providerOf is the listed provider at an address, else the one of that
// protocol that takes any address, so an address the list does not hold — a
// gateway, a proxy — is kept as it is.
func providerOf(protocol, addr string) string {
	addr = strings.TrimRight(addr, "/")
	for _, p := range Providers {
		if p.URL != "" && p.Protocol == protocol && p.URL == addr {
			return p.ID
		}
	}
	for _, p := range Providers {
		if p.URL == "" && p.Protocol == protocol {
			return p.ID
		}
	}
	return Other
}

// Entry is a model entry of the file that the setup does not touch.
type Entry struct{ Name, ID, URL, Kind string }

// Others are the file's entries no binding names: models an operator added,
// kept as they are by every save.
func Others(cfg map[string]any) []Entry {
	llm := object(object(cfg, "native"), "llm")
	bound := bindings(llm)
	var out []Entry
	for _, e := range entries(llm) {
		if !bound[str(e, "name")] {
			out = append(out, Entry{Name: str(e, "name"), ID: str(e, "id"), URL: str(e, "url"), Kind: str(e, "kind")})
		}
	}
	return out
}

// Apply writes s into the decoded file cfg, in the kernel's own shape. The
// entries that were bound and that s no longer uses are removed, which is
// how a dead default leaves; an entry no binding named is kept as it is. An
// entry is reused only when it is the same model on the same protocol and
// address, of the same kind: it keeps its name, price and settings and is
// enabled. A new entry is named after its model, distinct from every other
// entry the file will hold. None leaves its roles unbound.
func Apply(cfg map[string]any, s Setup) {
	s.Keep(Setup{}) // a key typed in one block serves the other at its address
	native := object(cfg, "native")
	if native == nil {
		native = map[string]any{}
		cfg["native"] = native
	}
	llm := object(native, "llm")
	if llm == nil {
		llm = map[string]any{}
		native["llm"] = llm
	}
	old := entries(llm)
	bound := bindings(llm)

	type want struct {
		c     Choice
		kind  string
		entry map[string]any
	}
	wants := []*want{{c: s.Chat, kind: "chat"}, {c: s.Embed, kind: "embed"}}
	for _, w := range wants {
		if w.c.Provider == None {
			continue
		}
		protocol, addr, _ := s.address(w.c)
		for _, e := range old {
			if str(e, "protocol") == protocol && strings.TrimRight(str(e, "url"), "/") == addr && str(e, "id") == w.c.ID && str(e, "kind") == w.kind {
				w.entry = e
				break
			}
		}
	}
	var out []map[string]any
	for _, e := range old {
		if !bound[str(e, "name")] || slices.ContainsFunc(wants, func(w *want) bool { return w.entry != nil && sameEntry(w.entry, e) }) {
			out = append(out, e)
		}
	}
	for _, w := range wants {
		if w.c.Provider == None {
			continue
		}
		protocol, addr, key := s.address(w.c)
		if key == "" {
			key = addressKey(old, protocol, addr)
		}
		if w.entry == nil {
			w.entry = map[string]any{"name": free(w.c.ID, out), "protocol": protocol, "url": addr, "id": w.c.ID, "kind": w.kind, "price": w.c.Price}
			out = append(out, w.entry)
		}
		w.entry["enabled"] = true
		if key != "" {
			w.entry["key"] = key
		}
		if s.Priced {
			w.entry["price"] = w.c.Price
		}
	}
	list := make([]any, len(out))
	for i, e := range out {
		list[i] = e
	}
	llm["models"] = list
	chat, embed := "", ""
	if wants[0].entry != nil {
		chat = str(wants[0].entry, "name")
	}
	if wants[1].entry != nil {
		embed = str(wants[1].entry, "name")
	}
	for _, r := range chatRoles {
		llm[r] = chat
	}
	llm["embed"] = embed
}

func sameEntry(a, b map[string]any) bool { return str(a, "name") == str(b, "name") }

// addressKey is a key the file holds for an address: a key belongs to the
// provider, so a new model there needs none typed again.
func addressKey(list []map[string]any, protocol, addr string) string {
	for _, e := range list {
		if str(e, "protocol") == protocol && strings.TrimRight(str(e, "url"), "/") == addr && str(e, "key") != "" {
			return str(e, "key")
		}
	}
	return ""
}

// The kernel's rule for an entry's name.
const maxName = 32

var notName = regexp.MustCompile(`[^a-z0-9._-]+`)

// free is a name for a model id that no entry of list holds: the id in the
// kernel's alphabet, cut to its length, with the lowest free suffix.
func free(id string, list []map[string]any) string {
	base := strings.Trim(notName.ReplaceAllString(strings.ToLower(id), "-"), "-")
	if base == "" {
		base = "model"
	}
	taken := func(n string) bool {
		return slices.ContainsFunc(list, func(e map[string]any) bool { return str(e, "name") == n })
	}
	name := base[:min(len(base), maxName)]
	for n := 2; taken(name); n++ {
		suffix := "-" + strconv.Itoa(n)
		name = base[:min(len(base), maxName-len(suffix))] + suffix
	}
	return name
}

func bindings(llm map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, r := range append(slices.Clone(chatRoles), "embed") {
		if n, _ := llm[r].(string); n != "" {
			out[n] = true
		}
	}
	return out
}

func entries(llm map[string]any) []map[string]any {
	list, _ := llm["models"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func object(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	o, _ := m[key].(map[string]any)
	return o
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// number reads a JSON number decoded either way.
func number(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// Summary names a choice for a sentence: the model and its provider.
func (s Setup) Summary(c Choice) string {
	if c.Provider == None {
		return "None"
	}
	return c.ID + " · " + s.name(c)
}
