//go:build e2e

// Package e2e drives the whole installation as a person does (requirements
// §9): the pinned kernel and agent, the UI, chat and the supervisor built from
// this tree, a fresh home, and a real browser, one subtest per user story, in
// order, on one rig. Outside a network namespace TestMain installs the pinned
// components into a cache and builds the programs, then runs itself again
// under `unshare -rn`, so no story reaches the network. Run it with `make e2e`.
package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/daios-ai/juice-ui/kernel"
)

const (
	nsEnv    = "JUICE_E2E_NS"  // set inside the namespace
	binEnv   = "JUICE_E2E_BIN" // the directory holding every program
	ui       = "http://127.0.0.1:8791"
	kernelAt = "http://127.0.0.1:4791"
	password = "secret123"
	answer   = "The fake model answers." // as read; the model writes it in Markdown (written)
	written  = "The fake model **answers**."
)

func TestMain(m *testing.M) {
	if os.Getenv(nsEnv) == "" {
		os.Exit(outside())
	}
	if out, err := exec.Command("ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: loopback: %v %s\n", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// outside installs the pinned kernel and agent into the cache, as the
// installer does, builds this tree's programs beside them, and runs the
// tests again inside a network namespace.
func outside() int {
	cache, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	home := filepath.Join(cache, "juice-ui-e2e")
	at, _ := url.Parse(kernelAt)
	inst := &kernel.Installation{Home: home, URL: at}
	for _, c := range inst.Due(false) {
		if err := inst.Install(context.Background(), c); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: install %s %s: %v\n", c.Name, c.Release, err)
			return 1
		}
	}
	bin := filepath.Join(home, "bin")
	for _, p := range []string{"juice-ui", "juice-chat", "juice-supervisor"} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, p), "../cmd/"+p).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: build %s: %v %s\n", p, err, out)
			return 1
		}
	}
	cmd := exec.Command("unshare", append([]string{"-rn", os.Args[0]}, os.Args[1:]...)...)
	cmd.Env = append(os.Environ(), nsEnv+"=1", binEnv+"="+bin)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	return 0
}

