package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/client"
)

// handoffCap bounds a chain of hand-offs: a post whose depth exceeds it is
// kept but delivered to nobody, so two members naming each other stop.
const handoffCap = 3

// carried bounds what one delivery carries: the newest entries since the
// member's previous delivery.
const carried = 50

// addressed is the member the message addresses (ID): a string whose first
// word, trailing punctuation removed, is a member's address, compared without
// case. "" when none: a document, or text starting with anything else.
func addressed(message json.RawMessage, members []client.Party) string {
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
		if strings.EqualFold(first, m.Address) {
			return ID(m)
		}
	}
	return ""
}

// recipients applies the routing rule: the member the message addresses, or
// every other member when it addresses nobody; never the author.
func recipients(author string, message json.RawMessage, members []client.Party) []client.Party {
	to := addressed(message, members)
	out := []client.Party{}
	for _, m := range members {
		if id := ID(m); id != author && (to == "" || to == id) {
			out = append(out, m)
		}
	}
	return out
}

// carriedEntry is one entry as a delivery carries it: its author by address,
// which a member may address.
type carriedEntry struct {
	From    string          `json:"from"`
	Message json.RawMessage `json:"message"`
}

// trace is the kernel's call of an action, as chat composes on it: the
// provider the action is of, chat@kernel, the callback base URL and the
// capability of the call's trace.
type trace struct{ self, callback, capability string }

func traceOf(r *http.Request, self string) trace {
	return trace{self, r.Header.Get(client.CallbackHeader), r.Header.Get(client.CapabilityHeader)}
}

// resolve is the user an address or a principal names, as chat's kernel names
// them: sys/resolve, composed on the dispatch's trace, within taskTimeout.
func (s *Service) resolve(ctx context.Context, d trace, address string) (client.Party, error) {
	ctx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()
	var p client.Party
	err := s.Kernel.CallbackCall(ctx, d.callback, d.capability, client.Resolve, map[string]string{"address": address}, &p)
	if err == nil && !IsPrincipal(p.Principal) {
		err = fmt.Errorf("%s resolved to no user", address)
	}
	return p, err
}

// deliver creates, during the post's own dispatch, one task per recipient, to
// its principal: the recipient completes it to answer, and chat's reply action
// is its onward. Each carries the room id as thread_id, the member, the
// delivery id and what the room said since that member's previous delivery,
// this post last. The task is created before the delivery is recorded, and the
// delivery is recorded as soon as its task is, without waiting on the other
// recipients, so a recipient answering at once finds it; a record that fails
// only repeats a range next time. A recipient the kernel refuses, or does not
// take in time, is skipped and reached by a later post; one chat holds by
// address alone is sent nothing, since a task to an address reaches whoever
// holds it now. It returns the recipients it could not deliver to, so the
// poster learns of them.
func (s *Service) deliver(ctx context.Context, d trace, roomID string, upto, depth int64, to []client.Party) []client.Party {
	missed := []client.Party{}
	var members []client.Party
	var ids []int64
	var tasks []client.CreateTaskInput
	for _, m := range to {
		if m.Principal == "" {
			missed = append(missed, m)
			continue
		}
		entries, err := s.Store.Range(m.Principal, roomID, upto, carried)
		var authors []client.Party
		if err == nil {
			authors, err = s.Store.Parties(authorsOf(entries))
		}
		var id int64
		if err == nil {
			id, err = s.Store.NextDelivery()
		}
		if err != nil {
			s.log().Warn("chat: delivery", "member", m.Principal, "err", err)
			missed = append(missed, m)
			continue
		}
		carry := make([]carriedEntry, len(entries))
		for i, e := range entries {
			carry[i] = carriedEntry{From: authors[i].Address, Message: e.Body}
		}
		partial, _ := json.Marshal(map[string]any{"thread_id": roomID, "member": m.Principal, "delivery": id, "message": carry})
		members, ids = append(members, m), append(ids, id)
		tasks = append(tasks, client.CreateTaskInput{Action: d.self + "/" + replyName, RequiredCaller: m.Principal, PartialArgs: partial})
	}
	created := s.createTasks(ctx, d, tasks, func(i int) {
		if err := s.Store.Delivered(members[i].Principal, roomID, upto, ids[i], depth); err != nil {
			s.log().Warn("chat: record delivery", "member", members[i].Principal, "err", err)
		}
	})
	for i, ok := range created {
		if !ok {
			missed = append(missed, members[i])
		}
	}
	return missed
}

