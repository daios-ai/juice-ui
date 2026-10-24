# Juice chat: concrete UX design

## 1. One workspace, one composer, two ways to work

The interface should let the user do two things without switching applications:

- **Delegate:** Tell a person or agent what needs doing.
- **Execute:** Select an exact action, inspect its arguments, and run it.

The composer is the common entry point. Ordinary text communicates; `/` opens a searchable launcher for actions and navigation.

The guiding rule is **one interaction surface per operation**. Do not introduce alternative interfaces, classifications, or modes unless a distinct requirement makes them necessary.

The user chooses how much control to retain:

1. Execute an action directly.
2. Let an agent propose an action and its arguments.
3. Delegate the work to an agent operating its own account.

All three begin in an ordinary conversation.

This follows established patterns: Slack supports slash commands and workflows directly in its composer, while Linear allows users to delegate work to agents without leaving the team's existing workspace.

## 2. The main screen

```text
┌───────────────────┬────────────────────────────────────────────┐
│ Juice             │ Research room                  Members (3) │
│                   │                                            │
│ + New conversation│ Alice                                      │
│                   │ Find suppliers for these components.       │
│ Assistant         │                                            │
│ Research room     │ Research agent                             │
│ Design team       │ I've identified three possible suppliers.  │
│                   │                                            │
│                   │ Alice                                      │
│                   │ Check their prices and delivery times.     │
│                   │                                            │
│                   │                                            │
│                   │ ┌────────────────────────────────────────┐ │
│                   │ │ [/] Message or choose an action     ➤ │ │
│                   │ └────────────────────────────────────────┘ │
│                   │                                            │
│ [Avatar]          │                                            │
└───────────────────┴────────────────────────────────────────────┘
```

The permanent interface contains three elements:

- **Sidebar:** Conversations and New conversation.
- **Main area:** Conversation history and composer.
- **Sidebar footer:** The signed-in user's avatar.

Clicking the avatar opens the account and navigation menu:

```text
┌────────────────────────────┐
│ alice@kernel               │
│ Available: 125 credits     │
├────────────────────────────┤
│ Home                       │
│ Inbox                   3  │
│ Discover actions           │
│ Workshop                   │
│ Wallet                     │
│ Agents                     │
│ Settings                   │
│ Network                    │
│ Admin                      │
├────────────────────────────┤
│ Sign out                   │
└────────────────────────────┘
```

Workshop includes action creation and management. Network and Admin appear only when authorized.

No other application pages occupy the permanent sidebar.

The avatar may carry the existing pending-task attention badge. The Inbox item shows the corresponding count inside the menu.

All destinations remain ordinary pages, accessible through their URLs, browser navigation, the avatar menu, and the command launcher.

The avatar menu and command launcher use the same navigation registry. They do not implement competing navigation systems.

This follows **progressive disclosure**: frequently needed operations are immediate; specialized controls appear on request.

## 3. The composer has one consistent grammar

| Interaction | Behavior |
|---|---|
| Type ordinary text | Compose a message |
| Type a member's name at the beginning | Suggest room members and insert the canonical address |
| Type `/` at the beginning | Open command and action search |
| Click the `/` button | Open the same search, preserving any existing draft |
| Select a command | Navigate or open its corresponding form |
| Select an action | Open its execution interface without running it |
| Press Escape | Dismiss suggestions without losing the draft |
| Press Enter on a normal message | Send the message |

There are no separate Chat, Agent, Search, and Execute modes.

### The `/` menu

```text
/translate

┌──────────────────────────────────────────────────┐
│ Actions                                          │
│ Translate text             linguist@work/text    │
│ Translate document         agency@work/document  │
│                                                  │
│ Pages                                            │
│ Transactions                                     │
└──────────────────────────────────────────────────┘
```

The launcher should present a small set of relevant matches, not the entire catalog.

Its results contain navigation commands and callable actions.

Selecting a command navigates to its destination or opens the corresponding form. Selecting an action opens its execution interface.

**Selection alone neither posts a message nor executes an action.**

Results should initially come from the catalog available without incurring a charge. Broader semantic action discovery can be offered separately where it requires a priced lookup.

The launcher replaces the existing global search pill rather than creating a second search mechanism.

The composer preserves its draft when opening or closing another interface.

### Group messages

Preserve the existing chat delivery contract.

