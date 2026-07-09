// Package daemon is the @chat application: a self-contained Juice user that
// registers its own free (price-0) actions and projects its own transaction
// ledger into rooms and messages. It attributes every event by the unforgeable
// caller_handle (no OAuth, no per-user consent) because it *is* a Juice member.
//
// It runs in-process inside juice-ui (started by the chat plugin's Start), but
// depends only on the kernel client — it knows nothing about juice-ui or the app
// seam, so it is independently testable against a live or fake kernel.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// pollInterval is how often the projection folds in new ledger events.
const pollInterval = 2 * time.Second

// ---- kernel surface ----

// actionSpec is one @chat action to register: its name and input schema. Every
// @chat action is a free (price-0) kind=http event emitter pointing at the app's
// ack endpoint; the real logic is the ledger projection.
type actionSpec struct {
	name        string
	inputSchema string
}

const outputSchema = `{"type":"object","properties":{"ok":{"type":"boolean","description":"acknowledgement"}}}`

// actionSpecs is the chat application's kernel surface.
var actionSpecs = []actionSpec{
	{"create-room", `{"type":"object","properties":{"name":{"type":"string","description":"room name"},"members":{"type":"array","items":{"type":"string"},"description":"member handles"}}}`},
	{"rename-room", `{"type":"object","properties":{"room":{"type":"string","description":"room id"},"name":{"type":"string","description":"new name"}}}`},
	{"delete-room", `{"type":"object","properties":{"room":{"type":"string","description":"room id"}}}`},
	{"add-member", `{"type":"object","properties":{"room":{"type":"string","description":"room id"},"member":{"type":"string","description":"handle to add"}}}`},
	{"remove-member", `{"type":"object","properties":{"room":{"type":"string","description":"room id"},"member":{"type":"string","description":"handle to remove"}}}`},
	{"leave-room", `{"type":"object","properties":{"room":{"type":"string","description":"room id"}}}`},
	{"message", `{"type":"object","properties":{"room":{"type":"string","description":"room id"},"text":{"type":"string","description":"message body"}}}`},
	{"delete-message", `{"type":"object","properties":{"room":{"type":"string","description":"room id"},"message":{"type":"string","description":"message id"}}}`},
}

// actionNameSet is the set of @chat action names, used by the projection to
// filter the ledger.
func actionNameSet() map[string]bool {
	m := make(map[string]bool, len(actionSpecs))
	for _, s := range actionSpecs {
		m[s.name] = true
	}
	return m
}

// ---- @chat kernel identity ----

// service is the @chat kernel identity: it authenticates as @chat, ensures the
// chat actions exist, and reads the @chat ledger for the projection. It manages
// @chat's own token (login + refresh) independent of any user session.
type service struct {
	client   *client.Client
	handle   string // "@chat"
	password string
	ackURL   string // absolute URL of the kernel-facing ack endpoint

	mu   sync.Mutex
	toks client.Tokens
}

func newService(c *client.Client, handle, password, ackURL string) *service {
	return &service{client: c, handle: handle, password: password, ackURL: ackURL}
}

// login authenticates as @chat (creating the account first if absent) and stores
// the token pair. Uses the same authorization-code+PKCE flow as a browser login.
func (s *service) login(ctx context.Context) error {
	// Best-effort create; ignore "already exists". A pre-existing @chat with a
	// different password will fail the Authorize below (a clear misconfiguration).
	_, _ = s.client.CreateUser(ctx, s.handle, "chat@juice-ui.local", s.password)
	verifier, err := session.NewVerifier()
	if err != nil {
		return err
	}
	code, err := s.client.Authorize(ctx, s.handle, s.password, session.Challenge(verifier))
	if err != nil {
		return fmt.Errorf("chat: authorize %s: %w", s.handle, err)
	}
	toks, err := s.client.ExchangeCode(ctx, code, verifier)
	if err != nil {
		return fmt.Errorf("chat: token exchange: %w", err)
	}
	s.mu.Lock()
	s.toks = toks
	s.mu.Unlock()
	return nil
}

// do runs fn with @chat's access token, refreshing (or re-logging-in) once on an
// authentication error and retrying.
func (s *service) do(ctx context.Context, fn func(token string) error) error {
	s.mu.Lock()
	tok := s.toks.AccessToken
	s.mu.Unlock()
	if tok == "" {
		if err := s.login(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		tok = s.toks.AccessToken
		s.mu.Unlock()
	}
	err := fn(tok)
	if !errors.Is(err, client.ErrUnauthenticated) {
		return err
	}
	if rerr := s.refresh(ctx); rerr != nil {
		return rerr
	}
	s.mu.Lock()
	tok = s.toks.AccessToken
	s.mu.Unlock()
	return fn(tok)
}

// refresh rotates @chat's token pair, falling back to a full re-login if the
// refresh token is no longer valid.
func (s *service) refresh(ctx context.Context) error {
	s.mu.Lock()
	rt := s.toks.RefreshToken
	s.mu.Unlock()
	nt, err := s.client.Refresh(ctx, rt)
	if err != nil {
		return s.login(ctx)
	}
	s.mu.Lock()
	s.toks = nt
	s.mu.Unlock()
	return nil
}

// ensureActions registers, enables, and publishes each chat action that does not
// already exist under @chat. Idempotent — safe to run on every startup.
func (s *service) ensureActions(ctx context.Context) error {
	return s.do(ctx, func(tok string) error {
		existing, err := s.client.ListActions(ctx, tok, s.handle, "", true)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, a := range existing {
			have[a.Name] = true
		}
		for _, spec := range actionSpecs {
			if have[spec.name] {
				continue
			}
			a, err := s.client.CreateAction(ctx, tok, client.CreateActionInput{
				Name:         spec.name,
				Kind:         "http",
				Source:       s.ackURL,
				Method:       "POST",
				Description:  "chat " + spec.name + " event",
				Price:        0,
				InputSchema:  json.RawMessage(spec.inputSchema),
				OutputSchema: json.RawMessage(outputSchema),
			})
			if err != nil {
				return fmt.Errorf("chat: create @chat/%s: %w", spec.name, err)
			}
			if err := s.client.EnableAction(ctx, tok, a.ID); err != nil {
				return fmt.Errorf("chat: enable @chat/%s: %w", spec.name, err)
			}
			pub := true
			if _, err := s.client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Public: &pub}); err != nil {
				return fmt.Errorf("chat: publish @chat/%s: %w", spec.name, err)
			}
		}
		return nil
	})
}

