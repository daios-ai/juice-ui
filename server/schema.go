package server

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Field is one node of the run form, derived from a node of the input schema.
// The form is the schema's tree (requirements.md §6.2): an object renders its
// Fields as a group, an array its Rows, a json node a JSON editor, and every
// other node one input.
type Field struct {
	// Name is the property key; Path is the node's JSON Pointer (RFC 6901), the
	// wire name under the f_/t_/n_ prefixes. An array element has no Name.
	Name, Path string
	// Type is the schema's, or json for a node the schema leaves open — `{}`,
	// an object without properties, an array without items — edited as JSON.
	Type     string // string | integer | number | boolean | object | array | json
	Nullable bool   // the schema admits null (`type: [T, "null"]`)
	Required bool
	// Title is the property's own short label, shown beside its key.
	Title       string
	Description string
	Enum        []string
	// The bounds the browser enforces exactly as the schema means them, as
	// written; empty when the schema states none. The kernel validates the rest.
	Min, Max             string
	MinLength, MaxLength string
	Default              string // the schema's default, shown as a placeholder and never submitted
	Input                string // an input type from `format`: date | email | url; "" for text
	// Value pre-fills the input. It is used where something already proposed an
	// argument — the model's selection (§6.2) — and is empty otherwise. A
	// pre-filled field is still an ordinary field: it is editable, it is
	// submitted like any other, and nothing about it is trusted.
	Value   string
	Null    bool            // a nullable field filled with null: its null box is ticked
	Fields  []Field         // an object's properties, in the order served
	Rows    []Field         // an array's elements, at Path/0, Path/1, …
	Example string          // a compact JSON skeleton: a json node's placeholder
	Secret  bool            // a write-only input, never filled (the configuration's keys)
	items   json.RawMessage // an array's item schema, for one more row
	// MinItems is an array's schema minItems: the rows the kernel demands once
	// the array is sent.
	MinItems int
	// ReadOnly marks a node of the view (§7) — a JSON value: a result, a
	// receipt's args, sealed partial_args — or a fixed key of the config form,
	// in the form's own anatomy, each control a read-only box with no wire name.
	// Long marks a read-only string past longText, folded.
	ReadOnly, Long bool
	// Open says a container renders open: an unnamed node always, a named one
	// while unsettled (settle).
	Open bool
}

// longText is the length past which a read-only string folds, so a page body
// or a compiled artifact does not push the rest of the result off screen.
const longText = 800

// View builds the view of a JSON value (§7). Object keys keep the order
// served; a number keeps its literal text; null, `{}` and `[]` are words in a
// box; empty or invalid input is `raw`, shown as it came.
func View(raw json.RawMessage) Field {
	f := viewField("", raw)
	f.settle(true)
	return f
}