Juice v0.36 defines how addressed and unaddressed messages create deliveries. Changing that behavior is a separate architectural decision because it changes task creation and potentially agent expenditure.

The interface must not imply different delivery behavior.

## 4. Selecting an action opens one work surface

Suppose the conversation contains:

> Translate this supplier's description into German.

The user selects `/translate`.

**Every selected action opens the same action interface**, regardless of schema complexity.

On desktop, it opens as a panel alongside the conversation. On mobile, it occupies the available screen.

```text
┌─────────────────────────────────────────┐
│ Translate text                       ×  │
│ linguist@work/translate                 │
│                                         │
│ Price: 2 credits                        │
│ Provider evidence                  ▸    │
│                                         │
│ Using: Alice's selected message         │
│                                         │
│ Text                                    │
│ ┌─────────────────────────────────────┐ │
│ │ [Supplier description]              │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ Target language                         │
│ ┌─────────────────────────────────────┐ │
│ │ German                              │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ [Cancel]              [Run (2 credits)] │
└─────────────────────────────────────────┘
```

The panel uses Juice's existing canonical schema-driven form and JSON editor.

The panel scrolls as needed. Nested objects and arrays use their existing controls.

There is no simple/complex classification, alternative execution form, or nested dialog.

The same panel handles:

- Actions selected through `/`.
- Actions opened from links or catalog results.
- Actions proposed by an agent.
- Actions receiving a previous result as input.

The canonical action page may remain a directly addressable route, but it uses the same form component.

The conversation remains visible where screen space permits. Closing the panel returns to the conversation without sending a message.

### 4.1 Contextual argument preparation

The action form may be populated using conversation context.

Context includes the relevant conversation history and any message or result explicitly selected by the user.

The form's values have two possible sources:

- **Explicit values:** Information already supplied by the user.
- **Model-proposed arguments:** Values inferred from the relevant context and the selected action's schema.

Both appear in the same editable fields.

Unknown values remain unfilled.

Model-assisted filling is available through **Fill from chat**.

It may happen automatically only when the model call's price is already covered by explicit authorization. Otherwise, the user authorizes that call first.

No heuristic decides whether a model is necessary. There are no confidence scores or separate AI editors.

The form remains the same whether its values were entered manually or proposed by a model.

**Argument preparation never authorizes action execution.**

Execution uses the kernel's authoritative terms, pinned quote, and existing spending confirmation.

### 4.2 Agent-proposed actions

An agent may propose an action and its arguments without executing it.

Opening that proposal presents the same action form, prepopulated with the proposed arguments.

The user can inspect, modify, approve, or dismiss it.

A proposal is not an execution authorization.

A first-class interactive proposal requires a structured representation that the chat interface can validate. The existing string-message contract does not itself provide this.

Arbitrary prose must not be interpreted as an authoritative executable proposal.

## 5. Action results become available for further work

After execution, the same panel changes from form to result.

```text
┌─────────────────────────────────────────┐
│ Translate text                          │
│ Completed · Charged 2 credits           │
│                                         │
│ Result                                  │
│ ┌─────────────────────────────────────┐ │
│ │ Translated description...           │ │
│ └─────────────────────────────────────┘ │
│                                         │
│ [Use result ▾]               [Close]    │
└─────────────────────────────────────────┘
```

The result is rendered through Juice's existing JSON view.

**Use result** offers two operations:

1. **In chat:** Insert the result into the message draft.
2. **In another action:** Select an action and use the result as context for populating its input form.

No automatic action selection or schema mapping is required.

Model-assisted mapping follows the same authorization rule as Fill from chat.

Nothing is automatically disclosed to other room members. Sharing requires sending the resulting draft.

Initially, chat can transport a serialized result as text because posts are strings.

Durable references to large results require additional backing support and are not assumed to exist.

No workflow builder, artifact manager, or second result representation is introduced.

## 6. Delegation uses the same conversation

Consider another workflow.

**Alice:**
> Research these suppliers and recommend the best option.

**Research agent:**
> I can compare pricing, delivery times, and reliability. I need access to two paid data services.

No action form is necessary unless Alice chooses to execute a service directly.

The agent can perform work within its own available balance and per-job budget. It can also request funds.

For example:

**Research agent:**
> I need another 10 credits to complete the comparison.

Alice invokes `/transfer`.

The ordinary transfer action form opens with the agent's address and proposed amount, provided their preparation is authorized.

The form identifies the paying account, recipient, and amount.

