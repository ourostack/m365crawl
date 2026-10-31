# Directed actions contract, v0

This contract says how an agent asks a tool to act for a person in Microsoft 365, such as sending a Teams message or a mail, and what the tool answers. It is written for tool authors and for the agents that call those tools. m365crawl implements it in `internal/directedactions`, and the files in `cases/` are the shared test cases: any tool that implements this contract should give the expected answer for every case.

Version 0 covers the decision a tool makes before it acts. It does not cover how the action reaches Microsoft 365.

## The model

The person turns writes on. While writes are on and the person directs an agent to act, the agent acts: there is no second confirmation step. These safeguards stay in place:

- Only the person can turn writes on or off. An agent that asks to change the switch is refused and told to ask the person.
- The acting account is explicit for anything other people will see.
- Every request names the agent and gives a reason.
- Content is bound to the exact conversation and audience the agent saw. If the audience has changed, the request is refused.
- A request can name the version of the target it read. If the target has moved on, the request is refused.
- An outcome can be `unknown`. An unknown outcome is never resent blindly.
- An optional intent key makes a retried send return the earlier request instead of sending twice.

## Effect classes

Every action has one effect class:

- `only_you`: only the person can see the result, such as a draft in their own mailbox.
- `visible_to_others`: other people can see the result, such as a sent message or a reaction.

## Actions

| Action | Effect | Send | Required fields | Optional fields | `target.message` kind | Audience from | Version from |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `send_teams_message` | `visible_to_others` | yes | `target.conversation`, `target.recipients`, `content.text` | `expected_version` | not allowed | conversation | conversation |
| `send_mail` | `visible_to_others` | yes | `target.recipients`, `content.subject`, `content.text` | none | not allowed | none; a new mail has no earlier audience | none |
| `reply_mail` | `visible_to_others` | yes | `target.message`, `target.recipients`, `content.text` | `expected_version` | `mail_message` | message | message |
| `reply_all_mail` | `visible_to_others` | yes | `target.message`, `target.recipients`, `content.text` | `expected_version` | `mail_message` | message | message |
| `add_reaction` | `visible_to_others` | no | `target.conversation`, `target.message`, `target.recipients`, `content.reaction` | `expected_version` | `chat_message` in that conversation | conversation | message |
| `remove_reaction` | `visible_to_others` | no | `target.conversation`, `target.message`, `target.recipients`, `content.reaction` | `expected_version` | `chat_message` in that conversation | conversation | message |
| `save_mail_draft` | `only_you` | no | `content.text` | `target.message` (a draft to update), `target.recipients`, `content.subject`, `expected_version` | `mail_draft` | none | message |
| `discard_mail_draft` | `only_you` | no | `target.message` | `expected_version` | `mail_draft` | none | message |
| `turn_writes_on` | the person's switch | no | none; always refused | none | not allowed | none | none |
| `turn_writes_off` | the person's switch | no | none; always refused | none | not allowed | none | none |

A mail message's audience is who the reply reaches, so it depends on the action: for `reply_mail` the message's reply-to addresses, else its sender; for `reply_all_mail` also its other To and Cc recipients, without the person's own addresses unless the person sent it. A reaction is seen by everyone in the chat now, not only by the people the message first reached, so its audience comes from the conversation while its version comes from the message.

`identity` is allowed on every action, and `intent_key` only on sends. Any other target or content field, or `expected_version` where the table does not list it, is refused. An unused field must never make a request look different from an earlier one.

`envelope.schema.json` gives the request's shape. Every request also needs `contract` (`directed-actions/v0`), `action`, `agent` and `reason`.

## The request

- `agent` names the agent that is acting, and `reason` says why. Both are required on every action and are recorded with it.
- `identity` is the account that acts. It is required for `visible_to_others` actions and optional for `only_you` actions. When present it must equal the signed-in account. When absent, the request acts as the signed-in account and is compared as if it had named it.
- `target.conversation` and `target.message` name what the action applies to. The tool must already know every target the request names, and each known target has a kind: `conversation`, `chat_message`, `mail_message` or `mail_draft`. `target.conversation` must name a `conversation`, and `target.message` must name the kind the table gives. A reaction's message must also belong to the named conversation.
- `target.recipients` lists everyone the agent expects to see the result. For a `visible_to_others` action, it must equal the current audience of the target the table names, compared as a set. A new mail has no earlier audience, so its recipients are taken as given. A blank recipient is refused.
- `expected_version`, when present, is the version the agent read of the target the table names. It must equal that target's current version.
- `intent_key` is allowed only on send actions.

Every value is compared after removing white space at its start and end. Content is compared in the form that is sent, with every CRLF and lone CR turned into LF. Identities and recipients also ignore letter case, everywhere they are compared: against the signed-in account, against a target's audience, and against earlier requests.

