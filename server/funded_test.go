package server

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	out, err := NewCaller(c).Run(context.Background(), "tok", "@sys/lookup", nil)
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
	out, err := NewCaller(c).Run(context.Background(), "tok", "@sys/lookup", nil)
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
		runStatus: 409,
		runBody:   `{"error":"lookup requires an embedding service"}`,
	}
	c, stop := f.start(t)
	defer stop()
	out, err := NewCaller(c).Run(context.Background(), "tok", "@sys/lookup", nil)
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

func TestRunInsufficientFundsIsError(t *testing.T) {
	f := &fakeKernel{
		runStatus: 402,
		runBody:   `{"error":"insufficient user balance"}`,
	}
	c, stop := f.start(t)
	defer stop()
	_, err := NewCaller(c).Run(context.Background(), "tok", "@sys/lookup", nil)
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
		completeStatus: 409,
		completeBody:   `{"error":"action execution failed"}`,
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
