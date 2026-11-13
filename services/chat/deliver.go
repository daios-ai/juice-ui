package chat

import (
	"context"
	"encoding/json"
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
// post last. The task is created before the delivery is recorded, and the
// delivery is recorded as soon as its task is, without waiting on the other
// recipients, so a recipient answering at once finds it; a record that fails
// only repeats a range next time. A recipient the kernel
// refuses, or does not take in time, is skipped and reached by a later post. It
// returns the recipients it could not deliver to, so the poster learns of them.
func (s *Service) deliver(ctx context.Context, self, callback, capability, roomID string, upto, depth int64, to []string) []string {
	missed := []string{}
	var members []string
	var ids []int64
	var tasks []client.CreateTaskInput
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
		members, ids = append(members, m), append(ids, id)
		tasks = append(tasks, client.CreateTaskInput{Action: self + "/" + replyName, RequiredCaller: m, PartialArgs: partial})
	}
	created := s.createTasks(ctx, callback, capability, tasks, func(i int) {
		if err := s.Store.Delivered(members[i], roomID, upto, ids[i], depth); err != nil {
			s.log().Warn("chat: record delivery", "member", members[i], "err", err)
		}
	})
	for i, ok := range created {
		if !ok {
			missed = append(missed, members[i])
		}
	}
	return missed
}

// taskTimeout bounds the creation of one task during a dispatch. The kernel
// dials the recipient's kernel to create it, and one that does not answer (a
// dial to an offline peer took 8 s, measured) must not hold the dispatch past
// the kernel's own deadline (10 s, measured), which would fail the whole post or
// room command. A task not created in time is created again later.
var taskTimeout = 3 * time.Second

// createTasks creates tasks through the capability of a dispatch, all at once,
// each within taskTimeout, and reports which were created. recorded runs for
// each task as soon as it is created, before the others finish: its recipient
// may complete it at once, and the completion must find what recorded wrote.
func (s *Service) createTasks(ctx context.Context, callback, capability string, tasks []client.CreateTaskInput, recorded func(i int)) []bool {
	created := make([]bool, len(tasks))
	var wg sync.WaitGroup
	for i, in := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tctx, cancel := context.WithTimeout(ctx, taskTimeout)
			defer cancel()
			if _, err := s.Kernel.CallbackCreateTask(tctx, callback, capability, in); err != nil {
				s.log().Warn("chat: create task", "action", in.Action, "member", in.RequiredCaller, "err", err)
				return
			}
			created[i] = true
			recorded(i)
		}()
	}
	wg.Wait()
	return created
}

// invite creates, during a dispatch, one invitation task per member named: the
// member completes it, or runs room/join, to join, and chat's accept action is
// the task's onward. Each carries the room id as thread_id, the member, the
// room's name and its creator, who invited. A member the kernel takes is
// invited; one it refuses or does not take in time, its kernel offline or
// unknown, stays added and is tried again at the room's next post. Those are
// returned, so the poster learns of them.
func (s *Service) invite(ctx context.Context, self, callback, capability, roomID string, members []string) []string {
	missed := []string{}
	if len(members) == 0 {
		return missed
	}
	room, err := s.Store.room(roomID)
	if err != nil {
		return append(missed, members...)
	}
	tasks := make([]client.CreateTaskInput, len(members))
	for i, m := range members {
		partial, _ := json.Marshal(map[string]any{"thread_id": roomID, "member": m, "name": room.Name, "from": room.Creator})
		tasks[i] = client.CreateTaskInput{Action: self + "/" + acceptName, RequiredCaller: m, PartialArgs: partial}
	}
	created := s.createTasks(ctx, callback, capability, tasks, func(i int) {
		if err := s.Store.Invited(roomID, members[i]); err != nil {
			s.log().Warn("chat: record invitation", "room", roomID, "member", members[i], "err", err)
		}
	})
	for i, ok := range created {
		if !ok {
			missed = append(missed, members[i])
		}
	}
	return missed
}
