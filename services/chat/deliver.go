package chat

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/daios-ai/juice-ui/client"
)

// handoffCap bounds a chain of hand-offs: a post whose depth exceeds it is
// kept but delivered to nobody, so two members naming each other stop.
const handoffCap = 3

// carried bounds what one delivery carries: the newest entries since the
// member's previous delivery.
const carried = 50

// addressed is the member the message addresses: a string whose first word,
// trailing punctuation removed, is a member's address, compared without case.
// "" when none: a document, or text starting with anything else.
func addressed(message json.RawMessage, members []string) string {
	var text string
	if json.Unmarshal(message, &text) != nil {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	first := strings.TrimRight(fields[0], ",:;.!?")
	for _, m := range members {
		if strings.EqualFold(first, m) {
			return m
		}
	}
	return ""
}

// recipients applies the routing rule: the member the message addresses, or
// every other member when it addresses nobody; never the author.
func recipients(author string, message json.RawMessage, members []string) []string {
	to := members
	if m := addressed(message, members); m != "" {
		to = []string{m}
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
// refuses is skipped. It returns the recipients it could not deliver to, so the
// poster learns of them: an address on a kernel this one cannot name is refused.
func (s *Service) deliver(ctx context.Context, self, callback, capability, roomID string, upto, depth int64, to []string) []string {
	missed := []string{}
	for _, m := range to {
		entries, err := s.Store.Range(m, roomID, upto, carried)
		if err != nil {
			s.log().Warn("chat: delivery range", "member", m, "err", err)
			missed = append(missed, m)
			continue
		}
		carry := make([]carriedEntry, 0, len(entries))
		for _, e := range entries {
			carry = append(carry, carriedEntry{From: e.Author, Message: e.Body})
		}
		id, err := s.Store.NextDelivery()
		if err != nil {
			s.log().Warn("chat: delivery id", "err", err)
			missed = append(missed, m)
			continue
		}
		partial, _ := json.Marshal(map[string]any{"thread_id": roomID, "member": m, "delivery": id, "message": carry})
		if _, err := s.Kernel.CallbackCreateTask(ctx, callback, capability, client.CreateTaskInput{
			Action: self + "/" + replyName, RequiredCaller: m, PartialArgs: partial,
		}); err != nil {
			s.log().Warn("chat: deliver", "room", roomID, "member", m, "err", err)
			missed = append(missed, m)
			continue
		}
		if err := s.Store.Delivered(m, roomID, upto, id, depth); err != nil {
			s.log().Warn("chat: record delivery", "member", m, "err", err)
		}
	}
	return missed
}

// invite creates, during a room command's own dispatch, one invitation task per
// member named: the member completes it to join, and chat's accept action is
// its onward. Each carries the room id as thread_id, the member, the room's
// name and who invited. A member the kernel refuses is not kept in the room,
// since nobody could accept for it; those are returned, so the inviter learns
// of them.
func (s *Service) invite(ctx context.Context, self, callback, capability, roomID, from string, members []string) []string {
	missed := []string{}
	if len(members) == 0 {
		return missed
	}
	room, err := s.Store.room(roomID)
	if err != nil {
		return append(missed, members...)
	}
	for _, m := range members {
		partial, _ := json.Marshal(map[string]any{"thread_id": roomID, "member": m, "name": room.Name, "from": from})
		if _, err := s.Kernel.CallbackCreateTask(ctx, callback, capability, client.CreateTaskInput{
			Action: self + "/" + acceptName, RequiredCaller: m, PartialArgs: partial,
		}); err != nil {
			s.log().Warn("chat: invite", "room", roomID, "member", m, "err", err)
			if err := s.Store.Uninvite(roomID, m); err != nil {
				s.log().Warn("chat: withdraw invitation", "room", roomID, "member", m, "err", err)
			}
			missed = append(missed, m)
		}
	}
	return missed
}