Two requests are the same request when their action, identity, conversation, message, recipients and content are equal under these rules. `agent` and `reason` do not count, so a retry may word its reason differently.

## The decision

The tool checks a request in this order and stops at the first refusal:

1. `contract` must be `directed-actions/v0`: `unsupported_contract`.
2. `action` must be in the table: `unknown_action`.
3. `agent`, then `reason`, must not be blank: `missing_agent`, `missing_reason`.
4. An action on the writes switch is refused: `person_only`.
5. Writes must be on: `writes_off`.
6. An intent key on an action that is not a send is refused: `intent_key_not_allowed`.
7. The fields must fit the action: `invalid_request`, with `field` naming the problem. The tool reports the first missing required field in the table's order, then the first field the action does not use, then a blank recipient, then an `expected_version` with no target to compare against. Fields are taken in this order: `target.conversation`, `target.message`, `target.recipients`, `content.text`, `content.subject`, `content.reaction`, `expected_version`.
8. A `visible_to_others` action needs `identity`: `identity_required`. A given identity must match: `identity_mismatch`.
9. If an earlier request with the same intent key is `queued`, `sent` or `unknown`, the same request gets a replay of that earlier request and nothing is sent. A different request with that key is refused: `intent_key_reused`. After a `failed` outcome nothing happened, so the key is free again.
10. Every named target must be known: `unknown_target`. Each must be the kind the action needs, and a reaction's message must belong to the named conversation: `target_mismatch`.
11. The recipients must match the target's audience: `recipients_changed`.
12. The expected version must match: `stale_version`.
13. If the same request was made earlier and its outcome is `unknown`, the request is refused until the person settles the earlier one: `outcome_unknown`. A new intent key does not get around this. For this rule a `reply_mail` and a `reply_all_mail` with the same identity, message and content are the same request, whatever their recipients, so a reply cannot be resent as a reply-all.
14. Otherwise the request is accepted and the tool acts.

The decision uses what the tool already knows. Before an action that cannot be undone, the tool checks again at the source: before discarding a draft, it fetches the message again and confirms it is still a draft; if it is not, it does not act and the outcome is `failed`.

## The answer

| Field | Meaning |
| --- | --- |
| `decision` | `accept`, `refuse` or `replay`. |
| `effect` | On `accept`, the action's effect class. |
| `code` | On `refuse`, the refusal code. |
| `field` | On `invalid_request`, the first missing or invalid field. |
| `replay_of` | On `replay`, the earlier request's id. |
| `earlier_outcome` | On `replay`, the earlier request's outcome. |
| `exit_class` | On `refuse` and `replay`, what the agent should do next (below). |
| `tell_person` | `true` when the agent must stop and tell the person. |

### Outcomes after acting

An accepted request ends in one of these outcomes:

- `queued`: the tool has taken the request and will finish it.
- `sent`: the action happened.
- `failed`: the action provably did not happen. The agent may fix the problem and ask again.
- `unknown`: the tool cannot tell whether the action happened, for example because the connection dropped after the request left. The agent must not send it again. It tells the person, who checks and settles it as sent or not sent.

A tool reports `failed` only with proof that nothing happened; otherwise it reports `unknown`. A refused request never acted, so it does not hold its intent key.

### Exit classes

| Class | Meaning | Tell the person | m365crawl exit |
| --- | --- | --- | --- |
| `done` | The action happened or is queued. A replay of a `queued` or `sent` request is `done`. | no | 0 |
| `request_problem` | The agent's request is wrong. Fix it and ask again. | no | 2 |
| `person_needed` | Only the person can unblock this: writes are off, the switch is theirs, or an earlier outcome is unknown. | yes | 3 |
| `failed` | The action did not happen. | no | 1 |
| `unknown` | The action may have happened. Never resend. A replay of an `unknown` request is `unknown`. | yes | 5 |

m365crawl's `writes` commands use these exit numbers (SPEC section "Directed writes through WorkIQ").

## Test cases

Each file in `cases/` holds one case:

- `name` and `description` say what it checks.
- `state` is what the tool knows: `writes` (`on` or `off`), `signed_in` (the signed-in account), `targets` (each known conversation or message, keyed by its id, with its `kind`, `recipients`, `version` and, for a chat message, its `conversation`), and `earlier` (accepted requests, each with `request_id`, `outcome` and the original `request`).
- `request` is the agent's request.
- `expect` is the answer. Fields that do not apply are left out, and `tell_person` is left out when it is `false`.

Some cases break the schema on purpose, such as a request with no `reason`, because the decision order says how a tool refuses them. All names, addresses and ids in the cases are synthetic.