// TestStories walks the user stories in order; each needs the ones before.
func TestStories(t *testing.T) {
	rig(t)
	browser := newBrowser(t)
	sys, ann, bob := user(t, browser), user(t, browser), user(t, browser)
	var room string // ann's first room, its path

	step(t, "install", func(t *testing.T) {
		visit(t, sys, "/")
		wait(t, sys, `document.querySelector('input[name=world][value=play]')?.checked`)
		submit(t, sys, "form.install-step", map[string]string{"world": "play"})
		waitText(t, sys, "Kernel name")
		submit(t, sys, "form.install-step", map[string]string{"handle": "alpha", "password": password, "confirm_password": password})
		waitText(t, sys, "Assistant password")
		submit(t, sys, "form.install-step", map[string]string{"password": password, "confirm_password": password})
		waitText(t, sys, "Chat model")
		chooseModel(t, sys, "chat", "fake-chat")
		chooseModel(t, sys, "embed", "fake-embed")
		time.Sleep(time.Second) // a refresh the last choice started has answered
		wait(t, sys, modelChosen("chat", "fake-chat")+" && "+modelChosen("embed", "fake-embed"))
		submit(t, sys, "form.install-step", nil)
		waitText(t, sys, "recovery phrase")
		submit(t, sys, "form.install-step", map[string]string{"understand": "on"})
		waitText(t, sys, "Kernel home")
		waitText(t, sys, "assistant@alpha")
		submit(t, sys, "form.install-step", nil)
		waitTextFor(t, sys, "Sign in", 3*time.Minute)
	})

	step(t, "the operator lands on the wallet and records a payment there", func(t *testing.T) {
		signIn(t, sys, "sys")
		waitURL(t, sys, "/wallet")
		waitText(t, sys, "This account holds no money. Conversations and other actions are paid from this balance. Add some to start.")
		wait(t, sys, nothingOfChat)
		click(t, sys, `a[data-modal="form-admin-deposit"]`)
		wait(t, sys, `document.querySelector("#dep-address").value === "sys@alpha"`)
		fill(t, sys, "#dep-ref", "e2e-sys@alpha")
		fill(t, sys, "#dep-amount", "100")
		click(t, sys, `#form-admin-deposit button.btn-money`)
		click(t, sys, "#modal-confirm")
		waitText(t, sys, "Recorded")
		waitURL(t, sys, "/wallet")
	})

	step(t, "a person without money lands on the wallet, and in their room once funded", func(t *testing.T) {
		signUp(t, ann, "ann")
		signIn(t, ann, "ann")
		waitURL(t, ann, "/wallet")
		waitText(t, ann, "This account holds no money. Conversations and other actions are paid from this balance. Add some to start.")
		wait(t, ann, nothingOfChat)
		waitText(t, ann, "the operator records money in and out. To add money, give them your account.")
		waitText(t, ann, "ann@alpha")
		deposit(t, sys, "ann@alpha", "100")
		waitURL(t, ann, "/chat/room/")
		room = path(t, ann)
		wait(t, ann, `document.body.textContent.includes("assistant@alpha")`)
	})

	step(t, "the operator funds the assistant in two clicks", func(t *testing.T) {
		visit(t, sys, "/agents")
		wait(t, sys, `document.querySelector('#agent-state-assistant .pill')?.textContent === "no funds"`)
		click(t, sys, `a[data-modal="form-fund-assistant"]`)
		fill(t, sys, "#fund-amount-assistant", "10")
		click(t, sys, `#form-fund-assistant button.btn-primary`)
		click(t, sys, `#form-fund-review-assistant button.btn-money`)
		waitURL(t, sys, "/agents")
		wait(t, sys, `document.querySelector('#agent-state-assistant .pill')?.textContent === "running" && !document.querySelector("#form-fund-review-assistant")`)
	})

	step(t, "a post is answered by the assistant", func(t *testing.T) {
		visit(t, ann, room)
		post(t, ann, "hello")
		waitText(t, ann, "hello")
		waitText(t, ann, answer)
		wait(t, ann, `[...document.querySelectorAll('.prose strong')].some((b) => b.textContent === "answers")`)
	})

	step(t, "a message is formatted, and its image loads only when clicked", func(t *testing.T) {
		picture := pictureHost(t)
		visit(t, ann, room)
		post(t, ann, "**look** at ![a cat]("+picture+")")
		wait(t, ann, `[...document.querySelectorAll('.prose strong')].some((b) => b.textContent === "look")`)
		wait(t, ann, `document.querySelector('a[data-image]')?.textContent.includes("127.0.0.1")`)
		wait(t, ann, `(() => { const m = [...document.querySelectorAll('.msg')].find((m) => m.querySelector('.prose strong')?.textContent === "look");
			return m && !m.querySelector('[data-json-switch]') && !document.querySelector('.prose img'); })()`)
		click(t, ann, `a[data-image]`)
		wait(t, ann, `(() => { const i = document.querySelector('.prose img'); return i && i.complete && i.naturalWidth > 0 && i.alt === "a cat"; })()`)
	})

	step(t, "a tool chosen from the launcher runs in the panel and its result is the draft", func(t *testing.T) {
		visit(t, ann, room)
		launch(t, ann, "resolve")
		click(t, ann, `.launcher a.menu-item[href^="/actions?"]`)
		runInPanel(t, ann, room)
	})

	step(t, "a tool chosen from search results opens in the panel", func(t *testing.T) {
		visit(t, ann, room)
		launch(t, ann, "resolve")
		key(t, ann, "\r")
		wait(t, ann, `!!document.querySelector('#panel .action-list a[href]')`)
		click(t, ann, `#panel .action-list a[href]`)
		wait(t, ann, `!!document.querySelector('#panel [data-panel-back]') && !!document.querySelector('#panel form')`)
		if p := path(t, ann); p != room {
			t.Fatalf("the result left the room for %s", p)
		}
		click(t, ann, `#panel [data-panel-back]`)
		wait(t, ann, `!!document.querySelector('#panel .action-list a[href]')`)
		click(t, ann, `#panel .action-list a[href]`)
		runInPanel(t, ann, room)
	})

	step(t, "a room made by address is joined by the invited person", func(t *testing.T) {
		signUp(t, bob, "bob")
		deposit(t, sys, "bob@alpha", "100")
		signIn(t, bob, "bob")
		visit(t, ann, room)
		click(t, ann, `a[data-modal="form-new"]`)
		fill(t, ann, "#room-members", "bob@alpha")
		fill(t, ann, "#room-name", "with bob")
		click(t, ann, `#form-new button[type=submit]`)
		waitText(t, ann, "with bob")
		waitURL(t, ann, "/chat/room/")
		post(t, ann, "hi bob")
		waitText(t, ann, "hi bob")
		visit(t, bob, "/chat")
		waitText(t, bob, "Invitation from")
		clickText(t, bob, "button", "Accept")
		clickText(t, bob, `a[starts-with(@href, "/chat/room/")]`, "with bob")
		waitText(t, bob, "hi bob")
	})

	step(t, "a post's charge is in the transactions", func(t *testing.T) {
		visit(t, ann, "/transactions")
		waitText(t, ann, "msg/post")
	})

	step(t, "the operator makes an agent, and starting it asks for its password", func(t *testing.T) {
		visit(t, sys, "/agents?form=agent-create")
		submit(t, sys, `form[action="/agents"]`, map[string]string{"handle": "helper", "password": password, "confirm_password": password})
		waitURL(t, sys, "/agents") // New agent lands on its Fund, over the list
		wait(t, sys, `document.querySelector('#form-fund-helper').open`)
		visit(t, sys, "/agents") // the fleet's control pane: an agent's commands are there
		wait(t, sys, `!!document.querySelector('#agent-commands-helper form[action$="/stop"]')`)
		other, cancel := chromedp.NewContext(sys) // a second tab on the list, kept by the heartbeat
		defer cancel()
		if err := chromedp.Run(other); err != nil {
			t.Fatal(err)
		}
		visit(t, other, "/agents")
		wait(t, other, `!!document.querySelector('#agent-commands-helper form[action$="/stop"]')`)
		run(t, sys, "front", page.BringToFront())
		submit(t, sys, `#agent-commands-helper form[action$="/stop"]`, nil)
		waitURL(t, sys, "/agents")
		wait(t, sys, `document.querySelector('#agent-state-helper .pill')?.textContent === "stopped"`)
		run(t, other, "front", page.BringToFront())
		waitFor(t, other, `document.querySelector('#agent-state-helper .pill')?.textContent === "stopped" && !!document.querySelector('#agent-commands-helper a[aria-label="Start helper"]')`, 30*time.Second)
		run(t, sys, "front", page.BringToFront())
		submit(t, sys, `#form-start-helper form`, map[string]string{"password": "wrong-password"})
		waitText(t, sys, "Password is incorrect.")
		wait(t, sys, `document.querySelector('#form-start-helper').open && !!document.querySelector('#agent-state-helper')`) // refused over the list
		submit(t, sys, `#form-start-helper form`, map[string]string{"password": password})
		waitURL(t, sys, "/agents")
		wait(t, sys, `!!document.querySelector('#agent-commands-helper form[action$="/stop"]')`)
		visit(t, sys, "/agents/helper") // its page: its facts and Settings, none of its commands
		wait(t, sys, `document.querySelector('h1 .ref')?.textContent === "helper" && document.body.innerText.includes("Settings") && !document.querySelector('form[action$="/stop"]')`)
	})

	step(t, "a person asks an agent, and the operator decides as it", func(t *testing.T) {
		visit(t, ann, "/actions?ref=sys@alpha/message")
		submit(t, ann, `form[action$="/run"]`, map[string]string{"f_/to": "helper@alpha", "f_/message": "may I?"})
		waitText(t, ann, "task_id")
		visit(t, sys, "/agents")
		wait(t, sys, `document.querySelector('#agent-commands-helper a[data-fill="helper"] .badge')?.textContent === "1"`)
		click(t, sys, `#agent-commands-helper a[data-fill="helper"]`) // the layout's Sign in dialog, its handle filled in
		wait(t, sys, `document.querySelector('#signin').open && document.querySelector('#signin [name=handle]').value === "helper" && document.activeElement === document.querySelector('#signin [name=password]')`)
		fill(t, sys, `#signin [name=password]`, "wrong-password")
		click(t, sys, `#signin button[type=submit]`) // by htmx: the refusal answers the dialog open, over the list
		wait(t, sys, `document.querySelector('#signin').open && document.querySelector('#signin').innerText.includes("Invalid handle or password.") && document.querySelector('#signin [name=handle]').value === "helper"`)
		wait(t, sys, `location.pathname === "/agents" && !!document.querySelector('#agent-state-helper')`)
		submit(t, sys, `#signin form`, map[string]string{"password": password})
		waitURL(t, sys, "/wallet") // the agent holds no money
		wait(t, sys, `document.querySelector('meta[name=account]').content === "helper@alpha" && !document.querySelector('.rail') && !document.querySelector('a[href="/agents"]')`)
		visit(t, sys, "/inbox")
		waitText(t, sys, "ann@alpha")
		submit(t, sys, `form[action^="/inbox/requesters/"][action$="/approve"]`, nil)
		waitText(t, sys, "Approved.")
		submit(t, sys, `form[action="/switch"]:has(input[value="sys@alpha"])`, nil)
		wait(t, sys, `document.querySelector('meta[name=account]')?.content === "sys@alpha"`)
		visit(t, sys, "/agents")
		wait(t, sys, `!!document.querySelector('#agent-commands-helper') && !document.querySelector('#agent-commands-helper [aria-label="Waiting"]')`)
		click(t, sys, `[data-menu="profile-popup"]`)                              // the avatar menu's Sign in opens the same dialog, empty
		wait(t, sys, `document.querySelector('#profile-popup').style.top !== ""`) // placed on the next frame
		click(t, sys, `#profile-popup a[data-modal="signin"]`)
		wait(t, sys, `document.querySelector('#signin').open && document.querySelector('#signin [name=handle]').value === ""`)
		for _, pg := range []string{"/admin", "/admin/peers"} { // users and kernels: record lists, as the agents
			visit(t, sys, pg)
			wait(t, sys, `!!document.querySelector('table.records tbody tr td[data-label]')`)
		}
		waitText(t, sys, "fee_bps") // the network page states this kernel's money with its config keys
		visit(t, sys, "/login")     // the entry screen is for a signed-out visitor alone
		wait(t, sys, `location.pathname !== "/login" && !document.querySelector('.auth')`)
	})

	step(t, "a page open as one account does nothing as another, and reloads itself", func(t *testing.T) {
		other, cancel := chromedp.NewContext(sys) // a second tab of the same person
		defer cancel()
		if err := chromedp.Run(other); err != nil {
			t.Fatal(err)
		}
		visit(t, other, "/wallet")
		wait(t, other, `document.querySelector('meta[name=account]').content === "sys@alpha"`)
		run(t, sys, "front", page.BringToFront())
		visit(t, sys, "/wallet")
		submit(t, sys, `form[action="/switch"]:has(input[value="helper@alpha"])`, nil)
		wait(t, sys, `document.querySelector('meta[name=account]')?.content === "helper@alpha"`)
		run(t, other, "front", page.BringToFront())
		waitFor(t, other, `document.querySelector('meta[name=account]')?.content === "helper@alpha" && location.pathname === "/wallet"`, 30*time.Second)
		run(t, sys, "front", page.BringToFront())
		visit(t, sys, "/wallet")
		submit(t, sys, `form[action="/switch"]:has(input[value="sys@alpha"])`, nil)
		wait(t, sys, `document.querySelector('meta[name=account]')?.content === "sys@alpha"`)
	})
}

