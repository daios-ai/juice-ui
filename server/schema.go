package server

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Field is one input field derived from a JSON Schema property.
type Field struct {
	Name        string
	Type        string // string | integer | number | boolean | object | array
	Required    bool
	Description string
	Enum        []string
	Example     string // for object/array fields: a pretty-printed JSON skeleton
}

// FormModel is the server-side model the run form is rendered from (R6.5.4). The
// server decides field types, requiredness, help text, and whether a usable
// object schema exists; the browser only hydrates inputs. The kernel remains the
// validation authority (R2.5).
type FormModel struct {
	Fields         []Field
	UseRawFallback bool   // no usable object schema -> offer only the raw-JSON editor
	Example        string // a pretty-printed JSON skeleton of the whole input, for the raw editor
}

// schemaDoc is the subset of JSON Schema the form renderer understands.
type schemaDoc struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Required   []string        `json:"required"`
}

type propDoc struct {
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Enum        []json.RawMessage `json:"enum"`
}

// BuildForm parses an input_schema into a FormModel. Any schema that is not a
// non-empty object schema yields UseRawFallback so the user can still submit raw
// JSON args.
func BuildForm(inputSchema json.RawMessage) FormModel {
	if len(bytes.TrimSpace(inputSchema)) == 0 {
		return FormModel{UseRawFallback: true}
	}
	var doc schemaDoc
	if err := json.Unmarshal(inputSchema, &doc); err != nil {
		return FormModel{UseRawFallback: true}
	}
	if doc.Type != "object" || len(bytes.TrimSpace(doc.Properties)) == 0 {
		return FormModel{UseRawFallback: true}
	}
	keys := orderedKeys(doc.Properties)
	if len(keys) == 0 {
		return FormModel{UseRawFallback: true}
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
			Type:        p.Type,
			Required:    required[k],
			Description: p.Description,
			Enum:        enumStrings(p.Enum),
		}
		if p.Type == "object" || p.Type == "array" {
			f.Example = SchemaExample(rawProps[k])
		}
		fm.Fields = append(fm.Fields, f)
	}
	return fm
}

// orderedKeys returns the property keys in their schema declaration order by
// streaming the JSON object, so the form preserves the author's field order
// rather than Go's randomized map iteration.
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
		Type       string            `json:"type"`
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
	switch s.Type {
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

// enumStrings renders enum values as display strings.
func enumStrings(raw []json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			out = append(out, s)
			continue
		}
		out = append(out, string(r)) // non-string enum value, show raw token
	}
	return out
}

// LockedField is a pre-filled field from the step's partial_args, shown
// read-only so the completer understands the pre-set context.
type LockedField struct {
	Name  string
	Value string // compact JSON representation of the value
}

// StepFormModel splits a step's input into what the creator already bound
// (Locked) and what the required caller must still supply (Editable).
// IsApproval is true when there are no editable fields and the user only
// needs to confirm.
type StepFormModel struct {
	Editable   FormModel
	Locked     []LockedField
	IsApproval bool
}

// BuildStepForm derives a completion form from the step's input_schema and
// partial_args. Fields present in partial_args are moved to Locked; fields
// absent remain in Editable. Nil or invalid partial_args is treated as empty
// (all fields editable).
func BuildStepForm(inputSchema, partialArgs json.RawMessage) StepFormModel {
	full := BuildForm(inputSchema)

	// Parse partial_args into a map. Any failure → treat as empty.
	bound := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(partialArgs)) > 0 {
		_ = json.Unmarshal(partialArgs, &bound)
	}

	if len(bound) == 0 {
		return StepFormModel{Editable: full}
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
		Example:        full.Example,
	}
	// Approval: structured schema and all fields are locked.
	isApproval := !full.UseRawFallback && len(editFields) == 0
	return StepFormModel{Editable: editFM, Locked: locked, IsApproval: isApproval}
}

