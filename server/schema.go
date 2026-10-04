package server

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Field is one input field derived from a JSON Schema property.
type Field struct {
	Name     string
	Type     string // string | integer | number | boolean | object | array
	Nullable bool   // the schema admits null (`type: [T, "null"]`)
	Required bool
	// Title is the property's own short label, shown beside its key.
	Title       string
	Description string
	Enum        []string
	Example     string // for object/array fields: a pretty-printed JSON skeleton
	// The bounds the browser enforces exactly as the schema means them, as
	// written; empty when the schema states none. The kernel validates the rest.
	Min, Max             string
	MinLength, MaxLength string
	// Value pre-fills the input. It is used where something already proposed an
	// argument — the model's selection (§6.2) — and is empty otherwise. A
	// pre-filled field is still an ordinary field: it is editable, it is
	// submitted like any other, and nothing about it is trusted.
	Value string
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

// schemaDoc is the part of an object schema the form renderer reads.
type schemaDoc struct {
	Type       schemaType      `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Required   []string        `json:"required"`
	// AdditionalProperties is false, absent, or a schema; a schema makes the
	// object a map, whose keys no field list can name.
	AdditionalProperties json.RawMessage `json:"additionalProperties"`
}

// propDoc is one property node.
type propDoc struct {
	Type        schemaType        `json:"type"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Enum        []json.RawMessage `json:"enum"`
	Minimum     json.RawMessage   `json:"minimum"`
	Maximum     json.RawMessage   `json:"maximum"`
	MinLength   json.RawMessage   `json:"minLength"`
	MaxLength   json.RawMessage   `json:"maxLength"`
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
	// No `properties` key at all is an UNCONSTRAINED object, and a schema under
	// `additionalProperties` is a map — any keys are valid, so the completer
	// needs the raw editor. An explicitly EMPTY `properties` on a closed object
	// is the opposite claim: this input declares no fields, so there is nothing
	// to collect and the form is its confirm button alone.
	isMap := bytes.HasPrefix(bytes.TrimSpace(doc.AdditionalProperties), []byte("{"))
	if doc.Type.Name != "object" || len(bytes.TrimSpace(doc.Properties)) == 0 || isMap {
		return FormModel{UseRawFallback: true}
	}
	keys := orderedKeys(doc.Properties)
	if len(keys) == 0 {
		return FormModel{NoFields: true}
	}
	var props map[string]propDoc
	if err := json.Unmarshal(doc.Properties, &props); err != nil {
		return FormModel{UseRawFallback: true}
	}
	// Keep the raw schema of each property so nested object/array fields can be
	// given a concrete JSON skeleton (R6.5.4: tell the user what the shape is).
	var rawProps map[string]json.RawMessage
	_ = json.Unmarshal(doc.Properties, &rawProps)
	required := make(map[string]bool, len(doc.Required))
	for _, r := range doc.Required {
		required[r] = true
	}
	fm := FormModel{Fields: make([]Field, 0, len(keys)), Example: SchemaExample(inputSchema)}
	for _, k := range keys {
		p := props[k]
		f := Field{
			Name:        k,
			Type:        p.Type.Name,
			Nullable:    p.Type.Nullable,
			Required:    required[k],
			Title:       p.Title,
			Description: p.Description,
			Enum:        enumStrings(p.Enum),
			Min:         string(p.Minimum),
			Max:         string(p.Maximum),
			MinLength:   string(p.MinLength),
			MaxLength:   string(p.MaxLength),
		}
		if f.Type == "object" || f.Type == "array" {
			f.Example = SchemaExample(rawProps[k])
		}
		fm.Fields = append(fm.Fields, f)
	}
	return fm
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

// LockedField is a pre-filled field from the task's partial_args, shown
// read-only so the completer understands the pre-set context.
type LockedField struct {
	Name  string
	Value string // compact JSON representation of the value
}

// TaskFormModel splits a task's input into what the creator already bound
// (Locked) and what the required caller must still supply (Editable).
// IsApproval is true when there are no editable fields and the user only
// needs to confirm.
type TaskFormModel struct {
	Editable   FormModel
	Locked     []LockedField
	IsApproval bool
}

// BuildTaskForm derives a completion form from the task's allowed_input and
// partial_args. Fields present in partial_args are moved to Locked; fields
// absent remain in Editable. Nil or invalid partial_args is treated as empty
// (all fields editable).
func BuildTaskForm(inputSchema, partialArgs json.RawMessage) TaskFormModel {
	full := BuildForm(inputSchema)

	// Parse partial_args into a map. Any failure → treat as empty.
	bound := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(partialArgs)) > 0 {
		_ = json.Unmarshal(partialArgs, &bound)
	}

	if len(bound) == 0 {
		// An empty-object schema with nothing pre-bound is still an approval: the
		// kernel is asking for a confirmation, not for input.
		return TaskFormModel{Editable: full, IsApproval: full.NoFields}
	}

	// Split schema fields into locked (present in partial_args) and editable.
	var locked []LockedField
	var editFields []Field
	inSchema := make(map[string]bool, len(full.Fields))
	for _, f := range full.Fields {
		inSchema[f.Name] = true
		if val, ok := bound[f.Name]; ok {
			locked = append(locked, LockedField{Name: f.Name, Value: string(val)})
		} else {
			editFields = append(editFields, f)
		}
	}
	// Include bound keys not in the schema (no type info, show raw value).
	for _, k := range orderedKeys(partialArgs) {
		if !inSchema[k] {
			locked = append(locked, LockedField{Name: k, Value: string(bound[k])})
		}
	}

	editFM := FormModel{
		Fields:         editFields,
		UseRawFallback: full.UseRawFallback,
		NoFields:       full.NoFields,
		Example:        full.Example,
	}
	// Approval: nothing meaningful is left for the completer to fill. This covers
	// a structured schema with every field locked AND a free-form schema whose
	// pre-bound args already carry the payload (e.g. sys/sink with `message`
	// locked) — in both cases we show just the acknowledge button, not a dead raw
	// box. We are inside the len(bound) > 0 branch, so args are already pre-bound.
	isApproval := len(editFields) == 0
	return TaskFormModel{Editable: editFM, Locked: locked, IsApproval: isApproval}
}