func viewField(name string, raw json.RawMessage) Field {
	raw = bytes.TrimSpace(raw)
	f := Field{Name: name, ReadOnly: true}
	if len(raw) == 0 || !json.Valid(raw) {
		f.Type, f.Value = "raw", string(raw)
		return f
	}
	switch raw[0] {
	case '{':
		keys := orderedKeys(raw)
		if len(keys) == 0 {
			f.Type, f.Value = "null", "{}"
			return f
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		f.Type = "object"
		for _, k := range keys {
			f.Fields = append(f.Fields, viewField(k, m[k]))
		}
	case '[':
		var items []json.RawMessage
		_ = json.Unmarshal(raw, &items)
		if len(items) == 0 {
			f.Type, f.Value = "null", "[]"
			return f
		}
		f.Type = "array"
		for _, it := range items {
			f.Rows = append(f.Rows, viewField("", it))
		}
	case '"':
		f.Type = "string"
		_ = json.Unmarshal(raw, &f.Value)
		f.Long = utf8.RuneCountInString(f.Value) > longText
	case 'n':
		f.Type, f.Value = "null", "null"
	case 't', 'f':
		f.Type, f.Value = "boolean", string(raw)
	default:
		f.Type, f.Value = "number", string(raw)
	}
	return f
}

// Size names a container's extent for its summary: "3 fields", "1 item".
func (f Field) Size() string {
	n, word := len(f.Fields), "field"
	if f.Type == "array" {
		n, word = len(f.Rows), "item"
	}
	if n != 1 {
		word += "s"
	}
	return strconv.Itoa(n) + " " + word
}

// holds reports whether a value sits anywhere under f: the node is in use, so
// it will be sent.
func (f *Field) holds() bool {
	if f.Null || f.Value != "" {
		return true
	}
	for i := range f.Fields {
		if f.Fields[i].holds() {
			return true
		}
	}
	for i := range f.Rows {
		if f.Rows[i].holds() {
			return true
		}
	}
	return false
}

// settle decides Open for f and every node under it. demanded says f must be
// sent: it is the root, or its parent is sent and the schema requires it. A
// node is sent when demanded or when it holds a value, and a required rule
// binds only under a node being sent — JSON Schema's `required` applies to an
// object that is present, and an empty container is omitted on submit
// (§6.2). It reports whether f is unsettled: demanded and empty — a scalar
// without a value, a row holding nothing, which submit drops and `minItems`
// then misses — or sent and holding an unsettled node. A named container opens while unsettled; an
// unnamed one — the root, an array's element — has no summary to fold behind
// and is always open. A view demands nothing, so its named nodes close (§7).
func (f *Field) settle(demanded bool) bool {
	unsettled := false
	sent := demanded || f.holds()
	switch {
	case f.Null:
	case f.Type == "object":
		for i := range f.Fields {
			unsettled = f.Fields[i].settle(sent && f.Fields[i].Required) || unsettled
		}
	case f.Type == "array":
		for i := range f.Rows {
			demanded := sent && i < f.MinItems
			unsettled = f.Rows[i].settle(demanded) || demanded && !f.Rows[i].holds() || unsettled
		}
	default:
		unsettled = demanded && f.Value == ""
	}
	f.Open = f.Name == "" || unsettled
	return unsettled
}

// settle settles each root field by its own requiredness: the root is sent.
func settle(fields []Field) {
	for i := range fields {
		fields[i].settle(fields[i].Required)
	}
}

// FormModel is the server-side model the run form is rendered from (R6.5.4). The
// server decides field types, requiredness, help text, and whether a usable
// object schema exists; the browser only hydrates inputs. The kernel remains the
// validation authority (R2.5).
type FormModel struct {
	Fields         []Field
	UseRawFallback bool // no usable object schema -> offer only the raw-JSON editor
	// NoFields marks a schema that IS a well-formed object and asks for nothing:
	// there is no input to collect, so the form is just its confirm button. It is
	// distinct from UseRawFallback, which means "shape unknown, type it yourself"
	// — offering a JSON box here would ask a person to author `{}` by hand. The
	// kernel's canonical form of "takes nothing" is
	// `{"type":"object","properties":{},"additionalProperties":false}`, while a
	// bare `{"type":"object"}` admits any object (verified live, ba7f5e5).
	NoFields bool
	Example  string // a pretty-printed JSON skeleton of the whole input, for the raw editor
}

// schemaType is `type` as the kernel's schema subset writes it (D4): one name,
// or `[T, "null"]` for a field that may be null.
type schemaType struct {
	Name     string
	Nullable bool
}

func (t *schemaType) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &t.Name); err == nil {
		return nil
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return err
	}
	for _, n := range names {
		if n == "null" {
			t.Nullable = true
		} else {
			t.Name = n
		}
	}
	return nil
}