// ---- the rig ----

// rig starts a fake model and the UI on a fresh home holding the programs, and
// ends every process of that home when the test ends.
func rig(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"juice", "juice-agent", "juice-ui", "juice-chat", "juice-supervisor"} {
		b, err := os.ReadFile(filepath.Join(os.Getenv(binEnv), p))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, p), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakeModel(t)
	log, err := os.Create(filepath.Join(home, "ui.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(bin, "juice-ui"))
	cmd.Env = append(os.Environ(), "JUICE_HOME="+home, "JUICE_KERNEL_URL="+kernelAt, "JUICE_UI_ADDR=127.0.0.1:8791",
		"JUICE_UI_PUBLIC_URL="+ui, "JUICE_UI_SESSION_SECRET="+strings.Repeat("e2e-", 12))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			_ = cmd.Process.Kill()
		}
		killHome(home)
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(home, "ui.log"))
			var warn []string
			for _, l := range strings.Split(string(b), "\n") {
				if !strings.Contains(l, "level=INFO") {
					warn = append(warn, l)
				}
			}
			t.Logf("ui.log, all but INFO:\n%s", tail(strings.Join(warn, "\n"), 60))
		}
	})
	for i := 0; i < 100; i++ {
		if r, err := http.Get(ui + "/"); err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the UI never answered")
}

