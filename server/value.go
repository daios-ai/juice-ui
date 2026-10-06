package server

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// Value is a JSON value prepared for the read-only `_value` partial: the one
// rendering of args, results and sealed partial_args (requirements.md §7). It
// is structure by JSON type alone — an object is a key/value list, an array an
// ordered list, a scalar its literal text — so what the kernel transported is
// shown and nothing in it is interpreted (§8.1): no value is linked, formatted
// or turned into an icon.
type Value struct {
	Kind    string  // object | array | string | number | bool | null | raw
	Text    string  // the scalar's text; for raw, the undecodable input
	Long    bool    // a string of more than longText characters, folded by default
	Entries []Entry // object members, in the order served
	Items   []Value // array elements
}

// Entry is one object member.
type Entry struct {
	Key   string
	Value Value
}

// longText is the length past which a string renders folded, so a page body or
// a compiled artifact does not push the rest of the result off screen.
const longText = 800

// valueOf decodes raw into a Value. Object keys keep the order the kernel
// served (a streaming decode, as orderedKeys); numbers keep their literal text.
// Empty or invalid input is `raw`, shown as it came.
func valueOf(raw json.RawMessage) Value {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || !json.Valid(raw) {
		return Value{Kind: "raw", Text: string(raw)}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, _ := decodeValue(dec)
	return v
}

// decodeValue reads one value from the token stream.
func decodeValue(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			v := Value{Kind: "object", Entries: []Entry{}}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return v, err
				}
				m, err := decodeValue(dec)
				if err != nil {
					return v, err
				}
				v.Entries = append(v.Entries, Entry{Key: k.(string), Value: m})
			}
			_, err = dec.Token() // '}'
			return v, err
		}
		v := Value{Kind: "array", Items: []Value{}}
		for dec.More() {
			m, err := decodeValue(dec)
			if err != nil {
				return v, err
			}
			v.Items = append(v.Items, m)
		}
		_, err = dec.Token() // ']'
		return v, err
	case string:
		return Value{Kind: "string", Text: t, Long: utf8.RuneCountInString(t) > longText}, nil
	case json.Number:
		return Value{Kind: "number", Text: t.String()}, nil
	case bool:
		if t {
			return Value{Kind: "bool", Text: "true"}, nil
		}
		return Value{Kind: "bool", Text: "false"}, nil
	default: // nil
		return Value{Kind: "null", Text: "null"}, nil
	}
}

// ValueBlock drives `_value_block`: the value, its raw JSON behind the
// disclosure, and the download route when the result has a transaction.
type ValueBlock struct {
	Value    Value
	Raw      json.RawMessage
	Download string
}

func valueBlock(raw json.RawMessage, download string) ValueBlock {
	return ValueBlock{Value: valueOf(raw), Raw: raw, Download: download}
}

// resultDownload is the download route of a transaction's result, or "" when
// the outcome names no transaction here (a task completed on another kernel).
func resultDownload(txID string) string {
	if txID == "" {
		return ""
	}
	return "/transactions/" + txID + "/result.json"
}