// authorsOf is the authors of entries, in order.
func authorsOf(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Author
	}
	return out
}

// taskTimeout bounds the creation of one task, or one resolve, during a
// dispatch. The kernel dials the recipient's kernel for it, and one that does
// not answer (a dial to an offline peer took 3 to 8 s, measured) must not hold
// the dispatch past the kernel's own deadline (10 s, measured), which would fail
// the whole post or room command. What is not done in time is done later.
var taskTimeout = 3 * time.Second

// createTasks creates tasks through the capability of a dispatch, all at once,
// each within taskTimeout, and reports which were created. Each task's reply
// names its addressee as the kernel now does, which chat keeps. recorded runs
// for each task as soon as it is created, before the others finish: its
// recipient may complete it at once, and the completion must find what
// recorded wrote.
func (s *Service) createTasks(ctx context.Context, d trace, tasks []client.CreateTaskInput, recorded func(i int)) []bool {
	created := make([]bool, len(tasks))
	var wg sync.WaitGroup
	for i, in := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tctx, cancel := context.WithTimeout(ctx, taskTimeout)
			defer cancel()
			t, err := s.Kernel.CallbackCreateTask(tctx, d.callback, d.capability, in)
			if err != nil {
				s.log().Warn("chat: create task", "action", in.Action, "member", in.RequiredCaller, "err", err)
				return
			}
			if IsPrincipal(t.RequiredCaller.Principal) {
				if err := s.Store.Named(t.RequiredCaller); err != nil {
					s.log().Warn("chat: record name", "member", in.RequiredCaller, "err", err)
				}
			}
			created[i] = true
			recorded(i)
		}()
	}
	wg.Wait()
	return created
}

// invite invites the seats named, during a dispatch. A seat added under an
// address is first resolved by chat's kernel and becomes the principal it
// names (Seat). Each invitation is a task to that principal: the member
// completes it, or runs room/join, to join, and chat's accept action is the
// task's onward. Each carries the room id as thread_id, the member, the room's
// name and its creator's address, who invited. A seat whose address does not
// resolve, or whose task the kernel does not take in time, its kernel offline
// or unknown, stays added and is tried again at the room's next post. Those
// are returned, so the poster learns of them.
func (s *Service) invite(ctx context.Context, d trace, roomID string, seats []string) []client.Party {
	missed := []client.Party{}
	if len(seats) == 0 {
		return missed
	}
	room, err := s.Store.room(roomID)
	var from []client.Party
	if err == nil {
		from, err = s.Store.Parties([]string{room.Creator})
	}
	if err != nil {
		p, _ := s.Store.Parties(seats)
		return append(missed, p...)
	}
	ids, owed, failed := slices.Clone(seats), make([]bool, len(seats)), make([]bool, len(seats))
	var wg sync.WaitGroup
	for i, seat := range seats {
		if owed[i] = IsPrincipal(seat); owed[i] {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.resolve(ctx, d, seat)
			if err == nil {
				owed[i], err = s.Store.Seat(roomID, seat, p)
			}
			if failed[i] = err != nil; failed[i] {
				s.log().Warn("chat: resolve", "address", seat, "err", err)
			}
			ids[i] = p.Principal
		}()
	}
	wg.Wait()
	var tasks []client.CreateTaskInput
	var sent []string
	for i, id := range ids {
		if failed[i] {
			missed = append(missed, client.Party{Address: seats[i]})
		}
		if !owed[i] || slices.Contains(sent, id) { // two addresses may name one member
			continue
		}
		partial, _ := json.Marshal(map[string]any{"thread_id": roomID, "member": id, "name": room.Name, "from": from[0].Address})
		tasks = append(tasks, client.CreateTaskInput{Action: d.self + "/" + acceptName, RequiredCaller: id, PartialArgs: partial})
		sent = append(sent, id)
	}
	created := s.createTasks(ctx, d, tasks, func(i int) {
		if err := s.Store.Invited(roomID, sent[i]); err != nil {
			s.log().Warn("chat: record invitation", "room", roomID, "member", sent[i], "err", err)
		}
	})
	for i, ok := range created {
		if !ok {
			p, _ := s.Store.Parties(sent[i : i+1])
			missed = append(missed, p...)
		}
	}
	return missed
}