// killHome ends any process still running with this home, found by its
// environment: the agents and chat the supervisor started.
func killHome(home string) {
	ps, _ := filepath.Glob("/proc/[0-9]*/environ")
	for _, p := range ps {
		b, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(b), "JUICE_HOME="+home+"\x00") {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(filepath.Base(filepath.Dir(p)), "%d", &pid); err == nil && pid != os.Getpid() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// fakeModel answers as an OpenAI-compatible provider on Ollama's address: two
// models, a fixed reply, and an embedding.
func fakeModel(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:11434")
	if err != nil {
		t.Fatal(err)
	}
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "fake-chat", "object": "model"}, map[string]any{"id": "fake-embed", "object": "model"}}})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		reply(w, map[string]any{"id": "c1", "object": "chat.completion", "model": "fake-chat", "choices": []any{
			map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": written}}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	})
	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input any `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := 1
		if xs, ok := in.Input.([]any); ok {
			n = len(xs)
		}
		data := make([]any, n)
		for i := range data {
			v := make([]float64, 8)
			v[i%8] = 1
			data[i] = map[string]any{"object": "embedding", "index": i, "embedding": v}
		}
		reply(w, map[string]any{"object": "list", "data": data, "model": "fake-embed"})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// pictureHost serves one image over https on loopback, as an image host on
// the web would, and returns its address.
func pictureHost(t *testing.T) string {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/cat.png"
}

// ---- the browser ----

func newBrowser(t *testing.T) context.Context {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath("/usr/bin/google-chrome"), chromedp.NoSandbox, chromedp.UserDataDir(t.TempDir()),
		chromedp.Flag("ignore-certificate-errors", true), // pictureHost's certificate is the test's own
		chromedp.WindowSize(1280, 1000))
	alloc, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(cancel)
	ctx, cancelCtx := chromedp.NewContext(alloc, chromedp.WithErrorf(func(string, ...any) {}))
	t.Cleanup(cancelCtx)
	if err := chromedp.Run(ctx); err != nil {
		t.Fatalf("chrome: %v", err)
	}
	return ctx
}

// user is a browser context of its own: users never share cookies.
func user(t *testing.T, browser context.Context) context.Context {
	ctx, cancel := chromedp.NewContext(browser, chromedp.WithNewBrowserContext())
	t.Cleanup(cancel)
	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx
}

// step runs a story, stopping the walk at the first that fails.
func step(t *testing.T, name string, f func(t *testing.T)) {
	if t.Failed() {
		t.Skip("an earlier story failed")
	}
	if !t.Run(name, f) {
		t.FailNow()
	}
}

func run(t *testing.T, ctx context.Context, what string, a ...chromedp.Action) {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := chromedp.Run(c, a...); err != nil {
		var body string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`location.pathname + "\n" + document.body.innerText`, &body))
		t.Fatalf("%s: %v\npage: %s", what, err, tail(body, 40))
	}
}

func visit(t *testing.T, ctx context.Context, p string) {
	t.Helper()
	run(t, ctx, "visit "+p, chromedp.Navigate(ui+p), chromedp.WaitReady("body"))
}

func click(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	run(t, ctx, "click "+sel, chromedp.WaitVisible(sel), chromedp.Click(sel, chromedp.NodeVisible))
}

// clickText clicks the element matched by an XPath step whose text is s.
func clickText(t *testing.T, ctx context.Context, step, s string) {
	t.Helper()
	x := fmt.Sprintf(`//%s[normalize-space()=%q]`, step, s)
	run(t, ctx, "click "+x, chromedp.WaitVisible(x, chromedp.BySearch), chromedp.Click(x, chromedp.BySearch, chromedp.NodeVisible))
}

func fill(t *testing.T, ctx context.Context, sel, v string) {
	t.Helper()
	run(t, ctx, "fill "+sel, chromedp.WaitVisible(sel), chromedp.SetValue(sel, v))
}

func key(t *testing.T, ctx context.Context, k string) {
	t.Helper()
	run(t, ctx, "key", chromedp.KeyEvent(k))
}

// wait waits for a script expression to be true, through navigations.
func wait(t *testing.T, ctx context.Context, expr string) {
	t.Helper()
	waitFor(t, ctx, expr, 80*time.Second)
}

func waitText(t *testing.T, ctx context.Context, s string) {
	t.Helper()
	waitTextFor(t, ctx, s, 80*time.Second)
}

func waitTextFor(t *testing.T, ctx context.Context, s string, d time.Duration) {
	t.Helper()
	q, _ := json.Marshal(s)
	waitFor(t, ctx, fmt.Sprintf(`document.body.innerText.includes(%s)`, q), d)
}

func waitFor(t *testing.T, ctx context.Context, expr string, d time.Duration) {
	t.Helper()
	js := fmt.Sprintf(`(() => { try { return !!(%s); } catch (_) { return false; } })()`, expr)
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		var ok bool
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := chromedp.Run(c, chromedp.Evaluate(js, &ok))
		cancel()
		if err == nil && ok {
			return
		}
	}
	var body string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`location.pathname + "\n" + document.body.innerText`, &body))
	t.Fatalf("waiting for %s\npage: %s", expr, tail(body, 40))
}

