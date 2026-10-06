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
	// The array field starts with one row of its item's fields, at its index path.
	fm := BuildForm(schema)
	var msgs Field
	for _, f := range fm.Fields {
		if f.Name == "messages" {
			msgs = f
		}
	}
	if msgs.Type != "array" || len(msgs.Rows) != 1 || len(msgs.Rows[0].Fields) != 2 || msgs.Rows[0].Fields[0].Path != "/messages/0/role" {
		t.Errorf("messages field rows wrong: %+v", msgs.Rows)
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

func TestBuildTaskFormAllEditable(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"]}`)
	sfm := BuildTaskForm(schema, nil)
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

func TestBuildTaskFormPartialLock(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`)
	partial := json.RawMessage(`{"a":"hello"}`)
	sfm := BuildTaskForm(schema, partial)
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

func TestBuildTaskFormAllLocked(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`)
	partial := json.RawMessage(`{"a":"hello","b":42}`)
	sfm := BuildTaskForm(schema, partial)
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

func TestBuildTaskFormInvalidPartial(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	for _, bad := range []json.RawMessage{[]byte("not json"), []byte(""), nil} {
		sfm := BuildTaskForm(schema, bad)
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

// A free-form schema (no named fields, e.g. sys/sink) whose payload is already
// pre-bound leaves nothing for the completer to fill → approval, no raw box.
func TestBuildTaskFormFreeFormLockedIsApproval(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`) // free-form → UseRawFallback
	partial := json.RawMessage(`{"message":"hi there"}`)
	sfm := BuildTaskForm(schema, partial)
	if !sfm.IsApproval {
		t.Error("free-form task with pre-bound payload → IsApproval should be true (just acknowledge, no raw box)")
	}
	if len(sfm.Editable.Fields) != 0 {
		t.Errorf("free-form approval has no editable fields, got %d", len(sfm.Editable.Fields))
	}
	if len(sfm.Locked) != 1 {
		t.Errorf("the pre-bound key is locked, expected 1, got %d", len(sfm.Locked))
	}
}

// A free-form schema with NO pre-bound args still needs input from the completer,
// so it is not an approval — it shows the raw editor.
func TestBuildTaskFormFreeFormNoLock(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	sfm := BuildTaskForm(schema, nil)
	if sfm.IsApproval {
		t.Error("free-form task with no pre-bound args must not be an approval")
	}
	if !sfm.Editable.UseRawFallback {
		t.Error("free-form task with no lock shows the raw editor")
	}
}

func TestBuildTaskFormExtraKeysInPartial(t *testing.T) {
	// partial_args may contain keys not in the schema (forward-compat).
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	partial := json.RawMessage(`{"a":"v","extra":true}`)
	sfm := BuildTaskForm(schema, partial)
	if !sfm.IsApproval {
		t.Error("all schema fields locked → IsApproval should be true")
	}
	if len(sfm.Locked) != 2 {
		t.Errorf("both schema and extra keys locked, expected 2, got %d", len(sfm.Locked))
	}
}

// An approval-only task has nothing to collect, so the form is its confirm
// button alone. The kernel expresses that with an EXPLICITLY EMPTY properties
// map — distinct from an absent one, which is an unconstrained object where the
// raw editor is right (§10). Both shapes were observed live on v0.12.23.
func TestEmptyPropertiesMeansNothingToFillNotFreeForm(t *testing.T) {
	// Shape seen on a payment task's allowed_input.
	fm := BuildForm(json.RawMessage(`{"type":"object","properties":{},"required":null}`))
	if !fm.NoFields {
		t.Error("explicitly empty properties declares no fields")
	}
	if fm.UseRawFallback {
		t.Error("an empty-properties schema must not offer a raw JSON box — there is nothing to author")
	}
	if len(fm.Fields) != 0 {
		t.Errorf("expected no fields, got %d", len(fm.Fields))
	}
	// The unconstrained shape keeps the raw editor: any object is valid there.
	free := BuildForm(json.RawMessage(`{"type":"object"}`))
	if free.NoFields || !free.UseRawFallback {
		t.Error("an absent properties key is unconstrained, so the raw editor stands")
	}
}

// The task form collapses to a single confirm in both routes: an empty-
// properties schema with nothing bound, and a free-form schema whose keys are
// all sealed in partial_args (the sys/message case).
func TestApprovalCollapsesForBothKernelShapes(t *testing.T) {
	noFields := BuildTaskForm(json.RawMessage(`{"type":"object","properties":{},"required":null}`), nil)
	if !noFields.IsApproval {
		t.Error("empty-properties + nothing bound is an approval, not a raw box")
	}
	if noFields.Editable.UseRawFallback {
		t.Error("an approval must not fall back to the raw editor")
	}
	sealed := BuildTaskForm(json.RawMessage(`{"type":"object"}`), json.RawMessage(`{"message":"please review"}`))
	if !sealed.IsApproval {
		t.Error("a free-form task whose payload is sealed is an approval")
	}
	if len(sealed.Locked) != 1 {
		t.Errorf("the sealed payload renders read-only, got %d locked", len(sealed.Locked))
	}
}

// The kernel stores schemas in the canonical form of its subset (verified live
// on ba7f5e5): a nullable field is `type: [T, "null"]`, keys arrive sorted, and
// a closed object carries additionalProperties:false. One nullable property
// must not send the whole form to the raw editor.
func TestBuildFormCanonicalNullable(t *testing.T) {
	fm := BuildForm(json.RawMessage(`{"additionalProperties":false,"properties":{
		"kind":{"description":"K.","enum":["a","b",null],"type":["string","null"]},
		"nest":{"additionalProperties":false,"description":"N.","properties":{"y":{"description":"Y.","type":"string"}},"type":["object","null"]},
		"opt":{"description":"O.","type":["string","null"]},
		"zeta":{"description":"Z.","type":"string"}},"required":["zeta"],"type":"object"}`))
	if fm.UseRawFallback || len(fm.Fields) != 4 {
		t.Fatalf("a nullable field must decode as a typed field: %+v", fm)
	}
	byName := map[string]Field{}
	for _, f := range fm.Fields {
		byName[f.Name] = f
	}
	if f := byName["opt"]; f.Type != "string" || !f.Nullable {
		t.Errorf("opt = %+v, want a nullable string", f)
	}
	if f := byName["zeta"]; f.Nullable || !f.Required {
		t.Errorf("zeta = %+v, want a required non-nullable string", f)
	}
	// null is the field's own choice, so it is not an enum option.
	if f := byName["kind"]; !f.Nullable || strings.Join(f.Enum, ",") != "a,b" {
		t.Errorf("kind = %+v, want enum a,b and nullable", f)
	}
	if f := byName["nest"]; f.Type != "object" || !f.Nullable || len(f.Fields) != 1 || f.Fields[0].Path != "/nest/y" {
		t.Errorf("nest = %+v, want a nullable object with its one field", f)
	}
}

// Title and the bounds the browser enforces exactly are carried as written;
// nothing else is.
func TestBuildFormHints(t *testing.T) {
	fm := BuildForm(json.RawMessage(`{"properties":{
		"n":{"description":"N.","maximum":9,"minimum":1.5,"default":3,"type":"number"},
		"s":{"description":"S.","maxLength":5,"minLength":2,"pattern":"^a","title":"Short name","type":"string"}},"type":"object"}`))
	n, s := fm.Fields[0], fm.Fields[1]
	if n.Min != "1.5" || n.Max != "9" || n.Value != "" {
		t.Errorf("n = %+v, want min 1.5, max 9, and no pre-filled default", n)
	}
	if s.Title != "Short name" || s.MinLength != "2" || s.MaxLength != "5" {
		t.Errorf("s = %+v, want title and length bounds", s)
	}
}

// The canonical "takes nothing" schema is a closed object with no properties;
// a schema under additionalProperties is a map, whose keys no field list can
// name.
func TestBuildFormClosedEmptyAndMap(t *testing.T) {
	if fm := BuildForm(json.RawMessage(`{"additionalProperties":false,"properties":{},"type":"object"}`)); !fm.NoFields {
		t.Errorf("a closed object with no properties takes nothing: %+v", fm)
	}
	if fm := BuildForm(json.RawMessage(`{"additionalProperties":{"type":"string"},"properties":{},"type":"object"}`)); !fm.UseRawFallback {
		t.Errorf("a map-shaped object needs the raw editor: %+v", fm)
	}
}

// The form is the schema's tree: an object's properties nest under its path, an
// array starts with max(minItems, 1) rows of its item, a map-shaped node is a
// JSON editor with the shape as its placeholder, and keys are pointer-escaped.
func TestBuildFormTree(t *testing.T) {
	fm := BuildForm(json.RawMessage(`{"type":"object","properties":{
		"a/b":{"type":"string","default":"x","format":"email"},
		"date":{"type":"string","format":"date"},
		"depth":{"type":"integer","exclusiveMinimum":0,"exclusiveMaximum":10},
		"meta":{"type":"object","additionalProperties":{"type":"string"}},
		"msgs":{"type":"array","minItems":2,"items":{"type":"object","properties":{
			"role":{"type":"string","enum":["user","system"]},
			"tool":{"type":["object","null"],"properties":{"name":{"type":"string"}}}},"required":["role"]}},
		"tags":{"type":"array","items":{"type":"string"}},
		"loose":{"type":"array"}},"required":["msgs"]}`))
	if fm.UseRawFallback || len(fm.Fields) != 7 {
		t.Fatalf("form = %+v", fm)
	}
	by := map[string]Field{}
	for _, f := range fm.Fields {
		by[f.Name] = f
	}
	if f := by["a/b"]; f.Path != "/a~1b" || f.Default != "x" || f.Value != "" || f.Input != "email" {
		t.Errorf("a/b = %+v: want an escaped path, the default as placeholder only, an email input", f)
	}
	if f := by["date"]; f.Input != "date" {
		t.Errorf("date = %+v", f)
	}
	if f := by["depth"]; f.Min != "1" || f.Max != "9" {
		t.Errorf("depth = %+v: exclusive integer bounds shift by one", f)
	}
	if f := by["meta"]; !f.Map || f.Example != "{}" || f.Fields != nil {
		t.Errorf("meta = %+v: a map node is a JSON editor", f)
	}
	msgs := by["msgs"]
	if !msgs.Required || len(msgs.Rows) != 2 || msgs.Rows[1].Path != "/msgs/1" || msgs.Rows[1].Name != "" {
		t.Fatalf("msgs = %+v", msgs)
	}
	row := msgs.Rows[1]
	if len(row.Fields) != 2 || row.Fields[0].Path != "/msgs/1/role" || !row.Fields[0].Required || len(row.Fields[0].Enum) != 2 {
		t.Errorf("row = %+v", row.Fields)
	}
	if tool := row.Fields[1]; !tool.Nullable || tool.Type != "object" || tool.Fields[0].Path != "/msgs/1/tool/name" {
		t.Errorf("tool = %+v", tool)
	}
	if f := by["tags"]; len(f.Rows) != 1 || f.Rows[0].Type != "string" || f.Rows[0].Path != "/tags/0" {
		t.Errorf("tags = %+v", f)
	}
	if f := by["loose"]; !f.Map || f.Example != "[]" {
		t.Errorf("an array without items is a JSON editor: %+v", f)
	}
	for _, c := range []string{"plain", "a/b", "a~b", "~1/"} {
		if pointerUnescape(pointerEscape(c)) != c {
			t.Errorf("pointer round trip broke %q", c)
		}
	}
}

// A proposal fills the tree: scalars as text, one row per array element, a map
// node as its JSON; a key the schema does not name is left alone.
func TestFormFill(t *testing.T) {
	fm := BuildForm(json.RawMessage(`{"type":"object","properties":{
		"n":{"type":"integer"},
		"meta":{"type":"object","additionalProperties":{"type":"string"}},
		"msgs":{"type":"array","items":{"type":"object","properties":{"role":{"type":"string"},"content":{"type":"string"}}}}}}`))
	fm.Fill(json.RawMessage(`{"n":4,"meta":{"k":"v"},"msgs":[{"role":"user","content":"a"},{"role":"system","content":"b"}],"extra":1}`))
	by := map[string]Field{}
	for _, f := range fm.Fields {
		by[f.Name] = f
	}
	if by["n"].Value != "4" || by["meta"].Value != `{"k":"v"}` {
		t.Errorf("scalars = %q, map = %q", by["n"].Value, by["meta"].Value)
	}
	msgs := by["msgs"]
	if len(msgs.Rows) != 2 || msgs.Rows[1].Path != "/msgs/1" || msgs.Rows[1].Fields[0].Value != "system" || msgs.Rows[0].Fields[1].Value != "a" {
		t.Errorf("rows = %+v", msgs.Rows)
	}
}

// The frozen form keeps the kernel's own order and types: keys as served,
// numbers as written, booleans and null as words, a long string folded, empty
// containers as words, empty or invalid input raw.
func TestFrozenKeepsOrderAndTypes(t *testing.T) {
	f := Frozen(json.RawMessage(`{"z":1.50,"a":[true,null,"x"],"m":{"k":"v"},"e":{},"l":[]}`))
	if f.Type != "object" || len(f.Fields) != 5 || !f.Frozen {
		t.Fatalf("root = %+v", f)
	}
	names := []string{}
	for _, c := range f.Fields {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "z,a,m,e,l" {
		t.Errorf("keys reordered: %v", names)
	}
	if n := f.Fields[0]; n.Type != "number" || n.Value != "1.50" {
		t.Errorf("number = %+v, want its literal text", n)
	}
	arr := f.Fields[1]
	if arr.Type != "array" || len(arr.Rows) != 3 || arr.Rows[0].Value != "true" || arr.Rows[1].Type != "null" || arr.Rows[2].Type != "string" || arr.Rows[2].Name != "" {
		t.Errorf("array = %+v", arr.Rows)
	}
	if m := f.Fields[2]; m.Type != "object" || m.Fields[0].Name != "k" || m.Fields[0].Value != "v" || !m.Fields[0].Frozen {
		t.Errorf("nested object = %+v", m)
	}
	if e, l := f.Fields[3], f.Fields[4]; e.Type != "null" || e.Value != "{}" || l.Type != "null" || l.Value != "[]" {
		t.Errorf("empties = %+v %+v", e, l)
	}
	long := strings.Repeat("é", longText+1)
	b, _ := json.Marshal(long)
	if v := Frozen(b); !v.Long || v.Value != long {
		t.Errorf("a string past %d characters folds: %+v", longText, v.Long)
	}
	if v := Frozen(json.RawMessage(`"short"`)); v.Long || v.Type != "string" {
		t.Error("a short string does not fold")
	}
	if v := Frozen(nil); v.Type != "raw" || v.Value != "" {
		t.Errorf("empty = %+v", v)
	}
	if v := Frozen(json.RawMessage(`{broken`)); v.Type != "raw" || v.Value != "{broken" {
		t.Errorf("invalid = %+v", v)
	}
}
