package server

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Field is one input field derived from a JSON Schema property.
type Field struct {
	Name        string
	Type        string // string | integer | number | boolean | object | array
	Required    bool
	Description string
	Enum        []string
}

// FormModel is the server-side model the run form is rendered from (R6.5.4). The
// server decides field types, requiredness, help text, and whether a usable
// object schema exists; the browser only hydrates inputs. The kernel remains the
// validation authority (R2.5).
type FormModel struct {
	Fields         []Field
	UseRawFallback bool // no usable object schema -> offer only the raw-JSON editor
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
	required := make(map[string]bool, len(doc.Required))
	for _, r := range doc.Required {
		required[r] = true
	}
	fm := FormModel{Fields: make([]Field, 0, len(keys))}
	for _, k := range keys {
		p := props[k]
		fm.Fields = append(fm.Fields, Field{
			Name:        k,
			Type:        p.Type,
			Required:    required[k],
			Description: p.Description,
			Enum:        enumStrings(p.Enum),
		})
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

// FormJSON returns the FormModel encoded for the client-side hydrator
// (schema-form.js reads it from a <script type="application/json"> blob).
func (fm FormModel) FormJSON() (string, error) {
	type wireField struct {
		Name        string   `json:"name"`
		Type        string   `json:"type"`
		Required    bool     `json:"required"`
		Description string   `json:"description"`
		Enum        []string `json:"enum,omitempty"`
	}
	wf := make([]wireField, len(fm.Fields))
	for i, f := range fm.Fields {
		wf[i] = wireField{f.Name, f.Type, f.Required, f.Description, f.Enum}
	}
	b, err := json.Marshal(struct {
		Fields         []wireField `json:"fields"`
		UseRawFallback bool        `json:"use_raw_fallback"`
	}{wf, fm.UseRawFallback})
	if err != nil {
		return "", fmt.Errorf("encode form model: %w", err)
	}
	return string(b), nil
}