// ---- ledger projection ----

// projector maintains the store as a projection of the @chat transaction ledger.
// It replays the ledger on the first poll and applies new events on each
// subsequent poll; Store.Applied makes replays idempotent.
type projector struct {
	svc   *service
	store *Store
	names map[string]bool
}

func newProjector(svc *service, store *Store) *projector {
	return &projector{svc: svc, store: store, names: actionNameSet()}
}

// poll reads @chat's ledger and applies any chat events not yet projected, in
// ledger (chronological) order.
func (p *projector) poll(ctx context.Context) error {
	var txs []client.Transaction
	if err := p.svc.do(ctx, func(tok string) error {
		var e error
		txs, e = p.svc.client.ListTransactions(ctx, tok, "")
		return e
	}); err != nil {
		return err
	}
	var pending []client.Transaction
	for _, t := range txs {
		if p.names[t.ActionName] && !p.store.Applied(t.ID) {
			pending = append(pending, t)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].StartedAt != pending[j].StartedAt {
			return pending[i].StartedAt < pending[j].StartedAt
		}
		return pending[i].ID < pending[j].ID
	})
	for _, t := range pending {
		if ev, ok := p.resolve(ctx, t); ok {
			p.store.Apply(ev)
		}
	}
	return nil
}

// resolve builds an event from a ledger transaction. The tx list omits the
// caller, so the unforgeable caller_handle is fetched with tx show (once per
// event); args come from the list.
func (p *projector) resolve(ctx context.Context, t client.Transaction) (event, bool) {
	caller, args := t.CallerHandle, t.Args
	if caller == "" {
		var full client.Transaction
		if err := p.svc.do(ctx, func(tok string) error {
			var e error
			full, e = p.svc.client.GetTransaction(ctx, tok, t.ID)
			return e
		}); err != nil {
			return event{}, false
		}
		caller = full.CallerHandle
		if len(args) == 0 {
			args = full.Args
		}
	}
	if caller == "" {
		return event{}, false
	}
	var a eventArgs
	_ = json.Unmarshal(args, &a)
	return event{TxID: t.ID, Action: t.ActionName, Caller: caller, At: t.StartedAt, Args: a}, true
}

// run polls on an interval until ctx is cancelled (an immediate first poll seeds
// the store from the ledger).
func (p *projector) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = p.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ---- in-process facade ----

// Daemon is the running @chat application: its kernel identity, the projected
// store, and the projection loop. The chat plugin holds one and reads/writes it
// via direct Go calls (no HTTP between them).
type Daemon struct {
	svc    *service
	store  *Store
	proj   *projector
	cancel context.CancelFunc

	mu    sync.Mutex
	ready bool
}

// New builds the @chat application. actionSource is the absolute URL the kernel
// calls back for each @chat action (juice-ui's /_kernel/chat/ack).
func New(c *client.Client, handle, password, actionSource string) *Daemon {
	store := NewStore()
	svc := newService(c, handle, password, actionSource)
	return &Daemon{svc: svc, store: store, proj: newProjector(svc, store)}
}

// Start brings the app up in the background: authenticate as @chat (retrying
// until the kernel is reachable), ensure the actions exist, then run the ledger
// projection until Stop. Backgrounded so juice-ui starts even when the kernel is
// briefly unavailable.
func (d *Daemon) Start(ctx context.Context) {
	bg, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go func() {
		for {
			if err := d.svc.login(bg); err == nil {
				break
			}
			select {
			case <-bg.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
		if err := d.svc.ensureActions(bg); err != nil {
			return
		}
		d.setReady(true)
		d.proj.run(bg, pollInterval)
	}()
}

// Stop halts the projection and background work.
func (d *Daemon) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// Poll folds the latest ledger events into the store now, so a write is visible
// on the read that follows it.
func (d *Daemon) Poll(ctx context.Context) error { return d.proj.poll(ctx) }

// Ready reports whether the app has authenticated and registered its actions.
func (d *Daemon) Ready() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ready
}

func (d *Daemon) setReady(v bool) {
	d.mu.Lock()
	d.ready = v
	d.mu.Unlock()
}

// Rooms returns the rooms handle is a member of, newest first.
func (d *Daemon) Rooms(handle string) []Room { return d.store.RoomsFor(handle) }

// Room returns a room and its messages for a member; ok is false if missing,
// deleted, or handle is not a member.
func (d *Daemon) Room(id, handle string) (Room, []Message, bool) { return d.store.Room(id, handle) }