Alice confirms the transfer through the existing money-authorization mechanism.

**Transferring 10 credits gives the agent funds. It does not automatically establish an enforceable 10-credit spending limit for that assignment.**

Funding an agent also does not grant it access to the user's wallet.

Juice's agent has a per-job budget, but the current contracts do not establish an equivalent task-specific spending ceiling that an arbitrary requester can impose.

No specialized funding-request interface is required initially.

A structured action proposal, where supported, uses the common action panel.

## 7. Keep information in its proper place

Minimalism requires deciding what not to display in the conversation.

| Information | Location |
|---|---|
| Messages and replies | Conversation |
| Agent hand-offs | Conversation |
| Current pending deliveries | Conversation, when observable |
| Action arguments | Action panel |
| Action result | Action panel until shared |
| Provider evidence | Action panel, expandable |
| Exact charges and receipts | Execution result and transaction history |
| Wallet balances and transfers | Wallet |
| Agent configuration | Agent management |
| Task approvals | Inbox |

Wallet, agent management, and Inbox remain dedicated pages, reached through the avatar menu or `/` launcher.

The conversation contains messages and shared work.

The action panel contains arguments, execution controls, and results.

The avatar menu contains secondary navigation.

Economic facts appear where relevant:

- **Who is executing?**
- **Who is paying?**
- **What is the quoted price?**
- **What was actually charged?**

The conversation should not become a permanent execution log, accounting dashboard, or configuration editor.

An agent can communicate progress through ordinary messages. The interface must not invent internal progress information that the kernel does not expose to the viewer.

## 8. Acceptance tests

### A. Direct execution

A user describes a task, chooses `/`, selects an action, reviews suggested arguments, confirms its price, and receives a result.

**Success:** The user can prepare arguments from conversation context without manually copying text.

### B. Delegated work

A user asks an agent to complete a task. The agent performs the work, requests additional funds if needed, and returns its answer.

**Success:** The user understands when the agent is working, what has been requested, and which transfers they personally authorized.

The user can fund the agent through an ordinary transfer without confusing its wallet with the user's wallet.

### C. Collaborative work

A user and several specialized agents share a room. The user addresses one agent, which can communicate with another specialist and return a result.

**Success:** Participants understand who is being asked to act and can follow the resulting hand-offs and replies.

Existing addressing and task-delivery semantics are preserved unless explicitly revised.

### D. Assisted execution

An agent proposes an action and its arguments. The user opens the proposal, reviews the same action form used for direct execution, and authorizes the action.

**Success:** The user can inspect and modify the proposal without manually reconstructing its arguments.

### E. Result reuse

A user executes an action and receives a result.

The user selects Use result and either prepares another action or composes a message using that result.

**Success:** No manual transcription is required.

These tests are more useful design criteria than measuring how many features fit into the composer.

## 9. Minimal implementation

The initial implementation consists of five changes.

| Priority | Change |
|---|---|
| 1 | Move secondary navigation into the sidebar avatar menu. Keep the sidebar dedicated to conversations. |
| 2 | Consolidate commands and action discovery into the composer's `/` launcher. |
| 3 | Use one action panel and one schema-driven form for all direct and agent-proposed actions. |
| 4 | Support model-assisted argument preparation from conversation context, subject to spending authorization. |
| 5 | Support explicit reuse of action results in subsequent messages or action inputs. |

Preserve the existing wallet, inbox, catalog, workshop, execution, receipt, and approval mechanisms.

Do not introduce new navigation modes, schema-complexity classifiers, execution interfaces, or orchestration systems.

Do not change the kernel's execution contract or the chat's task-delivery semantics as a side effect of this redesign.

### Resulting interaction model

The interface has **three permanent elements**:

- Conversations.
- Composer.
- Avatar menu.

It has **one contextual work surface**: the action panel.

That panel uses the same form whether arguments come from the user, conversation context, an earlier result, or an agent proposal.

The interaction cycle is:

**Conversation → Action → Result → Conversation or next action**

At any point, the user can delegate the remaining work to an agent instead.

The essential functionality is continuity: users should not have to repeat their intentions, reconstruct action inputs, or manually transport results between operations.

The two substantial additions beyond the existing UI are contextual argument preparation and result reuse. Both require implementation work, but neither requires another permanent interface.

Structured agent proposals and reusable results require validated representations. Their full interactive behavior must not be assumed from Juice's current string-based chat protocol.
