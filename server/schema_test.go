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

func TestFormJSONRoundTrip(t *testing.T) {
	fm := BuildForm(json.RawMessage(`{"type":"object","properties":{"q":{"type":"string","description":"d"}},"required":["q"]}`))
	s, err := fm.FormJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Fields []struct {
			Name        string `json:"name"`
			Required    bool   `json:"required"`
			Description string `json:"description"`
		} `json:"fields"`
		UseRawFallback bool `json:"use_raw_fallback"`
	}
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Fields) != 1 || decoded.Fields[0].Name != "q" || !decoded.Fields[0].Required {
		t.Errorf("round trip wrong: %s", s)
	}
}
