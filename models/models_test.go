package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// provider serves /models as an OpenAI-compatible or Anthropic API does, and
// records how it was asked.
func fakeProvider(t *testing.T, ids []string, wantKey string) (*httptest.Server, *http.Header) {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		if wantKey != "" && r.Header.Get("Authorization") != "Bearer "+wantKey && r.Header.Get("x-api-key") != wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
			return
		}
		data := []map[string]string{}
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestListAsksAsTheKernelAsks(t *testing.T) {
	srv, got := fakeProvider(t, []string{"b", "a", "a"}, "sk")
	ids, err := List(context.Background(), "openai", srv.URL+"/v1/", "sk")
	if err != nil || !slices.Equal(ids, []string{"a", "b"}) {
		t.Fatalf("ids %v err %v", ids, err)
	}
	if (*got).Get("Authorization") != "Bearer sk" {
		t.Errorf("openai key header: %v", *got)
	}
	if _, err := List(context.Background(), "anthropic", srv.URL+"/v1", "sk"); err != nil {
		t.Fatal(err)
	}
	if (*got).Get("x-api-key") != "sk" || (*got).Get("anthropic-version") != "2023-06-01" || (*got).Get("Authorization") != "" {
		t.Errorf("anthropic headers: %v", *got)
	}
	if _, err := List(context.Background(), "openai", srv.URL+"/v1", "wrong"); err == nil || err.Error() != "Incorrect API key provided" {
		t.Errorf("a refusal is the provider's words: %v", err)
	}
}

func TestListRefusals(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	if _, err := List(context.Background(), "openai", notFound.URL, ""); !errors.Is(err, ErrNoList) {
		t.Errorf("a 404 is no list: %v", err)
	}
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/models", http.StatusFound)
	}))
	defer moved.Close()
	if _, err := List(context.Background(), "openai", moved.URL, "sk"); err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("a redirect is not followed: %v", err)
	}
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model store unreadable"}`))
	}))
	defer ollama.Close()
	if _, err := List(context.Background(), "openai", ollama.URL, ""); err == nil || err.Error() != "model store unreadable" {
		t.Errorf("an error string is the provider's words: %v", err)
	}
	if _, err := List(context.Background(), "openai", "http://127.0.0.1:1", ""); err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Errorf("unreachable: %v", err)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) }))
	defer junk.Close()
	if _, err := List(context.Background(), "openai", junk.URL, ""); err == nil {
		t.Error("a page that is no list is refused")
	}
}

func TestReadAndKeep(t *testing.T) {
	f := url.Values{"chat_provider": {"openai"}, "chat_key": {" sk "}, "chat_model": {"gpt-5"},
		"embed_provider": {"other"}, "embed_url": {"http://h/v1/"}, "embed_model": {"e"}}
	s := Read(f)
	if s.Chat != (Choice{Provider: "openai", Key: "sk", ID: "gpt-5"}) || s.Embed.URL != "http://h/v1/" {
		t.Fatalf("read %+v", s)
	}
	// A blank key keeps the key held for the same address, and only there.
	held := Setup{Chat: Choice{Provider: "openai", Key: "held"}, Embed: Choice{Provider: Other, URL: "http://h/v1", Key: "hk"}}
	s.Chat.Key, s.Embed.Key = "", ""
	s.Keep(held)
	if s.Chat.Key != "held" || s.Embed.Key != "hk" {
		t.Errorf("kept %+v", s)
	}
	s = Setup{Chat: Choice{Provider: "openrouter"}}
	s.Keep(held)
	if s.Chat.Key != "" {
		t.Error("a key is never carried to another address")
	}
}

// file is a decoded config.json holding the kernel's first-boot defaults.
func file(t *testing.T, extra ...map[string]any) map[string]any {
	t.Helper()
	models := []any{
		map[string]any{"name": "gemma", "enabled": true, "protocol": "openai", "url": "http://localhost:11434/v1", "id": "gemma4:26b", "kind": "chat", "price": json.Number("0")},
		map[string]any{"name": "nomic", "enabled": true, "protocol": "openai", "url": "http://localhost:11434/v1", "id": "nomic-embed-text", "kind": "embed", "price": json.Number("0")},
	}
	for _, e := range extra {
		models = append(models, e)
	}
	return map[string]any{"kernel_handle": "k", "native": map[string]any{"lookup": map[string]any{"price": 0},
		"llm": map[string]any{"chat": "gemma", "json": "gemma", "decide": "gemma", "embed": "nomic", "models": models}}}
}

