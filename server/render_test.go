package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

func TestNewRendererParsesAllTemplates(t *testing.T) {
	if _, err := NewRenderer(); err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
}

func TestMoneyFormatting(t *testing.T) {
	cases := map[int64]string{0: "0", 5: "5", 100: "100", 1000: "1,000", 1234567: "1,234,567", -2500: "-2,500"}
	for in, want := range cases {
		if got := money(in); got != want {
			t.Errorf("money(%d) = %q want %q", in, got, want)
		}
	}
}

func TestPrettyJSONNoDoubleEncode(t *testing.T) {
	out := prettyJSON(json.RawMessage(`{"a":1}`))
	if !strings.Contains(out, "\"a\": 1") {
		t.Errorf("prettyJSON = %q", out)
	}
	if prettyJSON(nil) != "—" {
		t.Errorf("empty json should render placeholder")
	}
}

func TestStatusClassesDistinct(t *testing.T) {
	// The three financial states must map to three distinct classes (R8.7).
	c := map[string]string{
		"success": statusClass("success"),
		"failure": statusClass("failure"),
		"open":    statusClass("open"),
	}
	seen := map[string]bool{}
	for _, v := range c {
		if v == "" || seen[v] {
			t.Errorf("status classes not distinct/non-empty: %v", c)
		}
		seen[v] = true
	}
}

func TestRenderPageStampsThemeNoLeak(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{
		Title:     "Wallet",
		Theme:     "dark",
		Handle:    "@alice",
		Available: 1000,
		Data:      walletView{User: client.User{Handle: "@alice", Available: 1000, Locked: 250}},
	}
	if err := r.Render(rec, 200, "wallet", p); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-theme="dark"`) {
		t.Error("theme not stamped onto <html>")
	}
	if !strings.Contains(body, "1,000") {
		t.Error("available balance not rendered with money formatting")
	}
	if !strings.Contains(body, "@alice") {
		t.Error("handle not rendered in nav")
	}
}

func TestPct(t *testing.T) {
	cases := []struct{ n, d int64; want string }{{138, 142, "97"}, {0, 0, "—"}, {0, 10, "0"}, {10, 10, "100"}}
	for _, c := range cases {
		if got := pct(c.n, c.d); got != c.want {
			t.Errorf("pct(%d,%d) = %q want %q", c.n, c.d, got, c.want)
		}
	}
}

func TestRatingPct(t *testing.T) {
	cases := []struct {
		est   float64
		count int64
		want  string
	}{{4.8, 23, "96"}, {0, 0, ""}, {5.0, 1, "100"}, {2.5, 10, "50"}}
	for _, c := range cases {
		if got := ratingPct(c.est, c.count); got != c.want {
			t.Errorf("ratingPct(%v,%d) = %q want %q", c.est, c.count, got, c.want)
		}
	}
}

func TestRenderFragment(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: Success, Result: json.RawMessage(`{"ok":true}`), TxID: "tx1"}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Success") || !strings.Contains(body, "/transactions/tx1") {
		t.Errorf("fragment render missing content: %s", body)
	}
}
