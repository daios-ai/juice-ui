package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

// fakeKernel is a configurable httptest kernel for run tests.
type fakeKernel struct {
	runStatus int
	runBody   string
	txBody    string
}

func (f *fakeKernel) start(t *testing.T) (*client.Client, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(f.runStatus)
		w.Write([]byte(f.runBody))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(f.txBody))
	})
	srv := httptest.NewServer(mux)
	return client.New(srv.URL, nil), srv.Close
}

func TestRunSuccess(t *testing.T) {
	f := &fakeKernel{
		runStatus: 200,
		runBody:   `{"result":{"answer":42},"tx_id":"tx-9","trace_id":"trace-9"}`,
		txBody:    `{"id":"tx-9","gross":100,"fee":20,"net":80,"status":"success"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "sys/lookup", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Success {
		t.Fatalf("class = %v want Success", out.Class)
	}
	if out.TxID != "tx-9" {
		t.Errorf("tx = %q", out.TxID)
	}
	if out.Cost == nil || out.Cost.Gross != 100 || out.Cost.Fee != 20 || out.Cost.Net != 80 {
		t.Errorf("cost = %+v", out.Cost)
	}
}

func TestRunInputRejected(t *testing.T) {
	f := &fakeKernel{
		runStatus: 422,
		runBody:   `{"error":"field #.query: required field missing"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "sys/lookup", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Rejected {
		t.Fatalf("class = %v want Rejected", out.Class)
	}
	if out.TxID != "" || out.Cost != nil {
		t.Error("rejected outcome must have no transaction or cost")
	}
}

func TestRunExecutionFailed(t *testing.T) {
	f := &fakeKernel{
		runStatus: 500,
		runBody:   `{"code":"execution_failed","error":"remote call failed"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "sys/lookup", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Failed {
		t.Fatalf("class = %v want Failed", out.Class)
	}
	if out.Message == "" {
		t.Error("failed outcome should carry a message")
	}
}

func TestRunGrantRequired(t *testing.T) {
	f := &fakeKernel{
		runStatus: 403,
		runBody:   `{"error":"grant required","code":"grant_required","meta":{"action":"sys/gmail-send"}}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "sys/gmail-send", nil, "")
	if err != nil {
		t.Fatalf("grant_required must be an outcome, not an error: %v", err)
	}
	if out.Class != GrantRequired || !out.IsGrantRequired() {
		t.Fatalf("class = %v want GrantRequired", out.Class)
	}
	if out.ActionRef != "sys/gmail-send" {
		t.Errorf("ActionRef = %q want sys/gmail-send", out.ActionRef)
	}
}

func TestRunGrantRequiredFallsBackToRunRef(t *testing.T) {
	// If the error metadata omits the action, the ref being run is used.
	f := &fakeKernel{runStatus: 403, runBody: `{"error":"grant required","code":"grant_required"}`}
	c, stop := f.start(t)
	defer stop()
	out, _ := NewCaller(c).Run(context.Background(), "tok", "sys/gmail-send", nil, "")
	if out.ActionRef != "sys/gmail-send" {
		t.Errorf("ActionRef fallback = %q want sys/gmail-send", out.ActionRef)
	}
}

func TestRunPeerUnreachable(t *testing.T) {
	// A remote-proxy call the peer never received: a first-class refunded outcome,
	// not a raw error dumped through the generic path.
	f := &fakeKernel{
		runStatus: 502,
		runBody:   `{"error":"peer offline","code":"peer_unreachable","meta":{"peer":"peerb"}}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "peerb/greet", nil, "")
	if err != nil {
		t.Fatalf("peer_unreachable must be an outcome, not an error: %v", err)
	}
	if out.Class != PeerUnreachable || !out.IsPeerUnreachable() {
		t.Fatalf("class = %v want PeerUnreachable", out.Class)
	}
	if out.Message == "" {
		t.Error("peer-unreachable outcome should carry a message")
	}
}

func TestRunPeerUnfunded(t *testing.T) {
	// Our prepaid credit on the peer is exhausted (an operator condition): a
	// first-class not-charged outcome, distinct from the caller's own funds.
	f := &fakeKernel{
		runStatus: 402,
		runBody:   `{"error":"peer credit exhausted","code":"peer_unfunded","meta":{"peer":"peerb"}}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "peerb/greet", nil, "")
	if err != nil {
		t.Fatalf("peer_unfunded must be an outcome, not an error: %v", err)
	}
	if out.Class != PeerUnfunded || !out.IsPeerUnfunded() {
		t.Fatalf("class = %v want PeerUnfunded", out.Class)
	}
	if out.Message == "" {
		t.Error("peer-unfunded outcome should carry a message")
	}
}

func TestRunInsufficientFundsIsError(t *testing.T) {
	f := &fakeKernel{
		runStatus: 402,
		runBody:   `{"error":"insufficient user balance"}`,
	}
	c, stop := f.start(t)
	defer stop()
	_, err := NewCaller(c).Run(context.Background(), "tok", "sys/lookup", nil, "")
	if !isKind(err, client.KindInsufficientFunds) {
		t.Errorf("expected insufficient-funds error, got %v", err)
	}
}

func isKind(err error, k client.Kind) bool {
	ke, ok := client.AsKernelError(err)
	return ok && ke.Kind == k
}

// stepKernel is a fakeKernel extended with a step-complete endpoint.
type stepKernel struct {
	completeStatus int
	completeBody   string
	txBody         string
}

func (f *stepKernel) start(t *testing.T) (*client.Client, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/steps/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(f.completeStatus)
		w.Write([]byte(f.completeBody))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(f.txBody))
	})
	srv := httptest.NewServer(mux)
	return client.New(srv.URL, nil), srv.Close
}

