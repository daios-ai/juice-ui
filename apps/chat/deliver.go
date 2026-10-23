package chat

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/daios-ai/juice-ui/client"
)

// handoffCap bounds a chain of hand-offs: a post whose depth exceeds it is
// kept but delivered to nobody, so two members naming each other stop.
const handoffCap = 3

// carried bounds what one delivery carries: the newest entries since the
// member's previous delivery.
const carried = 50

// mention is a name written with @: a handle, or an address handle@kernel.
var mention = regexp.MustCompile(`@([A-Za-z0-9_.-]+(?:@[A-Za-z0-9_.-]+)?)`)

// named returns the members whose handle or address is written with @ in any
// string of the message, in the members' order, or nil when the message names
// nobody at all. An e-mail address names nobody, since its "@" joins a name
// rather than opening one.
func named(message json.RawMessage, members []string) []string {
	said := map[string]bool{}
	var v any
	_ = json.Unmarshal(message, &v)
	for _, t := range texts(v) {
		for _, m := range mention.FindAllStringSubmatchIndex(t, -1) {
			if m[0] > 0 && isNameByte(t[m[0]-1]) {
				continue
			}
			said[strings.ToLower(t[m[2]:m[3]])] = true
		}
	}
	if len(said) == 0 {
		return nil
	}
	out := []string{}
	for _, m := range members {
		handle, _, _ := strings.Cut(m, "@")
		if said[strings.ToLower(m)] || said[strings.ToLower(handle)] {
			out = append(out, m)
		}
	}
	return out
}

func isNameByte(b byte) bool {
	return b == '_' || b == '.' || b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// texts is every string inside a JSON value.
func texts(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, texts(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range x {
			out = append(out, texts(e)...)
		}
		return out
	}
	return nil
}

// recipients applies the routing rule: the members the message names, the
// author excluded, or every other member when it names nobody.
func recipients(author string, message json.RawMessage, members []string) []string {
	to := named(message, members)
	if to == nil {
		to = members
	}
	out := []string{}
	for _, m := range to {
		if m != author {
			out = append(out, m)
		}
	}
	return out
}

// carriedEntry is one entry as a delivery carries it.
type carriedEntry struct {
	From    string          `json:"from"`
	Message json.RawMessage `json:"message"`
}

// deliver creates, during the post's own dispatch, one task per recipient:
// the recipient completes it to answer, and chat's reply action is its
// onward. Each carries the room id as thread_id, the member, the delivery
// id and what the room said since that member's previous delivery, this
// post last. The task is created before the delivery is recorded, so a
// record that fails only repeats a range next time; a recipient the kernel
// refuses is skipped. It returns how many were created.
func (c *Chat) deliver(ctx context.Context, callback, capability, roomID string, upto, depth int64, to []string) int {
	n := 0
	for _, m := range to {
		entries, err := c.store.Range(m, roomID, upto, carried)
		if err != nil {
			c.log().Warn("chat: delivery range", "member", m, "err", err)
			continue
		}
		carry := make([]carriedEntry, 0, len(entries))
		for _, e := range entries {
			carry = append(carry, carriedEntry{From: e.Author, Message: e.Body})
		}
		id, err := c.store.NextDelivery()
		if err != nil {
			c.log().Warn("chat: delivery id", "err", err)
			continue
		}
		partial, _ := json.Marshal(map[string]any{"thread_id": roomID, "member": m, "delivery": id, "message": carry})
		if _, err := c.deps.Client.CallbackCreateTask(ctx, callback, capability, client.CreateTaskInput{
			Action: c.self() + "/" + replyName, RequiredCaller: m, PartialArgs: partial,
		}); err != nil {
			c.log().Warn("chat: deliver", "room", roomID, "member", m, "err", err)
			continue
		}
		if err := c.store.Delivered(m, roomID, upto, id, depth); err != nil {
			c.log().Warn("chat: record delivery", "member", m, "err", err)
			continue
		}
		n++
	}
	return n
}
