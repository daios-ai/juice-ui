# Juice: Conversational search and action execution

## 1. Objective

Juice's primary interface is a conversation between people and agents. The user must be able to discover and execute affordances without leaving that conversation.

There are two distinct needs:

- **Quick access:** Find and execute an action the local kernel already knows about.
- **Marketplace discovery:** Search the wider affordance economy for capabilities that may not be in the local catalog.

Both should begin in the existing `/` launcher and use the existing contextual panel. Neither requires a separate interface mode.

The overall interaction is:

**Conversation → `/` → Action selection or marketplace search → Action form → Execute → Result in composer**

The user decides when to post the result to the conversation.

## 2. Preserve instant action selection

Version 0.39 already loads the known action catalog and filters it locally as the user types.

**Keep this functionality.**

For example, typing `/translate` displays matching known actions. Selecting one opens its action form directly in the contextual panel.

This operation does not invoke `sys/lookup`.

**Justification:** When the desired action is already known, requiring a network search and a separate results page adds latency, potentially incurs a charge, and provides no additional information needed to select it.

The launcher should therefore continue displaying actual known actions, not merely suggested search queries.

Navigation commands remain directly selectable through the same launcher.

## 3. Submitting a query performs marketplace discovery

If the user types `/translate` but does not select a suggested action, pressing Enter should submit `translate` to `sys/lookup`.

This changes one behavior in v0.39:

**Enter submits a search unless the user has explicitly selected a suggestion.**

The first suggested action must not be selected automatically.

The distinction is:

| Interaction | Outcome |
|---|---|
| Select a known action | Open its form immediately |
| Select a navigation command | Navigate to its destination |
| Submit the query | Search the affordance economy |

This resembles established retail-search interactions: suggestions provide shortcuts to known items, while submitting the query produces a broader set of results.

**Justification:** Local catalog filtering and marketplace discovery have different coverage and potentially different costs. The user should be able to choose either without learning two entry points.

### Search costs

Typing and filtering the known catalog remain free.

A submitted query may invoke a priced `sys/lookup` action. Its quoted price must therefore be visible before submission.

For example:

`Search the economy (0.01 USDC)`

Submitting the search authorizes that quoted operation directly. No additional confirmation dialog is needed.

If lookup is free, submission proceeds normally.

The kernel's existing quote-pinning and authorization requirements remain in force.

## 4. Search results stay within the conversation

A marketplace search initiated from chat displays its results in the **contextual panel**, beside the conversation.

It must not navigate to the Discover page.

The panel reuses Discover's existing search-result components, including action descriptions, prices, reputation information, sorting, and pagination.

The Discover page remains available as a standalone marketplace destination.

**Justification:** There is no reason to maintain two implementations of marketplace search. The only difference is where results appear.

| Search origin | Results destination |
|---|---|
| Chat composer | Contextual panel |
| Discover page | Discover page |

Both use the same search mechanism.

## 5. The contextual panel supports browsing

After searching, the user can inspect multiple actions before choosing one.

The panel has two relevant states:

**Search results**

```text
┌─ Contextual panel ────────────┐
│ Search: translate         ×  │
│                              │
│ Translate text               │
│ Translate document           │
│ Translation API              │
│                              │
│ [Sorting and pagination]     │
└──────────────────────────────┘
```

**Selected action**

```text
┌─ Contextual panel ────────────┐
│ ← Results                 ×  │
│                              │
│ Translate document           │
│ provider@kernel/translate    │
│                              │
│ Description and evidence     │
│                              │
│ [Existing action form]       │
│                              │
│ [Fill from chat]             │
│ [Run (N)]                    │
└──────────────────────────────┘
```

The behavior is:

- Selecting a search result replaces the results with that action's details and form.
- **Back** restores the previous results, including query, sorting, pagination, and scroll position.
- Selecting another result opens another action in the same panel.
- **Close** dismisses the panel without changing the conversation or its draft.
- An action selected directly from the launcher's known-action suggestions opens its form without requiring a preceding results view.

**Justification:** Marketplace discovery necessarily involves comparing alternatives. Back navigation allows comparison without repeating or repurchasing the search.

This requires retaining the panel's search state, not implementing a general-purpose browser or navigation history.

## 6. Executing an action returns its result to the composer