func TestCompleteStepSuccess(t *testing.T) {
	f := &stepKernel{
		completeStatus: 200,
		completeBody:   `{"result":{"ok":true},"tx_id":"tx-s1","trace_id":"tr-s1","step_id":"S1"}`,
		txBody:         `{"id":"tx-s1","gross":50,"fee":5,"net":45,"status":"success"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).CompleteStep(context.Background(), "tok", "S1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Success {
		t.Fatalf("class = %v want Success", out.Class)
	}
	if out.TxID != "tx-s1" {
		t.Errorf("tx = %q", out.TxID)
	}
	if out.Cost == nil || out.Cost.Gross != 50 || out.Cost.Fee != 5 || out.Cost.Net != 45 {
		t.Errorf("cost = %+v", out.Cost)
	}
}

func TestCompleteStepRejected(t *testing.T) {
	f := &stepKernel{
		completeStatus: 422,
		completeBody:   `{"error":"field #.name: required field missing"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).CompleteStep(context.Background(), "tok", "S1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Rejected {
		t.Fatalf("class = %v want Rejected", out.Class)
	}
	if out.TxID != "" || out.Cost != nil {
		t.Error("rejected outcome must have no transaction or cost")
	}
}

func TestCompleteStepFailed(t *testing.T) {
	f := &stepKernel{
		completeStatus: 500,
		completeBody:   `{"code":"execution_failed","error":"remote call failed"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).CompleteStep(context.Background(), "tok", "S1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Failed {
		t.Fatalf("class = %v want Failed", out.Class)
	}
	if out.Message == "" {
		t.Error("failed outcome should carry a message")
	}
}

// A 409 is refused BEFORE execution — the action never ran and nothing was
// charged — so it must not read as a failure with a refund. Splitting it out of
// Failed is what keeps "refunded" from being claimed over money that never moved.
func TestRunInvalidStateIsRejectedNotFailed(t *testing.T) {
	f := &fakeKernel{
		runStatus: 409,
		runBody:   `{"code":"invalid_state","error":"action is inactive"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "lena/greet", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Rejected {
		t.Fatalf("class = %v want Rejected", out.Class)
	}
	// The "not charged" fact is the panel's headline; the message says what was
	// wrong and must not repeat it (§3 copy discipline).
	if out.Message != "action is inactive" {
		t.Errorf("message should state the reason alone, got %q", out.Message)
	}
	if strings.Contains(out.Message, "not charged") {
		t.Errorf("the message must not repeat the headline: %q", out.Message)
	}
}

// A refused quote carries the CURRENT terms, so the form can re-pin in one round
// trip instead of re-reading the action. Both values arrive as strings on the
// wire (verified live on v0.12.16).
func TestRunTermsChangedCarriesNewTerms(t *testing.T) {
	f := &fakeKernel{
		runStatus: 409,
		runBody:   `{"code":"terms_changed","error":"the action's terms changed; it now costs 500","meta":{"price":"500","quote_hash":"NEWHASH"}}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "lena/greet", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != TermsChanged {
		t.Fatalf("class = %v want TermsChanged", out.Class)
	}
	if out.Price != 500 || out.QuoteHash != "NEWHASH" {
		t.Errorf("new terms not carried: price=%d hash=%q", out.Price, out.QuoteHash)
	}
	if !strings.Contains(out.Message, "$0.500") {
		t.Errorf("the message must name the new price in dollars: %q", out.Message)
	}
	if strings.Contains(out.Message, "refund") {
		t.Errorf("nothing was locked, so nothing was refunded: %q", out.Message)
	}
}
