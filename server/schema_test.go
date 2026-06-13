package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildFormBasic(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string","description":"Search query"},
			"limit":{"type":"integer","description":"Max results"}
		},
		"required":["query"]
	}`)
	fm := BuildForm(schema)
	if fm.UseRawFallback {
		t.Fatal("should not fall back for a valid object schema")
	}
	if len(fm.Fields) != 2 {
		t.Fatalf("fields = %d want 2", len(fm.Fields))
	}
	// order preserved: query before limit
	if fm.Fields[0].Name != "query" || fm.Fields[1].Name != "limit" {
		t.Errorf("field order not preserved: %+v", fm.Fields)
	}
	if !fm.Fields[0].Required {
		t.Error("query should be required")
	}
	if fm.Fields[1].Required {
		t.Error("limit should be optional")
	}
	if fm.Fields[0].Type != "string" || fm.Fields[1].Type != "integer" {
		t.Errorf("types wrong: %+v", fm.Fields)
	}
	if fm.Fields[0].Description != "Search query" {
		t.Errorf("description lost: %q", fm.Fields[0].Description)
	}
}

func TestBuildFormEnum(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string","enum":["fast","slow"]}}}`)
	fm := BuildForm(schema)
	if len(fm.Fields) != 1 || len(fm.Fields[0].Enum) != 2 {
		t.Fatalf("enum not parsed: %+v", fm.Fields)
	}
	if fm.Fields[0].Enum[0] != "fast" || fm.Fields[0].Enum[1] != "slow" {
		t.Errorf("enum values = %v", fm.Fields[0].Enum)
	}
}

func TestBuildFormFallbacks(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{"empty", ``},
		{"null", `null`},
		{"non-object type", `{"type":"string"}`},
		{"object without properties", `{"type":"object"}`},
		{"unconstrained (no type)", `{}`},
		{"garbage", `not json`},
	}
	for _, c := range cases {
		fm := BuildForm(json.RawMessage(c.schema))
		if !fm.UseRawFallback {
			t.Errorf("%s: expected raw fallback", c.name)
		}
	}
}

func TestBuildFormNestedOrderPreserved(t *testing.T) {
	// Properties whose values are nested objects/arrays must not break key
	// ordering (the streaming decoder skips whole values).
	schema := json.RawMessage(`{"type":"object","properties":{
		"a":{"type":"string"},
		"b":{"type":"object","properties":{"x":{"type":"string"}}},
		"c":{"type":"array"}
	}}`)
	fm := BuildForm(schema)
	got := []string{}
	for _, f := range fm.Fields {
		got = append(got, f.Name)
	}
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("order = %v want a,b,c", got)
	}
}

func TestSchemaExampleNested(t *testing.T) {
	// llm/chat-shaped schema: messages is an array of {role, content} objects.
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"system":{"type":"string","description":"Optional system prompt"},
			"messages":{"type":"array","items":{"type":"object","properties":{
				"role":{"type":"string"},"content":{"type":"string"}}}}
		},
		"required":["messages"]
	}`)
	ex := SchemaExample(schema)
	// Must be valid JSON and reveal the nested shape.
	if !json.Valid([]byte(ex)) {
		t.Fatalf("example is not valid JSON:\n%s", ex)
	}
	for _, want := range []string{`"messages"`, `"role"`, `"content"`, `"system"`, "[", "{"} {
		if !strings.Contains(ex, want) {
			t.Errorf("example missing %q:\n%s", want, ex)
		}
	}
	// The array field carries its own skeleton for pre-filling its textarea.
	fm := BuildForm(schema)
	var msgs Field
	for _, f := range fm.Fields {
		if f.Name == "messages" {
			msgs = f
		}
	}
	if msgs.Type != "array" || !json.Valid([]byte(msgs.Example)) || !strings.Contains(msgs.Example, `"role"`) {
		t.Errorf("messages field example wrong: %q", msgs.Example)
	}
}

func TestSchemaExampleScalars(t *testing.T) {
	ex := SchemaExample(json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"},"n":{"type":"integer"},"b":{"type":"boolean"}}}`))
	for _, want := range []string{`"x": ""`, `"n": 0`, `"b": false`} {
		if !strings.Contains(ex, want) {
			t.Errorf("example missing %q:\n%s", want, ex)
		}
	}
	// Unconstrained schema yields no skeleton.
	if SchemaExample(json.RawMessage(`{}`)) != "" {
		t.Error("unconstrained schema should produce empty example")
	}
}

func TestBuildStepFormAllEditable(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"]}`)
	sfm := BuildStepForm(schema, nil)
	if sfm.IsApproval {
		t.Error("should not be approval")
	}
	if len(sfm.Locked) != 0 {
		t.Errorf("expected 0 locked, got %d", len(sfm.Locked))
	}
	if len(sfm.Editable.Fields) != 2 {
		t.Errorf("expected 2 editable, got %d", len(sfm.Editable.Fields))
	}
}

func TestBuildStepFormPartialLock(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`)
	partial := json.RawMessage(`{"a":"hello"}`)
	sfm := BuildStepForm(schema, partial)
	if sfm.IsApproval {
		t.Error("should not be approval — b is still editable")
	}
	if len(sfm.Locked) != 1 || sfm.Locked[0].Name != "a" || sfm.Locked[0].Value != `"hello"` {
		t.Errorf("locked wrong: %+v", sfm.Locked)
	}
	if len(sfm.Editable.Fields) != 1 || sfm.Editable.Fields[0].Name != "b" {
		t.Errorf("editable wrong: %+v", sfm.Editable.Fields)
	}
}

func TestBuildStepFormAllLocked(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`)
	partial := json.RawMessage(`{"a":"hello","b":42}`)
	sfm := BuildStepForm(schema, partial)
	if !sfm.IsApproval {
		t.Error("all fields locked → IsApproval should be true")
	}
	if len(sfm.Locked) != 2 {
		t.Errorf("expected 2 locked, got %d", len(sfm.Locked))
	}
	if len(sfm.Editable.Fields) != 0 {
		t.Errorf("expected 0 editable, got %d", len(sfm.Editable.Fields))
	}
}

func TestBuildStepFormInvalidPartial(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	for _, bad := range []json.RawMessage{[]byte("not json"), []byte(""), nil} {
		sfm := BuildStepForm(schema, bad)
		if sfm.IsApproval {
			t.Error("invalid partial_args should produce all-editable form")
		}
		if len(sfm.Locked) != 0 {
			t.Errorf("invalid partial_args should produce 0 locked, got %d", len(sfm.Locked))
		}
		if len(sfm.Editable.Fields) != 1 {
			t.Errorf("invalid partial_args should keep all fields editable")
		}
	}
}

func TestBuildStepFormExtraKeysInPartial(t *testing.T) {
	// partial_args may contain keys not in the schema (forward-compat).
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	partial := json.RawMessage(`{"a":"v","extra":true}`)
	sfm := BuildStepForm(schema, partial)
	if !sfm.IsApproval {
		t.Error("all schema fields locked → IsApproval should be true")
	}
	if len(sfm.Locked) != 2 {
		t.Errorf("both schema and extra keys locked, expected 2, got %d", len(sfm.Locked))
	}
}