func llmOf(cfg map[string]any) map[string]any { return object(object(cfg, "native"), "llm") }

func names(cfg map[string]any) []string {
	var out []string
	for _, e := range entries(llmOf(cfg)) {
		out = append(out, str(e, "name"))
	}
	return out
}

func TestApplyOnAnEmptyFile(t *testing.T) {
	cfg := map[string]any{}
	Apply(cfg, Setup{Chat: Choice{Provider: "openai", Key: "sk", ID: "gpt-5"}, Embed: Choice{Provider: Same, ID: "text-embedding-3-small"}})
	l := llmOf(cfg)
	if l["chat"] != "gpt-5" || l["json"] != "gpt-5" || l["decide"] != "gpt-5" || l["embed"] != "text-embedding-3-small" {
		t.Fatalf("bindings %v", l)
	}
	es := entries(l)
	if len(es) != 2 || es[1]["protocol"] != "openai" || es[1]["url"] != "https://api.openai.com/v1" || es[1]["key"] != "sk" ||
		es[1]["kind"] != "embed" || es[1]["enabled"] != true || es[0]["price"] != int64(0) {
		t.Errorf("entries %v", es)
	}
	// None leaves every role unbound and no model behind.
	cfg = map[string]any{}
	Apply(cfg, Setup{})
	if l := llmOf(cfg); l["chat"] != "" || l["embed"] != "" || len(l["models"].([]any)) != 0 {
		t.Errorf("none: %v", l)
	}
}

func TestApplyReplacesTheDefaultsAndKeepsAnExtra(t *testing.T) {
	extra := map[string]any{"name": "sold", "enabled": true, "protocol": "openai", "url": "https://x/v1", "id": "big", "kind": "chat", "price": json.Number("7")}
	cfg := file(t, extra)
	Apply(cfg, Setup{Chat: Choice{Provider: "openrouter", Key: "k", ID: "openai/gpt-5"}})
	if got := names(cfg); !slices.Equal(got, []string{"sold", "openai-gpt-5"}) {
		t.Errorf("the dead defaults leave, the extra stays: %v", got)
	}
	if l := llmOf(cfg); l["embed"] != "" || l["chat"] != "openai-gpt-5" {
		t.Errorf("bindings %v", l)
	}
	if extra["price"] != json.Number("7") {
		t.Error("an extra is kept as it is")
	}
	if cfg["kernel_handle"] != "k" || object(object(cfg, "native"), "lookup") == nil {
		t.Error("nothing beside native.llm is touched")
	}
}

func TestApplyReusesOnlyTheSameModel(t *testing.T) {
	disabled := map[string]any{"name": "mine", "enabled": false, "protocol": "openai", "url": "https://api.openai.com/v1",
		"id": "gpt-5", "kind": "chat", "price": json.Number("9"), "max_tokens": json.Number("2048"), "key": "old"}
	otherKind := map[string]any{"name": "emb", "enabled": true, "protocol": "openai", "url": "https://api.openai.com/v1", "id": "gpt-5", "kind": "embed"}
	cfg := file(t, disabled, otherKind)
	Apply(cfg, Setup{Chat: Choice{Provider: "openai", ID: "gpt-5"}})
	if llmOf(cfg)["chat"] != "mine" || disabled["enabled"] != true || disabled["price"] != json.Number("9") ||
		disabled["max_tokens"] != json.Number("2048") || disabled["key"] != "old" {
		t.Errorf("reused entry: %v, bindings %v", disabled, llmOf(cfg))
	}
	// A typed key replaces the held one; prices are written only when given.
	Apply(cfg, Setup{Chat: Choice{Provider: "openai", ID: "gpt-5", Key: "new", Price: 3}, Priced: true})
	if disabled["key"] != "new" || disabled["price"] != int64(3) {
		t.Errorf("key and price: %v", disabled)
	}
	// The same model of the other kind is another entry, left alone.
	Apply(cfg, Setup{Chat: Choice{Provider: "openai", ID: "gpt-5"}, Embed: Choice{Provider: Same, ID: "gpt-5"}})
	if llmOf(cfg)["embed"] != "emb" {
		t.Errorf("the embed entry of that model is reused for search: %v", llmOf(cfg))
	}
	// Once nothing binds them, the entries that were bound leave.
	Apply(cfg, Setup{Chat: Choice{Provider: "openai", ID: "other-chat"}})
	if got := names(cfg); !slices.Equal(got, []string{"other-chat"}) {
		t.Errorf("entries %v", got)
	}
}