// nothingOfChat holds on a page drawn for an account holding no money: no
// banner and nothing of chat, its rail and New room included.
const nothingOfChat = `!document.querySelector(".banner:not([hidden]), .rail") && !document.body.innerText.includes("New room")`

func waitURL(t *testing.T, ctx context.Context, prefix string) {
	t.Helper()
	q, _ := json.Marshal(prefix)
	wait(t, ctx, fmt.Sprintf(`location.pathname.startsWith(%s)`, q))
}

func path(t *testing.T, ctx context.Context) string {
	t.Helper()
	var p string
	run(t, ctx, "path", chromedp.Evaluate(`location.pathname + location.search`, &p))
	return p
}

// submit fills a form by its fields' names and submits it as a plain post,
// for the setup pages a story walks through rather than tests.
func submit(t *testing.T, ctx context.Context, form string, fields map[string]string) {
	t.Helper()
	f, _ := json.Marshal(form)
	v, _ := json.Marshal(fields)
	run(t, ctx, "submit "+form, chromedp.WaitReady(form), chromedp.Evaluate(fmt.Sprintf(`(() => {
		const f = document.querySelector(%s), v = %s || {};
		for (const [k, x] of Object.entries(v)) for (const el of f.querySelectorAll('[name="' + k + '"]')) {
			if (el.type === "radio") el.checked = el.value === x;
			else if (el.type === "checkbox") el.checked = true;
			else el.value = x;
		}
		HTMLFormElement.prototype.submit.call(f);
	})()`, f, v), nil))
}

