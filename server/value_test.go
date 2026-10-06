package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// The value tree keeps the kernel's own order and types: keys as served,
// numbers as written, booleans and null as words.
func TestValueOfKeepsOrderAndTypes(t *testing.T) {
	v := valueOf(json.RawMessage(`{"z":1.50,"a":[true,null,"x"],"m":{"k":"v"}}`))
	if v.Kind != "object" || len(v.Entries) != 3 {
		t.Fatalf("value = %+v", v)
	}
	if v.Entries[0].Key != "z" || v.Entries[1].Key != "a" || v.Entries[2].Key != "m" {
		t.Errorf("keys reordered: %+v", v.Entries)
	}
	if n := v.Entries[0].Value; n.Kind != "number" || n.Text != "1.50" {
		t.Errorf("number = %+v, want its literal text", n)
	}
	arr := v.Entries[1].Value
	if arr.Kind != "array" || len(arr.Items) != 3 {
		t.Fatalf("array = %+v", arr)
	}
	if arr.Items[0].Kind != "bool" || arr.Items[0].Text != "true" || arr.Items[1].Kind != "null" || arr.Items[2].Kind != "string" {
		t.Errorf("array items = %+v", arr.Items)
	}
	if m := v.Entries[2].Value; m.Kind != "object" || m.Entries[0].Value.Text != "v" {
		t.Errorf("nested object = %+v", m)
	}
}

// A long string folds; a short one does not; empty or invalid input is raw.
func TestValueOfLongAndRaw(t *testing.T) {
	long := strings.Repeat("é", longText+1)
	b, _ := json.Marshal(long)
	if v := valueOf(b); !v.Long || v.Text != long {
		t.Errorf("a string past %d characters folds: %+v", longText, v.Long)
	}
	if v := valueOf(json.RawMessage(`"short"`)); v.Long {
		t.Error("a short string does not fold")
	}
	if v := valueOf(nil); v.Kind != "raw" || v.Text != "" {
		t.Errorf("empty = %+v", v)
	}
	if v := valueOf(json.RawMessage(`{broken`)); v.Kind != "raw" || v.Text != "{broken" {
		t.Errorf("invalid = %+v", v)
	}
}

func TestResultDownload(t *testing.T) {
	if got := resultDownload("T1"); got != "/transactions/T1/result.json" {
		t.Errorf("download = %q", got)
	}
	if resultDownload("") != "" {
		t.Error("no transaction, no download")
	}
}