func TestApplyNamesAreDistinct(t *testing.T) {
	taken := map[string]any{"name": "gemma4-26b", "enabled": true, "protocol": "openai", "url": "https://y/v1", "id": "z", "kind": "chat"}
	cfg := file(t, taken)
	long := strings.Repeat("x", 40)
	Apply(cfg, Setup{Chat: Choice{Provider: Other, URL: "http://h/v1", ID: "gemma4:26b"}, Embed: Choice{Provider: Same, ID: "gemma4.26b"}})
	got := names(cfg)
	if !slices.Equal(got, []string{"gemma4-26b", "gemma4-26b-2", "gemma4.26b"}) {
		t.Errorf("names %v", got)
	}
	cfg = map[string]any{"native": map[string]any{"llm": map[string]any{"models": []any{
		map[string]any{"name": long[:32], "protocol": "openai", "url": "https://y/v1", "id": "q", "kind": "chat"}}}}}
	Apply(cfg, Setup{Chat: Choice{Provider: Other, URL: "http://h/v1", ID: long}, Embed: Choice{Provider: Same, ID: "Ünï/Code"}})
	got = names(cfg)
	if got[1] != long[:30]+"-2" || got[2] != "n-code" {
		t.Errorf("names %v", got)
	}
	for _, n := range got {
		if len(n) > maxName || notName.MatchString(n) {
			t.Errorf("the kernel refuses %q", n)
		}
	}
}

func TestFromTheFile(t *testing.T) {
	cfg := file(t)
	s := From(cfg)
	if s.Chat != (Choice{Provider: "ollama", ID: "gemma4:26b"}) || s.Embed != (Choice{Provider: Same, ID: "nomic-embed-text"}) {
		t.Errorf("from %+v", s)
	}
	Apply(cfg, Setup{Chat: Choice{Provider: "anthropic", Key: "ak", ID: "claude"}, Embed: Choice{Provider: Other, URL: "http://e/v1", ID: "e", Price: 4}, Priced: true})
	s = From(cfg)
	if s.Chat != (Choice{Provider: "anthropic", Key: "ak", ID: "claude"}) || s.Embed != (Choice{Provider: Other, URL: "http://e/v1", ID: "e", Price: 4}) {
		t.Errorf("round trip %+v", s)
	}
	if got := Others(file(t, map[string]any{"name": "x", "id": "big", "url": "u", "kind": "chat"})); len(got) != 1 || got[0].Name != "x" {
		t.Errorf("others %v", got)
	}
}

func TestCheck(t *testing.T) {
	srv, _ := fakeProvider(t, []string{"chat-1", "embed-1"}, "")
	keyed, _ := fakeProvider(t, []string{"m"}, "sk")
	ctx := context.Background()
	other := func(addr, id string) Choice { return Choice{Provider: Other, URL: addr, ID: id} }
	for _, c := range []struct {
		s    Setup
		want string
	}{
		{Setup{}, ""},
		{Setup{Chat: other(srv.URL, "chat-1"), Embed: Choice{Provider: Same, ID: "embed-1"}}, ""},
		{Setup{Chat: other(srv.URL, "nope")}, "serves no model nope"},
		{Setup{Chat: other(srv.URL, "")}, "Choose a chat model."},
		{Setup{Chat: other("localhost:9", "x")}, "starting with http"},
		{Setup{Chat: Choice{Provider: "bogus"}}, "Choose a provider for chat."},
		{Setup{Chat: Choice{Provider: "openai", ID: "gpt-5"}}, "OpenAI needs a key."},
		{Setup{Chat: other(keyed.URL, "m")}, "Incorrect API key provided."},
		{Setup{Chat: Choice{Provider: Other, URL: keyed.URL, Key: "sk", ID: "m"}}, ""},
		{Setup{Embed: Choice{Provider: Same, ID: "e"}}, "Choose a provider for search."},
		{Setup{Embed: Choice{Provider: "anthropic", Key: "k", ID: "e"}}, "Anthropic serves no search models"},
		{Setup{Chat: Choice{Provider: "anthropic", Key: "k", ID: "c"}, Embed: Choice{Provider: Same, ID: "e"}}, "Anthropic serves no search models"},
	} {
		err := Check(ctx, c.s)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v, want %q", c.s, err, c.want)
		}
	}
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	if err := Check(ctx, Setup{Chat: other(notFound.URL, "typed")}); err != nil {
		t.Errorf("a provider with no list takes the id typed: %v", err)
	}
}