// chooseModel picks Ollama, the fake model's address, for a block, then the
// model its list offers.
func chooseModel(t *testing.T, ctx context.Context, block, model string) {
	t.Helper()
	sel := "#m-" + block + "-provider"
	run(t, ctx, "provider "+block, chromedp.WaitReady(sel), chromedp.SetValue(sel, "ollama"),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).dispatchEvent(new Event("change", {bubbles: true}))`, sel), nil))
	// The model field is replaced each time its list arrives, so the model is
	// chosen once no refresh is in flight, on the field then current.
	wait(t, ctx, modelChosen(block, model))
}

// modelChosen chooses a block's model, once no refresh is in flight and its
// field offers it, and is true when it is chosen.
func modelChosen(block, model string) string {
	return fmt.Sprintf(`(() => { const s = document.querySelector("#m-%s-model");
		if (document.querySelector(".htmx-request") || !s || !s.querySelector('option[value=%q]')) return false;
		s.value = %q; return s.value === %q; })()`, block, model, model, model)
}

func signIn(t *testing.T, ctx context.Context, handle string) {
	t.Helper()
	visit(t, ctx, "/login")
	submit(t, ctx, `form[action="/login"]`, map[string]string{"handle": handle, "password": password})
	wait(t, ctx, `location.pathname !== "/login"`)
}

func signUp(t *testing.T, ctx context.Context, handle string) {
	t.Helper()
	visit(t, ctx, "/login")
	submit(t, ctx, `form[action="/signup"]`, map[string]string{"handle": handle, "password": password, "confirm_password": password})
	waitText(t, ctx, "I understand")
	submit(t, ctx, `form:has(input[name=understand])`, map[string]string{"understand": "on"})
	wait(t, ctx, `!document.querySelector('input[name=understand]')`)
}

// deposit records a payment to an address from Admin, as the operator.
func deposit(t *testing.T, ctx context.Context, address, amount string) {
	t.Helper()
	visit(t, ctx, "/admin?form=admin-deposit")
	submit(t, ctx, `form[action="/admin/deposit"]`, map[string]string{"address": address, "amount": amount, "ref": "e2e-" + address})
	waitText(t, ctx, "Recorded")
}

// post types into the composer and sends with Enter, waiting until it is
// sent (the box empties); what it reads as depends on its Markdown.
func post(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	box := `form[data-composer] [data-fields] textarea`
	run(t, ctx, "post", chromedp.WaitVisible(box), chromedp.Click(box), chromedp.SendKeys(box, text), chromedp.KeyEvent("\r"))
	wait(t, ctx, `document.querySelector('form[data-composer] [data-fields] textarea').value === ""`)
}

// launch opens the composer's launcher and types a query after its slash.
func launch(t *testing.T, ctx context.Context, q string) {
	t.Helper()
	box := `form[data-composer] [data-fields] textarea`
	run(t, ctx, "launch", chromedp.WaitVisible(box), chromedp.Click(`form[data-composer] [data-launcher]`), chromedp.SendKeys(box, q))
	wait(t, ctx, `!document.querySelector('form[data-composer] .launcher').hidden`)
}

// runInPanel fills the panel's first empty text field with an address, runs
// it, and expects the result as the composer's draft, the panel gone and the
// page still the room.
func runInPanel(t *testing.T, ctx context.Context, room string) {
	t.Helper()
	wait(t, ctx, `!!document.querySelector('#panel form button[type=submit]')`)
	if p := path(t, ctx); p != room {
		t.Fatalf("the tool left the room for %s", p)
	}
	run(t, ctx, "fill the panel", chromedp.Evaluate(`(() => {
		const f = document.querySelector('#panel form');
		for (const el of f.querySelectorAll('textarea:not([disabled]), input[type=text]:not([disabled])')) if (!el.value) el.value = "ann@alpha";
	})()`, nil))
	click(t, ctx, `#panel form button[type=submit]:not([formaction])`)
	wait(t, ctx, `document.querySelector('form[data-composer] [data-fields] textarea').value.trim() !== "" && document.getElementById('panel').childElementCount === 0`)
	if p := path(t, ctx); p != room {
		t.Fatalf("the run left the room for %s", p)
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