// schemaDoc is one schema node, as far as the form renderer reads it.
type schemaDoc struct {
	Type        schemaType        `json:"type"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Enum        []json.RawMessage `json:"enum"`
	Default     json.RawMessage   `json:"default"`
	Format      string            `json:"format"`
	Minimum     json.RawMessage   `json:"minimum"`
	Maximum     json.RawMessage   `json:"maximum"`
	ExclMin     json.RawMessage   `json:"exclusiveMinimum"`
	ExclMax     json.RawMessage   `json:"exclusiveMaximum"`
	MinLength   json.RawMessage   `json:"minLength"`
	MaxLength   json.RawMessage   `json:"maxLength"`
	MinItems    int               `json:"minItems"`
	Items       json.RawMessage   `json:"items"`
	Properties  json.RawMessage   `json:"properties"`
	Required    []string          `json:"required"`
}

// BuildForm parses an input_schema into a FormModel. Any schema that is not an
// object schema naming its fields yields UseRawFallback so the user can still
// submit raw JSON args.
func BuildForm(inputSchema json.RawMessage) FormModel {
	if len(bytes.TrimSpace(inputSchema)) == 0 {
		return FormModel{UseRawFallback: true}
	}
	var doc schemaDoc
	if err := json.Unmarshal(inputSchema, &doc); err != nil {
		return FormModel{UseRawFallback: true}
	}
	// No `properties` key at all is an UNCONSTRAINED object — any keys are
	// valid, so the completer needs the raw editor. An explicitly EMPTY
	// `properties` is the opposite claim: this input declares no fields, so
	// there is nothing to collect and the form is its confirm button alone.
	if doc.Type.Name != "object" || len(bytes.TrimSpace(doc.Properties)) == 0 {
		return FormModel{UseRawFallback: true}
	}
	fields := childFields("", doc)
	if fields == nil {
		return FormModel{NoFields: true}
	}
	settle(fields)
	return FormModel{Fields: fields, Example: SchemaExample(inputSchema)}
}

// childFields builds an object's property fields under path, in the order the
// kernel serves them (canonical, keys sorted; verified live, ba7f5e5). Nil when
// the object names no property.
func childFields(path string, doc schemaDoc) []Field {
	keys := orderedKeys(doc.Properties)
	if len(keys) == 0 {
		return nil
	}
	var props map[string]json.RawMessage
	_ = json.Unmarshal(doc.Properties, &props)
	required := make(map[string]bool, len(doc.Required))
	for _, r := range doc.Required {
		required[r] = true
	}
	fields := make([]Field, 0, len(keys))
	for _, k := range keys {
		fields = append(fields, buildField(path+"/"+pointerEscape(k), k, props[k], required[k]))
	}
	return fields
}

// buildField builds the field tree of one schema node. An array starts with
// max(minItems, 1) rows: a row left blank is omitted on submit.
func buildField(path, name string, raw json.RawMessage, required bool) Field {
	var p schemaDoc
	_ = json.Unmarshal(raw, &p)
	f := Field{
		Name: name, Path: path, Type: p.Type.Name, Nullable: p.Type.Nullable, Required: required,
		Title: p.Title, Description: p.Description, Enum: enumStrings(p.Enum),
		Min: string(p.Minimum), Max: string(p.Maximum),
		MinLength: string(p.MinLength), MaxLength: string(p.MaxLength),
		Default: scalarText(p.Default), Input: inputType(p.Format), MinItems: p.MinItems,
	}
	// HTML has no exclusive bound; for an integer the next value is exact.
	if f.Type == "integer" {
		if f.Min == "" {
			f.Min = shifted(p.ExclMin, 1)
		}
		if f.Max == "" {
			f.Max = shifted(p.ExclMax, -1)
		}
	}
	switch f.Type {
	case "", "object":
		if f.Fields = childFields(path, p); f.Fields == nil {
			f.Type, f.Example = "json", skeleton(raw)
		}
	case "array":
		if len(bytes.TrimSpace(p.Items)) == 0 {
			f.Type, f.Example = "json", "[]"
			break
		}
		f.items = p.Items
		for len(f.Rows) < max(p.MinItems, 1) {
			f.addRow()
		}
	}
	return f
}

// addRow appends one more element to an array field.
func (f *Field) addRow() {
	f.Rows = append(f.Rows, buildField(f.Path+"/"+strconv.Itoa(len(f.Rows)), "", f.items, false))
}

// Fill pre-fills the form from an args object (§6.2): scalars become input
// values, an array gets one row per element, a json node its JSON, a null a
// ticked null box. It returns each value the form cannot hold exactly, as its
// JSON Pointer and the reason, in pointer order: a key the form has no field
// for, a value of another kind than its field's, a null where none is
// admitted, and an empty value a blank control would drop. A proposal leaves
// those to the raw editor; the JSON switch refuses to convert.
func (fm *FormModel) Fill(args json.RawMessage) []string {
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil || m == nil {
		return []string{"/: not a JSON object"}
	}
	inexact := fillObject("", fm.Fields, m)
	settle(fm.Fields)
	return inexact
}

// fillObject fills fields from an object's members, reporting each member it
// has no field for.
func fillObject(path string, fields []Field, m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var inexact []string
	for _, k := range keys {
		i := slices.IndexFunc(fields, func(f Field) bool { return f.Name == k })
		if i < 0 {
			inexact = append(inexact, path+"/"+pointerEscape(k)+": the form has no field for it")
			continue
		}
		inexact = append(inexact, fields[i].fill(m[k])...)
	}
	return inexact
}

// takes names what a field of each type holds, for a reason.
var takes = map[string]string{
	"string": "text", "integer": "an integer", "number": "a number",
	"boolean": "true or false", "object": "an object", "array": "a list",
}

func (f *Field) fill(v json.RawMessage) []string {
	v = bytes.TrimSpace(v)
	if f.Type == "json" { // its text is the value, null included
		f.Value = string(v)
		return nil
	}
	if string(v) == "null" {
		if !f.Nullable {
			return []string{f.Path + ": null is not admitted here"}
		}
		f.Null = true
		return nil
	}
	wrong := []string{f.Path + ": the field takes " + takes[f.Type]}
	switch f.Type {
	case "object":
		var m map[string]json.RawMessage
		if json.Unmarshal(v, &m) != nil {
			return wrong
		}
		if len(m) == 0 && !f.Required {
			return []string{f.Path + ": an empty object can only be written in JSON"}
		}
		return fillObject(f.Path, f.Fields, m)
	case "array":
		var items []json.RawMessage
		if json.Unmarshal(v, &items) != nil {
			return wrong
		}
		if len(items) == 0 {
			if f.Required {
				return nil
			}
			return []string{f.Path + ": an empty list can only be written in JSON"}
		}
		f.Rows = nil
		var inexact []string
		for _, it := range items {
			f.addRow()
			inexact = append(inexact, f.Rows[len(f.Rows)-1].fill(it)...)
		}
		return inexact
	}
	// A scalar holds exactly the JSON kind its field submits.
	text := scalarText(v)
	var ok bool
	switch f.Type {
	case "string":
		ok = v[0] == '"'
		if ok && text == "" {
			return []string{f.Path + ": an empty string can only be written in JSON"}
		}
	case "integer":
		_, err := strconv.ParseInt(string(v), 10, 64)
		ok = err == nil
	case "number":
		_, err := strconv.ParseFloat(string(v), 64)
		ok = err == nil
	case "boolean":
		ok = string(v) == "true" || string(v) == "false"
	}
	if !ok {
		return wrong
	}
	if f.Enum != nil && !slices.Contains(f.Enum, text) {
		return []string{f.Path + ": not one of its choices"}
	}
	f.Value = text
	return nil
}

// scalarText is a JSON scalar as a person would type it: a string without its
// quotes, anything else as written. Empty for no value.
func scalarText(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(bytes.TrimSpace(v))
}

// inputType maps the formats HTML enforces as the schema means them. date-time
// is left out: datetime-local submits no zone and fails RFC 3339.
func inputType(format string) string {
	switch format {
	case "date", "email":
		return format
	case "uri":
		return "url"
	}
	return ""
}

// shifted is an exclusive integer bound moved by d onto the inclusive one.
func shifted(raw json.RawMessage, d int64) string {
	n, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(n+d, 10)
}

// pointerEscape encodes one JSON Pointer reference token (RFC 6901 §3).
func pointerEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// pointerUnescape decodes one reference token (RFC 6901 §4: ~1 first, then ~0).
func pointerUnescape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}

// orderedKeys returns an object's keys in the order they are served by
// streaming the JSON object, rather than Go's randomized map iteration. The
// kernel stores schemas canonical, keys sorted, so a schema's fields arrive in
// that order (verified live, ba7f5e5); partial_args keep the order they carry.
func orderedKeys(obj json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(obj))
	// expect '{'
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return keys
		}
		key, ok := keyTok.(string)
		if !ok {
			return keys
		}
		keys = append(keys, key)
		// skip the value (handles nested objects/arrays via a RawMessage decode)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}

// SchemaExample returns a pretty-printed JSON skeleton of a schema — empty
// strings, zeros, false, one-element arrays, nested objects — so the user can
// see the expected input shape and fill it in (R6.5.4). It returns "" when the
// schema is unconstrained (no usable type).
func SchemaExample(raw json.RawMessage) string {
	compact := skeleton(raw)
	if compact == "" || compact == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(compact), "", "  "); err != nil {
		return compact
	}
	return buf.String()
}

// skeleton builds a compact JSON skeleton string for a schema, preserving the
// declaration order of object properties.
func skeleton(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "null"
	}
	var s struct {
		Type       schemaType        `json:"type"`
		Properties json.RawMessage   `json:"properties"`
		Items      json.RawMessage   `json:"items"`
		Enum       []json.RawMessage `json:"enum"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "null"
	}
	if len(s.Enum) > 0 {
		return string(s.Enum[0])
	}
	switch s.Type.Name {
	case "string":
		return `""`
	case "integer", "number":
		return "0"
	case "boolean":
		return "false"
	case "array":
		if len(bytes.TrimSpace(s.Items)) > 0 {
			return "[" + skeleton(s.Items) + "]"
		}
		return "[]"
	case "object":
		keys := orderedKeys(s.Properties)
		if len(keys) == 0 {
			return "{}"
		}
		var props map[string]json.RawMessage
		_ = json.Unmarshal(s.Properties, &props)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			kb, _ := json.Marshal(k)
			parts = append(parts, string(kb)+":"+skeleton(props[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return "null"
	}
}

// enumStrings renders enum values as display strings. A null member is left
// out: a nullable field carries null as its own explicit choice.
func enumStrings(raw []json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if string(bytes.TrimSpace(r)) == "null" {
			continue
		}
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			out = append(out, s)
			continue
		}
		out = append(out, string(r)) // non-string enum value, show raw token
	}
	return out
}

