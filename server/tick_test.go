package server

import (
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// The tick answers each watch of both lanes in order through the watcher of
// its kind, ignores a kind nobody registered, skips a watcher that fails so
// the others still answer, and answers a page without watches with nothing.
// (The kernel-gone sign-out, the badge, process and peer watchers are proved
// through the full stack in handlers_test.go and admin_test.go.)
func TestTickAnswersEachWatchAndSkipsTheRest(t *testing.T) {
	k := authKernel()
	defer k.Close()
	cl := client.New(k.URL, nil)
	s := &Server{client: cl, sessions: session.NewManager(scs.New(), cl),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), watchers: map[string]apps.Watcher{}}
	s.watch("a", func(r *http.Request, key, shown string) (template.HTML, error) {
		return template.HTML("<a>" + key + ":" + shown + "</a>"), nil
	})
	s.watch("bad", func(*http.Request, string, string) (template.HTML, error) { return "", errors.New("boom") })

	rec := httptest.NewRecorder()
	s.getTick(rec, httptest.NewRequest(http.MethodGet, "/tick?watch=a:x:1&slow=bad:y:2&slow=nobody:z&watch=a:w", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "<a>x:1</a><a>w:</a>" {
		t.Fatalf("tick = %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	rec = httptest.NewRecorder()
	s.getTick(rec, httptest.NewRequest(http.MethodGet, "/tick", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("a page without watches: %d %q", rec.Code, rec.Body.String())
	}
}

// A watcher saying what the page shows is gone sends the browser where it
// names, with nothing else; an htmx handler saying so does the same.
func TestAPageWhoseContentMovedIsSentOn(t *testing.T) {
	k := authKernel()
	defer k.Close()
	cl := client.New(k.URL, nil)
	s := &Server{client: cl, sessions: session.NewManager(scs.New(), cl),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), watchers: map[string]apps.Watcher{}}
	s.watch("a", func(r *http.Request, key, shown string) (template.HTML, error) { return "<a></a>", nil })
	s.watch("gone", func(*http.Request, string, string) (template.HTML, error) { return "", apps.Moved("/x") })
	req := httptest.NewRequest(http.MethodGet, "/tick?watch=a:1&watch=gone:2", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	s.getTick(rec, req)
	if rec.Header().Get("HX-Redirect") != "/x" || rec.Body.Len() != 0 {
		t.Errorf("tick = %v %q", rec.Header(), rec.Body.String())
	}
	rec = httptest.NewRecorder()
	if !s.handleErrFragment(rec, req, apps.Moved("/x")) || rec.Header().Get("HX-Redirect") != "/x" || rec.Body.Len() != 0 {
		t.Errorf("handler = %v %q", rec.Header(), rec.Body.String())
	}
}