func TestView(t *testing.T) {
	srv, _ := fakeProvider(t, []string{"a", "b"}, "")
	saved := Providers
	t.Cleanup(func() { Providers = saved })
	Providers = append([]Provider{{ID: "here", Name: "Here", Protocol: "openai", URL: srv.URL, Local: true},
		{ID: "gone", Name: "Gone", Protocol: "openai", URL: "http://127.0.0.1:1", Local: true}}, saved[2:]...)
	typed := Setup{Chat: Choice{Provider: "here", ID: "a", Key: "k"}, Embed: Choice{Provider: Other, URL: "http://127.0.0.1:1", ID: "e"}}
	if f := View(context.Background(), typed, Setup{}); f.Chat.Held {
		t.Error("a key typed and not held is not said to be held")
	}
	f := View(context.Background(), typed, typed)
	note := func(b Block, id string) string {
		for _, o := range b.Options {
			if o.ID == id {
				return o.Note
			}
		}
		return "absent"
	}
	if note(f.Chat, "here") != "2 models" || note(f.Chat, "gone") != "not running" || note(f.Chat, "openai") != "" {
		t.Errorf("notes %+v", f.Chat.Options)
	}
	if !slices.Equal(f.Chat.Suggestions, []string{"a", "b"}) || !f.Chat.Held || f.Chat.Summary != "a · Here" {
		t.Errorf("chat block %+v", f.Chat)
	}
	if note(f.Embed, "anthropic") != "absent" || note(f.Embed, Same) != "" || note(f.Chat, Same) != "absent" {
		t.Error("search offers no anthropic, and Same only there")
	}
	if f.Embed.Problem == "" {
		t.Error("a provider that could not be asked says why")
	}
	if !f.Chat.Shown(f.Chat.ForModel) || f.Chat.Shown(f.Chat.ForURL) || f.Chat.Shown(f.Chat.ForKey) || !f.Embed.Shown(f.Embed.ForKey) || !f.Embed.Shown(f.Embed.ForURL) {
		t.Errorf("parts shown: %+v %+v", f.Chat, f.Embed)
	}
	// Search on the chat block's provider is not offered beside an anthropic
	// chat, and a provider that needs a key is not asked without one.
	f = View(context.Background(), Setup{Chat: Choice{Provider: "anthropic", ID: "c"}}, Setup{})
	if note(f.Embed, Same) != "absent" || f.Chat.Problem != "" || f.Chat.Suggestions != nil {
		t.Errorf("anthropic without a key: %+v", f.Chat)
	}
	// A provider search cannot use is not asked for search.
	f = View(context.Background(), Setup{Embed: Choice{Provider: "anthropic", Key: "k", ID: "e"}}, Setup{})
	if f.Embed.Problem != "" || f.Embed.Suggestions != nil {
		t.Errorf("anthropic for search is not asked: %+v", f.Embed)
	}
}