When an action is executed from a conversation, its successful result should **populate that conversation's message composer as an editable draft**.

The panel may close after successful execution.

The user can inspect, edit, annotate, send, or discard the draft.

- A string result is inserted as text.
- A structured result is inserted as serialized JSON.
- Any existing message draft must be preserved rather than silently overwritten. The result can be appended with a clear separation.
- The `/` search query itself is not inserted into the message draft.

The action's receipt remains accessible through the existing transaction interface.

A failed or pending execution continues to use the existing outcome display; it must not be represented as a successful result.

**Justification:** The originating conversation already establishes where the result is needed. Asking the user to select **Use result → In chat** repeats a decision that has already been made.

However, executing an action and communicating its result are not the same operation.

Populating the composer preserves that distinction without introducing another interface.

### Sending the result

When the user presses Send, the message goes through the existing `chat@kernel/msg/post` action.

Normal chat delivery rules apply, including deliveries to other room members.

Until then, the result remains a private draft.

**No new chat-entry type or delivery mechanism is required.**

## 7. Remove redundant result controls

Remove the proposed **Use result** menu, including:

- **In chat**
- **In another action**

The first operation is replaced by automatic insertion into the composer.

The second is unnecessary because composition between affordances already takes place through chat.

For example:

1. An OCR action extracts text.
2. Its result is sent to the conversation.
3. The user asks an agent to translate it.
4. The agent selects a translation action and constructs its arguments from the conversation.

The OCR and translation schemas do not need to be compatible. The agent performs the semantic mapping.

When the human prefers direct execution, they can select `/translate` and optionally use **Fill from chat** to prepare its arguments.

**Justification:** Juice already provides a mechanism for interpreting and composing actions: agents working with conversation context. A separate result-routing or schema-mapping system would duplicate that capability.

## 8. Preserve the existing execution model

The contextual panel continues to use the same action form as the ordinary action page.

**Fill from chat** remains optional. If the model call is priced, its button names the cost and directly authorizes that call. It does not automatically execute the selected action.

The user reviews the arguments and runs the action through its existing priced control.

No additional confirmation dialog is needed for ordinary quoted micropayments. The existing kernel authorization and quote-pinning requirements remain unchanged.

Actions launched outside a conversation continue to display their results on the ordinary action page. They do not need an automatic chat destination.

## 9. Scope of the change

Implement the following changes to v0.39:

| Change | Existing functionality reused |
|---|---|
| Enter submits a query unless a suggestion is explicitly selected | `/` launcher |
| Submitted queries invoke marketplace discovery | `sys/lookup` |
| Chat search results appear in the contextual panel | Discover results components |
| Results support Back, comparison, and pagination | Existing action panel and Discover state |
| Successful execution populates the composer | Existing message field |
| Sending uses ordinary chat delivery | `chat@kernel/msg/post` |
| Remove `Use result` | No replacement component needed |

Preserve the existing avatar menu, conversation sidebar, schema-driven forms, agent delegation, action execution, receipts, and security rules.

Do not introduce a second search launcher, a workflow builder, automatic schema mapping, structured result messages, or a new chat delivery protocol.

## 10. Acceptance tests

The implementation should satisfy these scenarios:

**Known action:** Type `/translate`, select a known action, and open its form without calling `sys/lookup`.

**Unknown action:** Type `/translate`, press Enter without selecting a suggestion, and see marketplace search results in the contextual panel.

**Browse:** Open an action from the results, go back, and recover the same query, sorting, page, and scroll position.

**Execute:** Run an action from the panel and see its successful result inserted into the originating conversation's composer.

**Preserve drafts:** If the composer already contains text, executing an action does not destroy it.

**Post:** Edit the result and press Send. The resulting message uses the existing chat action and delivery rules.

**Cancel:** Close the panel or discard the draft without posting a message.

**Economic authorization:** Typing does not spend money; priced search, model preparation, and execution occur only through their respective explicitly priced controls.

### Final interaction model

**Known action**

`/` → Select known action → Review form → Run → Edit result in composer → Send

**Marketplace discovery**

`/` → Submit search → Browse results → Select action → Review form → Run → Edit result in composer → Send

Both workflows remain within the conversation.

The marketplace supplies capabilities. The contextual panel lets the user choose and execute them. **The conversation remains the place where results are communicated and further work is composed.**