// TaskFormModel is a task's completion form: what the required caller must
// still supply (Editable), or IsApproval when nothing is left to fill and the
// completer only confirms. What the creator bound (partial_args) renders as
// the view above it (_call_form).
type TaskFormModel struct {
	Editable   FormModel
	IsApproval bool
}

// BuildTaskForm derives a completion form from the task's allowed_input and
// partial_args: the form of the schema without the sealed properties, so its
// fields and its editor's example agree. Nil or invalid partial_args is
// treated as empty (all fields editable).
func BuildTaskForm(inputSchema, partialArgs json.RawMessage) TaskFormModel {
	bound := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(partialArgs)) > 0 {
		_ = json.Unmarshal(partialArgs, &bound)
	}
	if len(bound) == 0 {
		// An empty-object schema with nothing pre-bound is still an approval: the
		// kernel is asking for a confirmation, not for input.
		full := BuildForm(inputSchema)
		return TaskFormModel{Editable: full, IsApproval: full.NoFields}
	}
	form := BuildForm(unsealed(inputSchema, bound))
	// Nothing left to fill is an approval: a structured schema with every field
	// sealed, and a free-form schema whose pre-bound args already carry the
	// payload (sys/sink with `message` sealed) — the acknowledge button alone,
	// not a dead raw box.
	return TaskFormModel{Editable: form, IsApproval: len(form.Fields) == 0}
}

// unsealed is the schema without the properties named in sealed, the rest in
// the order served; a schema naming no properties is returned as it is.
func unsealed(schema json.RawMessage, sealed map[string]json.RawMessage) json.RawMessage {
	var doc map[string]json.RawMessage
	if json.Unmarshal(schema, &doc) != nil || doc["properties"] == nil {
		return schema
	}
	var props map[string]json.RawMessage
	_ = json.Unmarshal(doc["properties"], &props)
	var b bytes.Buffer
	b.WriteByte('{')
	for _, k := range orderedKeys(doc["properties"]) {
		if _, ok := sealed[k]; ok {
			continue
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(props[k])
	}
	b.WriteByte('}')
	doc["properties"] = b.Bytes()
	var required []string
	_ = json.Unmarshal(doc["required"], &required)
	required = slices.DeleteFunc(required, func(k string) bool { _, ok := sealed[k]; return ok })
	if len(required) == 0 {
		delete(doc, "required")
	} else {
		doc["required"], _ = json.Marshal(required)
	}
	out, _ := json.Marshal(doc)
	return out
}
