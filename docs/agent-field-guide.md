# A field guide for the next agent

**Find the evidence. Check its limits. Then answer.**

Use this page when you inherit a question, not a whole conversation. It is a
workflow, not another command contract: [the command reference](commands.md),
[the installed skill](../.agents/skills/m365crawl/SKILL.md) and
[SPEC.md](../SPEC.md) own the details.

## Start here in 30 seconds

```sh
m365crawl --json version
m365crawl
m365crawl skill
```

These identify the installed build, show the archive's sources and freshness,
and print the guide that matches that build. The overview never syncs. For
subsequent archive reads, use `--no-sync` when you do not want a desktop refresh;
keep the same explicit `--db PATH` throughout if you selected an archive.
See [overview](commands.md#overview) and the [read workflow](../.agents/skills/m365crawl/SKILL.md#start-here).

Choose the smallest next step:

| What you need | Start with | What not to conclude |
| --- | --- | --- |
| One conversation | Narrow search, then the returned thread route | A preview is the whole thread |
| Yesterday's work | `m365crawl --no-sync coverage --day yesterday` | No hits means no work |
| Older mail | The read's `workiq_routes[].next` commands | A full mailbox fetch is always necessary |
| A linked Office document | The explicit content workflow | Being able to view it grants permission to extract it |
| To send something | The directed-write contract and the person's instruction | Installed tools or a prepared draft authorize sending |

The [skill](../.agents/skills/m365crawl/SKILL.md) describes the search/thread
workflow; [coverage](commands.md#coverage), [WorkIQ](commands.md#workiq),
[content acquisition](content.md) and [directed writes](commands.md#writes)
define the other rows.

## A worked example: "Did I send nothing that day?"

Use a made-up day, not somebody's mailbox contents, to learn the sequence:

```sh
m365crawl --no-sync coverage --day 2026-09-01 --source mail:sent
m365crawl --no-sync mail list --folder sent --since 2026-09-01 --until 2026-09-01 --limit 20
```

Read both the counts and their qualifications. Inspect the relevant coverage
entry and the mail result's `range_coverage`, `source_health`, `truncated` and
`workiq_routes` when present. A read's equal date-only bounds select that local
day; this is **not** the meaning of a WorkIQ window's equal bounds.
See [mail list](commands.md#mail-list), [coverage](commands.md#coverage) and
[WorkIQ windows](commands.md#workiq).

Here is the decision, not invented output:

1. If the relevant entry is `covered` and its count is zero, report the zero
   within that entry's source and scope.
2. For any state other than `covered`, including `partial`, `not_covered` or an
   unfamiliar future value, report the observed count as a lower bound, not a
   complete account of the day. Follow its `fix` or exact route.
3. If an authorized WorkIQ read is needed, follow the emitted account and
   window rather than guessing either. Repeat the original offline read after
   the fetch, and check coverage again.

A safe answer might be: "I found no retained items for that source, but this
day is not covered, so I cannot say none existed." A complete zero and an
unknown zero must never sound the same. [Coverage defines that distinction](commands.md#coverage).

## Five traps worth leaving a note about

| Trap | Better move |
| --- | --- |
| `--max-age 0` sounds like "fresh now" | It disables implicit sync. Use `--no-sync` to make offline intent clear, or run an explicit desktop sync when authorized. |
| WorkIQ returned some data, so it finished | Read `status`, `resumable` and `gaps`. Retry the same command only when `resumable` is true; follow the error's fix otherwise. |
| A quick mail window proves completeness | Listing mode is never proof. Check `window.covered`; follow the emitted proof route if one is needed. |
| Server reads worked, so Outlook is healthy | Keep source health and provenance separate. Server coverage does not repair the local reader. |
| A send timed out, so try again | An `unknown` write outcome is unresolved, not unsent. Inspect `writes verify ID`; never resend blindly. |

The [read workflow](../.agents/skills/m365crawl/SKILL.md#start-here),
[WorkIQ reference](commands.md#workiq) and
[write outcome contract](commands.md#writes) are the checks behind these rules.
For protected documents, an extraction refusal is a boundary: do not switch
readers to get around it. [Content acquisition](content.md) explains the rights check.

## Leave a trail, not a transcript

When handing work over, record the installed version, selected archive pointer
(privately), exact command and filters, result status, coverage and gaps,
unfinished operations and the next concrete check. Include source IDs only in
the user's authorized context. Never attach a real archive, message body,
credential or raw private session to a public issue.
[Privacy](privacy.md) and the [agent privacy rules](../.agents/skills/m365crawl/SKILL.md#privacy)
apply to evidence as well as answers.

Use a small handoff receipt:

```text
Question and scope:
Installed version:
Command and filters:
Observed result:
Coverage and missing evidence:
Unresolved operation IDs (do not replay):
Next check and owner:
```

For a product change, keep one writer per checkout, reproduce with synthetic
inputs, and distinguish fixture checks from native-host and live-service
qualification. A merged fix is not an installed release; verify the consumer's
actual version and the affected workflow. Follow [AGENTS.md](../AGENTS.md) and
the [release procedure](releasing.md), rather than copying an old session's
release commands.