func TestInitialAndSupported(t *testing.T) {
	srv, _ := fakeProvider(t, []string{"gemma4:26b", "nomic-embed-text"}, "")
	saved := Providers
	t.Cleanup(func() { Providers = saved })
	Providers = append([]Provider{{ID: "here", Name: "Here", Protocol: "openai", URL: srv.URL + "/v1", Local: true}}, saved[2:]...)
	schema := []byte(`{"properties":{"native":{"properties":{"llm":{"properties":{"models":{
	  "default":[{"url":"` + srv.URL + `/v1","id":"gemma4:26b","kind":"chat"},{"url":"` + srv.URL + `/v1","id":"nomic-embed-text","kind":"embed"}],
	  "items":{"properties":{"name":{},"enabled":{},"protocol":{},"url":{},"key":{},"id":{},"kind":{},"price":{}}}}}}}}}}`)
	s := Initial(context.Background(), schema)
	if s.Chat != (Choice{Provider: "here", ID: "gemma4:26b"}) || s.Embed != (Choice{Provider: Same, ID: "nomic-embed-text"}) {
		t.Errorf("initial %+v", s)
	}
	if s := Initial(context.Background(), nil); s.Chat.Provider != "here" || s.Chat.ID != "" || s.Embed.Provider != None {
		t.Errorf("no schema guesses no model: %+v", s)
	}
	Providers = saved[2:]
	if s := Initial(context.Background(), schema); s != (Setup{}) {
		t.Errorf("nothing running is no model: %+v", s)
	}
	if !Supported(schema) || Supported([]byte(`{"properties":{"native":{"properties":{"llm":{"properties":{"models":{"items":{"properties":{"name":{}}}}}}}}}}`)) || Supported(nil) {
		t.Error("supported")
	}
}

// Three reviews' cases: Same posted for the chat block resolves to nothing and
// is refused, never recursed into; two blocks at one address keep their own
// keys; an address the list does not hold, of either protocol, is kept.
func TestMalformedAndUnlistedSetups(t *testing.T) {
	s := Read(url.Values{"chat_provider": {Same}, "chat_model": {"m"}, "embed_provider": {Same}, "embed_model": {"e"}})
	if err := Check(context.Background(), s); err == nil || err.Error() != "Choose a provider for chat." {
		t.Errorf("Same for chat is refused: %v", err)
	}
	if f := View(context.Background(), s, Setup{}); f.Chat.Summary == "" {
		t.Error("and still renders")
	}

	held := Setup{Chat: Choice{Provider: Other, URL: "http://h/v1", Key: "chat-key", ID: "c"},
		Embed: Choice{Provider: Other, URL: "http://h/v1", Key: "embed-key", ID: "e"}}
	typed := Setup{Chat: Choice{Provider: Other, URL: "http://h/v1", ID: "c"}, Embed: Choice{Provider: Other, URL: "http://h/v1", ID: "e"}}
	typed.Keep(held)
	if typed.Chat.Key != "chat-key" || typed.Embed.Key != "embed-key" {
		t.Errorf("each block keeps its own key: %+v", typed)
	}

	proxy := map[string]any{"native": map[string]any{"llm": map[string]any{"chat": "c", "models": []any{
		map[string]any{"name": "c", "protocol": "anthropic", "url": "https://gateway.example/anthropic/v1", "key": "k", "id": "claude", "kind": "chat"}}}}}
	got := From(proxy)
	if got.Chat.Provider != "other-anthropic" || got.Chat.URL != "https://gateway.example/anthropic/v1" {
		t.Fatalf("the proxy is kept: %+v", got.Chat)
	}
	Apply(proxy, got)
	if e := entries(llmOf(proxy))[0]; e["url"] != "https://gateway.example/anthropic/v1" || e["protocol"] != "anthropic" || e["key"] != "k" || llmOf(proxy)["chat"] != "c" {
		t.Errorf("saved again, it is unchanged: %v", e)
	}
	srv, gotHdr := fakeProvider(t, []string{"claude"}, "k")
	f := View(context.Background(), Setup{Chat: Choice{Provider: "other-anthropic", URL: srv.URL, Key: "k", ID: "claude"}}, Setup{})
	if (*gotHdr).Get("x-api-key") != "k" || !slices.Contains(f.Chat.Suggestions, "claude") || !f.Chat.Shown(f.Chat.ForURL) || !f.Chat.Shown(f.Chat.ForKey) {
		t.Errorf("asked at its own address: %+v", f.Chat)
	}
	for _, o := range f.Embed.Options {
		if o.ID == "other-anthropic" {
			t.Error("search offers no Anthropic-compatible address")
		}
	}
}
