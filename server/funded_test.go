package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/daios/juice-ui/client"
)

// fakeKernel is a configurable httptest kernel for funded-session tests.
type fakeKernel struct {
	mu          sync.Mutex
	startedFund *int64
	ended       []string
	callStatus  int
	callBody    string
	txBody      string
	listBody    string
}

func (f *fakeKernel) start(t *testing.T) (*client.Client, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Funds int64 `json:"funds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.startedFund = &body.Funds
		f.mu.Unlock()
		w.WriteHeader(201)
		w.Write([]byte(`{"process_id":"ephem-1","available":` + itoa(body.Funds) + `,"trace_id":"tr"}`))
	})
	mux.HandleFunc("POST /v1/processes/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.ended = append(f.ended, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /v1/call", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(f.callStatus)
		w.Write([]byte(f.callBody))
	})
	mux.HandleFunc("GET /v1/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(f.txBody))
	})
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(f.listBody))
	})
	srv := httptest.NewServer(mux)
	return client.New(srv.URL, nil), srv.Close
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestCallEphemeralSuccess(t *testing.T) {
	f := &fakeKernel{
		callStatus: 200,
		callBody:   `{"result":{"answer":42},"tx_id":"tx-9","trace_id":"trace-9"}`,
		txBody:     `{"id":"tx-9","gross":100,"fee":20,"net":80,"status":"success"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).CallEphemeral(context.Background(), "tok", "@sys/lookup", nil)
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
	// The ephemeral process is created at funds 0 and ended (R6.5.1).
	if f.startedFund == nil || *f.startedFund != 0 {
		t.Errorf("ephemeral process not started at funds 0: %v", f.startedFund)
	}
	if len(f.ended) != 1 || f.ended[0] != "ephem-1" {
		t.Errorf("ephemeral process not ended: %v", f.ended)
	}
}

func TestCallEphemeralInputRejection(t *testing.T) {
	f := &fakeKernel{callStatus: 422, callBody: `{"error":"field #.query: required field missing"}`}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).CallEphemeral(context.Background(), "tok", "@sys/lookup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Rejected {
		t.Fatalf("class = %v want Rejected", out.Class)
	}
	if out.TxID != "" || out.Cost != nil {
		t.Error("rejected outcome must have no transaction or cost")
	}
	// process still created and ended even though nothing was charged.
	if len(f.ended) != 1 {
		t.Errorf("process should still be ended: %v", f.ended)
	}
}

func TestCallEphemeralFailureLinksTx(t *testing.T) {
	f := &fakeKernel{
		callStatus: 409,
		callBody:   `{"error":"lookup requires an embedding service"}`,
		listBody:   `[{"id":"tx-fail","status":"failure"},{"id":"tx-old","status":"success"}]`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).CallEphemeral(context.Background(), "tok", "@sys/lookup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Failed {
		t.Fatalf("class = %v want Failed", out.Class)
	}
	if out.FailureTxID != "tx-fail" {
		t.Errorf("failure tx = %q want tx-fail", out.FailureTxID)
	}
	if out.Message == "" {
		t.Error("failed outcome should carry a message")
	}
}

func TestCallFundedUsesProvidedProcess(t *testing.T) {
	f := &fakeKernel{
		callStatus: 200,
		callBody:   `{"result":{"ok":true},"tx_id":"tx-1","trace_id":"t"}`,
		txBody:     `{"id":"tx-1","gross":50,"fee":10,"net":40,"status":"success"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).CallFunded(context.Background(), "tok", "my-session", "@alice/paid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Class != Success || out.Cost.Gross != 50 {
		t.Fatalf("outcome = %+v", out)
	}
	// CallFunded must NOT create or end a process.
	if f.startedFund != nil {
		t.Error("CallFunded should not start a process")
	}
	if len(f.ended) != 0 {
		t.Error("CallFunded should not end a process")
	}
}

func TestCallInsufficientFundsIsError(t *testing.T) {
	f := &fakeKernel{callStatus: 402, callBody: `{"error":"insufficient user balance"}`}
	c, stop := f.start(t)
	defer stop()
	_, err := NewCaller(c).CallFunded(context.Background(), "tok", "p", "@alice/paid", nil)
	if !isKind(err, client.KindInsufficientFunds) {
		t.Errorf("expected insufficient-funds error, got %v", err)
	}
}

func isKind(err error, k client.Kind) bool {
	ke, ok := client.AsKernelError(err)
	return ok && ke.Kind == k
}
