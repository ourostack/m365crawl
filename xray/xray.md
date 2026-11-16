# AX X-ray: m365crawl

Ouro AX claim set 1.5.0 (sha256 dfcf73422b9f718d709d2210649ec1e3b98c5d19650cc1180b358368fb6b1feb), checked by ax-xray 1.5.0 at 2026-11-16T11:04:32.049Z. Target: at local; CLI `m365crawl` (96 commands, 255 calls).

**Conforms: every applicable must claim holds, and the inventory covers every surface checked.** Claims holding: 22 of 33 (must: 14 of 14). Checks: 625 pass, 136 fail, 3 skipped.

Calls to what other people see: allowed, the target is a proven mock (stub: yes, loopback: yes, isolated home: yes, no credentials: yes, mock identity: yes).

Effects the X-ray caused: none recorded.

| | Claim | Tier | Rule | Pass | Fail | Skip |
|---|---|---|---|---|---|---|
| ✗ | `cli.help-size` | should | Each command's `--help` is at most 6,000 bytes. | 37 | 59 | 0 |
| ✗ | `cli.named-is-listed` | should | Every command the CLI tells an agent to run, in an error's fix or another command's help, is listed in the overview. | 0 | 33 | 0 |
| ✗ | `cli.read-json` | should | Every read takes `--json` and prints exactly one JSON document (a stream prints JSON lines) and writes nothing to stderr. It exits 0, except a check command (a doctor), which exits with the declared `findings` code when its checks find a problem. | 35 | 28 | 0 |
| ✗ | `cli.one-line-error` | should | Every error is one line on stderr; stdout stays empty, or with `--json` holds the same error as one JSON document. | 73 | 6 | 0 |
| ✗ | `cli.fact-before-voice` | should | An action's text result starts with its outcome's summary: what happened, and who can see it, before anything else. | 0 | 6 | 0 |
| ✗ | `cli.help-width` | should | No help line is wider than the width limit (100 columns by default). | 95 | 2 | 0 |
| ✗ | `cli.overview-short` | should | The overview (`--help`) fits one screen: at most 60 lines. | 0 | 1 | 0 |
| ✗ | `cli.voice-off-in-json` | should | Under `--json`, stdout holds the JSON alone, and no voice line is printed on stdout or stderr. | 0 | 1 | 0 |
| – | `cli.check-verdict` | should | A check command (a doctor) puts its verdict on the first line of its text, and its `--json` has the same verdict and exit code. | 0 | 0 | 1 |
| – | `cli.work-events` | should | The interface records an attributed work event (`work-event/1`) for each action, handoff and wait, and an agent can read them through the interface. | 0 | 0 | 1 |
| – | `cli.voice-off-when-piped` | should | A voice line prints only when stdout is a terminal: never into a pipe, a file or an agent's tool call. | 0 | 0 | 1 |
| ✓ | `cli.help-parses` | must | `<cli> help --json` prints one JSON document that names every command. | 1 | 0 | 0 |
| ✓ | `cli.help-effects` | must | Help says who sees each command: only the person, or other people. | 96 | 0 | 0 |
| ✓ | `cli.inventory` | must | Every command the CLI registers is listed once in `help --json` and in the inventory the adopter generates from its command registration, and the X-ray covers exactly that set. | 1 | 0 | 0 |
| ✓ | `cli.exit-codes` | must | Exit codes are documented, and every exit code the CLI gives is one of them. | 1 | 0 | 0 |
| ✓ | `cli.unknown-flag` | must | An unknown flag fails before anything runs, and the error names the flag. | 63 | 0 | 0 |
| ✓ | `cli.ambiguous-verbs` | must | Ambiguous verbs (edit, delete, remove) don't act; they answer with the specific commands. | 3 | 0 | 0 |
| ✓ | `cli.others-need-why` | must | Every command other people see refuses to run without `--why`, and says so. | 6 | 0 | 0 |
| ✓ | `cli.wrong-ref-first` | must | A wrong reference fails before anything reaches anyone, and the error says where to get a right one. | 6 | 0 | 0 |
| ✓ | `cli.others-preview` | must | Every command other people see has a preview that acts on nothing (`--dry-run`) and names who will see it. | 6 | 0 | 0 |
| ✓ | `cli.wait-on-refs` | must | A wait command accepts the refs the reads list: `watch <ref>` waits on the conversation a list gave. | 1 | 0 | 0 |
| ✓ | `cli.reads-stay-reads` | must | Reads change nothing. | 1 | 0 | 0 |
| ✓ | `cli.refusals-change-nothing` | must | A refused call changes nothing, except an audit record that says it was refused. | 7 | 0 | 0 |
| ✓ | `cli.no-dev-commands` | must | No message the CLI gives an agent or a person (help, a refusal, an error, a next step, an outcome's `human.resume`, a doctor's fix) asks for a developer command, a repository path or a step that only works in a source checkout, unless the step is marked `sourceCheckout: true`. | 1 | 0 | 0 |
| ✓ | `copy.precise-failure` | must | Every failure the CLI reports to an agent or a person (a refusal, an error, an outcome's `error` and `summary`, `human.reason`, a doctor's finding, and any failure `help --json` describes) says what happened and precisely why (the system and its answer), who or what caused it where that is known, and the next step; when the cause is unknown, what was tried and what came back. It never stops at "couldn't", "could not", "something went wrong", "unable to", "failed to load", "an error occurred" or a bare "Error" or "Failed": a phrase passes only with its cause after it in the same sentence ("Couldn't send: you're offline", "… because the server refused it"; not "Couldn't send: try again"). | 1 | 0 | 0 |
| ✓ | `cli.command-help` | should | Every command answers `<command> --help` with exit 0 on stdout, and nothing on stderr. | 96 | 0 | 0 |
| ✓ | `cli.unknown-command` | should | An unknown command fails with the usage exit code and lists the commands, or names the help that lists them. | 1 | 0 | 0 |
| ✓ | `cli.error-teaches` | should | Every error names a next action: a command to run or a flag to add. | 79 | 0 | 0 |
| ✓ | `cli.no-stack-trace` | should | No call prints a stack trace. | 1 | 0 | 0 |
| ✓ | `cli.plain-pipe` | should | Output into a pipe is plain text: no ANSI escape codes and no carriage-return redraws, and `NO_COLOR` wins over `FORCE_COLOR`. | 1 | 0 | 0 |
| ✓ | `cli.outcome` | should | An action's `--json` result is an outcome (`outcome/1`) that validates: its state, who can see it, a postcondition, a receipt and the next commands. | 6 | 0 | 0 |
| ✓ | `cli.wait-typed` | should | A wait's `--json` answer is a wait (`wait/1`): what happened, or, when nothing did in time, the cursor and the exact command that resumes. | 1 | 0 | 0 |
| ✓ | `cli.audience-named` | should | The preview of an action other people see names who will see it, not a placeholder such as "other people". | 6 | 0 | 0 |

## What fails, and the fix

### `cli.help-size`: Each command's `--help` is at most 6,000 bytes.

Why: Agents read a command's help before using it, often on every task; a long page costs tokens each time.

Fix: Move rules that most calls don't need into a help topic, and point to it in one line.

- **search --help**: 24058 bytes, over 6000
- **messages --help**: 13516 bytes, over 6000
- **conversations --help**: 6286 bytes, over 6000
- **activity --help**: 6971 bytes, over 6000
- **coverage --help**: 8361 bytes, over 6000
- **calendar attendees sources --help**: 6217 bytes, over 6000
- **calendar attendees list --help**: 6453 bytes, over 6000
- **calendar agenda --help**: 7162 bytes, over 6000
- **calendar actions --help**: 6464 bytes, over 6000
- **calendar event --help**: 11486 bytes, over 6000
- **mail list --help**: 8068 bytes, over 6000
- **workiq sync --help**: 6664 bytes, over 6000
- **workiq coverage --help**: 6204 bytes, over 6000
- **writes on --help**: 6030 bytes, over 6000
- **writes off --help**: 6016 bytes, over 6000
- **writes status --help**: 6076 bytes, over 6000
- **writes target --help**: 6972 bytes, over 6000
- **writes send-teams-message --help**: 7884 bytes, over 6000
- **writes save-mail-draft --help**: 7957 bytes, over 6000
- **writes reply-mail --help**: 8015 bytes, over 6000
- **writes reply-all-mail --help**: 8049 bytes, over 6000
- **writes send-mail --help**: 7213 bytes, over 6000
- **writes discard-mail-draft --help**: 7198 bytes, over 6000
- **writes add-reaction --help**: 7520 bytes, over 6000
- **writes remove-reaction --help**: 7528 bytes, over 6000
- **writes verify --help**: 6076 bytes, over 6000
- **writes settle --help**: 6226 bytes, over 6000
- **docs sync --help**: 9720 bytes, over 6000
- **docs list --help**: 10042 bytes, over 6000
- **docs search --help**: 9315 bytes, over 6000
- **docs show --help**: 7722 bytes, over 6000
- **loop sync --help**: 6705 bytes, over 6000
- **loop list --help**: 7634 bytes, over 6000
- **loop search --help**: 7559 bytes, over 6000
- **loop show --help**: 7823 bytes, over 6000
- **scout sync --help**: 7812 bytes, over 6000
- **scout list --help**: 7242 bytes, over 6000
- **scout search --help**: 6719 bytes, over 6000
- **drafts list --help**: 6870 bytes, over 6000
- **drafts show --help**: 6368 bytes, over 6000
- **content capture --help**: 8969 bytes, over 6000
- **content fetch image --help**: 7101 bytes, over 6000
- **content fetch engage-thread --help**: 7724 bytes, over 6000
- **content fetch engage-home --help**: 7025 bytes, over 6000
- **content fetch page --help**: 6936 bytes, over 6000
- **content fetch stream --help**: 7220 bytes, over 6000
- **content fetch doc --help**: 6196 bytes, over 6000
- **content signin --help**: 6944 bytes, over 6000
- **content list --help**: 10950 bytes, over 6000
- **content search --help**: 11014 bytes, over 6000
- **content show --help**: 10450 bytes, over 6000
- **content sources --help**: 9585 bytes, over 6000
- **transcripts list --help**: 6346 bytes, over 6000
- **transcripts fetch --help**: 7176 bytes, over 6000
- **transcripts signin --help**: 6216 bytes, over 6000
- **unread --help**: 13169 bytes, over 6000
- **thread --help**: 11795 bytes, over 6000
- **watch --help**: 7583 bytes, over 6000
- **sql --help**: 23782 bytes, over 6000

### `cli.named-is-listed`: Every command the CLI tells an agent to run, in an error's fix or another command's help, is listed in the overview.

Why: A command that only another command's help names is found late or not at all.

Fix: List the command in the overview, or stop naming it.

- **overview**: `m365crawl help --json` names `m365crawl overview`, which m365crawl --help does not list
- **doctor**: `m365crawl help --json` names `m365crawl doctor`, which m365crawl --help does not list
- **sync**: `m365crawl help --json` names `m365crawl sync`, which m365crawl --help does not list
- **status**: `m365crawl help --json` names `m365crawl status`, which m365crawl --help does not list
- **search**: `m365crawl help --json` names `m365crawl search`, which m365crawl --help does not list
- **mail**: `m365crawl help --json` names `m365crawl mail`, which m365crawl --help does not list
- **messages**: `m365crawl help --json` names `m365crawl messages`, which m365crawl --help does not list
- **conversations**: `m365crawl help --json` names `m365crawl conversations`, which m365crawl --help does not list
- **teams**: `m365crawl help --json` names `m365crawl teams`, which m365crawl --help does not list
- **people**: `m365crawl help --json` names `m365crawl people`, which m365crawl --help does not list
- **activity**: `m365crawl help --json` names `m365crawl activity`, which m365crawl --help does not list
- **coverage**: `m365crawl help --json` names `m365crawl coverage`, which m365crawl --help does not list
- **calendar**: `m365crawl help --json` names `m365crawl calendar`, which m365crawl --help does not list
- **workiq**: `m365crawl help --json` names `m365crawl workiq`, which m365crawl --help does not list
- **writes**: `m365crawl help --json` names `m365crawl writes`, which m365crawl --help does not list
- **notes**: `m365crawl help --json` names `m365crawl notes`, which m365crawl --help does not list
- **docs**: `m365crawl help --json` names `m365crawl docs`, which m365crawl --help does not list
- **loop**: `m365crawl help --json` names `m365crawl loop`, which m365crawl --help does not list
- **scout**: `m365crawl help --json` names `m365crawl scout`, which m365crawl --help does not list
- **drafts**: `m365crawl help --json` names `m365crawl drafts`, which m365crawl --help does not list
- **content**: `m365crawl help --json` names `m365crawl content`, which m365crawl --help does not list
- **transcripts**: `m365crawl help --json` names `m365crawl transcripts`, which m365crawl --help does not list
- **stores**: `m365crawl help --json` names `m365crawl stores`, which m365crawl --help does not list
- **records**: `m365crawl help --json` names `m365crawl records`, which m365crawl --help does not list
- **unread**: `m365crawl help --json` names `m365crawl unread`, which m365crawl --help does not list
- **thread**: `m365crawl help --json` names `m365crawl thread`, which m365crawl --help does not list
- **watch**: `m365crawl help --json` names `m365crawl watch`, which m365crawl --help does not list
- **whoami**: `m365crawl help --json` names `m365crawl whoami`, which m365crawl --help does not list
- **sql**: `m365crawl help --json` names `m365crawl sql`, which m365crawl --help does not list
- **skill**: `m365crawl help --json` names `m365crawl skill`, which m365crawl --help does not list
- **help**: `m365crawl help --json` names `m365crawl help`, which m365crawl --help does not list
- **metadata**: `m365crawl help --json` names `m365crawl metadata`, which m365crawl --help does not list
- **version**: `m365crawl help --json` names `m365crawl version`, which m365crawl --help does not list

### `cli.read-json`: Every read takes `--json` and prints exactly one JSON document (a stream prints JSON lines) and writes nothing to stderr. It exits 0, except a check command (a doctor), which exits with the declared `findings` code when its checks find a problem.

Why: One parseable answer per call is the cheapest thing for an agent to consume.

Fix: Give the read a `--json` that prints the same data the text shows, as one document.

- **doctor**: `m365crawl doctor --json` → exit 3; stderr: {"error":{"code":"doctor_failed","message":"failing checks: teams_installed, full_disk_access, teams_origin","fix":"Resolve the failing checks listed by `m365crawl doctor`, then run again."}}
- **search**: `m365crawl search --json` → exit 2; stderr: {"error":{"code":"usage","message":"search needs words to find or at least one filter (--mentions-me, --from, --conversation, --team, --since, --until)","fix":"To list messages without a text query, …
- **people show**: `m365crawl people show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl people show --help` to see the accepted arguments and flags."}}
- **calendar attendees list**: `m365crawl calendar attendees list --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<event>\"","fix":"Run `m365crawl calendar attendees list --help` to see the accepted arguments and flags."}}
- **calendar attendees show**: `m365crawl calendar attendees show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl calendar attendees show --help` to see the accepted arguments and flags."}}
- **calendar event**: `m365crawl calendar event --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<event>\"","fix":"Run `m365crawl calendar event --help` to see the accepted arguments and flags."}}
- **mail show**: `m365crawl mail show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl mail show --help` to see the accepted arguments and flags."}}
- **mail thread**: `m365crawl mail thread --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl mail thread --help` to see the accepted arguments and flags."}}
- **workiq coverage**: `m365crawl workiq coverage --json` → exit 3; stderr: {"error":{"code":"workiq_disabled","message":"the explicit WorkIQ source could not complete (workiq_disabled)","fix":"Inspect the optional source setup and retry explicitly; no sign-in or agreement w…
- **workiq status**: `m365crawl workiq status --json` → exit 1; stderr: {"error":{"code":"workiq_protocol","message":"the optional WorkIQ source could not complete (workiq_protocol)","fix":"WorkIQ's output was not the MCP JSON this version reads. A WorkIQ log level that …
- **writes target**: `m365crawl writes target --json` → exit 2; stderr: {"error":{"code":"usage","message":"give exactly one of --chat (with --chat-message for a reaction), --message (with --all for a reply-all) and --draft","fix":"Run `m365crawl writes target --help` to…
- **notes search**: `m365crawl notes search --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<query>\"","fix":"Run `m365crawl notes search --help` to see the accepted arguments and flags."}}
- **notes show**: `m365crawl notes show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl notes show --help` to see the accepted arguments and flags."}}
- **docs search**: `m365crawl docs search --json` → exit 2; stderr: {"error":{"code":"usage","message":"docs search needs words, a native date filter, --application or --surface; use docs list for an unfiltered list","fix":"Run `m365crawl docs search --help` to see t…
- **docs show**: `m365crawl docs show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl docs show --help` to see the accepted arguments and flags."}}
- **loop search**: `m365crawl loop search --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<query>\"","fix":"Run `m365crawl loop search --help` to see the accepted arguments and flags."}}
- **loop show**: `m365crawl loop show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl loop show --help` to see the accepted arguments and flags."}}
- **scout search**: `m365crawl scout search --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<query>\"","fix":"Run `m365crawl scout search --help` to see the accepted arguments and flags."}}
- **scout show**: `m365crawl scout show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl scout show --help` to see the accepted arguments and flags."}}
- **drafts show**: `m365crawl drafts show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl drafts show --help` to see the accepted arguments and flags."}}
- **content images**: `m365crawl content images --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<target>\"","fix":"Run `m365crawl content images --help` to see the accepted arguments and flags."}}
- **content search**: `m365crawl content search --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<query>\"","fix":"Run `m365crawl content search --help` to see the accepted arguments and flags."}}
- **content show**: `m365crawl content show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<id>\"","fix":"Run `m365crawl content show --help` to see the accepted arguments and flags."}}
- **transcripts show**: `m365crawl transcripts show --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<meeting>\"","fix":"Run `m365crawl transcripts show --help` to see the accepted arguments and flags."}}
- **records**: `m365crawl records --json` → exit 2; stderr: {"error":{"code":"usage","message":"missing flags: --database=STRING","fix":"Run `m365crawl records --help` to see the accepted arguments and flags."}}
- **thread**: `m365crawl thread --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<target>\"","fix":"Run `m365crawl thread --help` to see the accepted arguments and flags."}}
- **sql**: `m365crawl sql --json` → exit 2; stderr: {"error":{"code":"usage","message":"expected \"<query>\"","fix":"Run `m365crawl sql --help` to see the accepted arguments and flags."}}
- **skill**: `m365crawl skill --json` → exit 0; stdout: ---; stdout is not one JSON document

### `cli.one-line-error`: Every error is one line on stderr; stdout stays empty, or with `--json` holds the same error as one JSON document.

Why: One line is cheap to read and easy to quote back; stdout stays parseable.

Fix: Catch errors at the entry point and print one line to stderr.

- **writes send-teams-message (wrong ref)**: `m365crawl writes send-teams-message --chat axxraynoref --text axxraynoref --why "ax-xray conformance probe: nobody aske…` → exit 2; stderr: {"error":{"code":"write_request_problem","message":"the directed write was refused: missing_agent; nothing was sent","fix":"Fix the request as the printed code and field say, then ask again; `m365cra… (1 stderr lines, 1 stdout lines)
- **writes reply-mail (wrong ref)**: `m365crawl writes reply-mail --message axxraynoref --text axxraynoref --why "ax-xray conformance probe: nobody asked, so…` → exit 2; stderr: {"error":{"code":"write_request_problem","message":"the directed write was refused: missing_agent; nothing was sent","fix":"Fix the request as the printed code and field say, then ask again; `m365cra… (1 stderr lines, 1 stdout lines)
- **writes reply-all-mail (wrong ref)**: `m365crawl writes reply-all-mail --message axxraynoref --text axxraynoref --why "ax-xray conformance probe: nobody asked…` → exit 2; stderr: {"error":{"code":"write_request_problem","message":"the directed write was refused: missing_agent; nothing was sent","fix":"Fix the request as the printed code and field say, then ask again; `m365cra… (1 stderr lines, 1 stdout lines)
- **writes send-mail (wrong ref)**: `m365crawl writes send-mail --to axxraynoref --subject axxraynoref --text axxraynoref --why "ax-xray conformance probe: …` → exit 2; stderr: {"error":{"code":"write_request_problem","message":"the directed write was refused: missing_agent; nothing was sent","fix":"Fix the request as the printed code and field say, then ask again; `m365cra… (1 stderr lines, 1 stdout lines)
- **writes add-reaction (wrong ref)**: `m365crawl writes add-reaction --chat axxraynoref --message axxraynoref --reaction axxraynoref --why "ax-xray conformanc…` → exit 2; stderr: {"error":{"code":"write_request_problem","message":"the directed write was refused: missing_agent; nothing was sent","fix":"Fix the request as the printed code and field say, then ask again; `m365cra… (1 stderr lines, 1 stdout lines)
- **writes remove-reaction (wrong ref)**: `m365crawl writes remove-reaction --chat axxraynoref --message axxraynoref --reaction axxraynoref --why "ax-xray conform…` → exit 2; stderr: {"error":{"code":"write_request_problem","message":"the directed write was refused: missing_agent; nothing was sent","fix":"Fix the request as the printed code and field say, then ask again; `m365cra… (1 stderr lines, 1 stdout lines)

### `cli.fact-before-voice`: An action's text result starts with its outcome's summary: what happened, and who can see it, before anything else.

Why: The first line is the one an agent and a person read; it carries the fact, and anything warmer comes after it.

Fix: Print the outcome's summary first (`outcomeText` does), and any voice line after it.

- **writes send-teams-message**: the text starts "{\"ax\":\"outcome/1\",\"action\":\"send_teams_message\",\"effect\":\"others\",\"audience\":{\"only\":false,\"who\":\"e…", not the summary "Would send that message: \"ax-xray probe\", for everyone in chat 19:00000000-0000-4000-8000-000000000…"
- **writes reply-mail**: the text starts "{\"ax\":\"outcome/1\",\"action\":\"reply_mail\",\"effect\":\"others\",\"audience\":{\"only\":false,\"who\":\"the sende…", not the summary "Would send that reply: \"ax-xray probe\", for the sender and reply-to addresses of mail 1700000061000…"
- **writes reply-all-mail**: the text starts "{\"ax\":\"outcome/1\",\"action\":\"reply_all_mail\",\"effect\":\"others\",\"audience\":{\"only\":false,\"who\":\"the r…", not the summary "Would send that reply to everyone on the mail: \"ax-xray probe\", for the reply-to addresses of mail …"
- **writes send-mail**: the text starts "{\"ax\":\"outcome/1\",\"action\":\"send_mail\",\"effect\":\"others\",\"audience\":{\"only\":false,\"who\":\"ana@exampl…", not the summary "Would send that mail: \"ax-xray probe\", for ana@example.com; they will see it. Nothing was sent or c…"
- **writes add-reaction**: the text starts "{\"ax\":\"outcome/1\",\"action\":\"add_reaction\",\"effect\":\"others\",\"audience\":{\"only\":false,\"who\":\"everyon…", not the summary "Would add the reaction 👍 to that message, for everyone in chat 19:00000000-0000-4000-8000-00000000…"
- **writes remove-reaction**: the text starts "{\"ax\":\"outcome/1\",\"action\":\"remove_reaction\",\"effect\":\"others\",\"audience\":{\"only\":false,\"who\":\"ever…", not the summary "Would remove the reaction 👍 from that message, for everyone in chat 19:00000000-0000-4000-8000-000…"

### `cli.help-width`: No help line is wider than the width limit (100 columns by default).

Why: Long lines wrap unpredictably in terminals and transcripts and cost tokens to re-read.

Fix: Wrap help at one width in the help renderer, not by hand.

- **--help**: 6 lines over 100 columns, first:   m365crawl search "words"    search chats, mail, fetched transcripts, notes, document metadata, Loop text and archived cloud content
- **overview --help**: 6 lines over 100 columns, first (133):   m365crawl search "words"    search chats, mail, fetched transcripts, notes, document metadata, Loop text and archived cloud content

### `cli.overview-short`: The overview (`--help`) fits one screen: at most 60 lines.

Why: The overview is read on every cold start; details belong on each command's page.

Fix: Keep one line per command in the overview and move details to `<command> --help`.

- **--help**: 448 lines

### `cli.voice-off-in-json`: Under `--json`, stdout holds the JSON alone, and no voice line is printed on stdout or stderr.

Why: An agent parses `--json`; a line of character around the document breaks the parse or costs tokens.

Fix: Print voice lines only on a terminal, never under `--json` (the kit's `voice` does this).

- **m365crawl skill --json**: stdout is not JSON alone: ---

## Every surface

| Surface | Holds | Fails | Skipped |
|---|---|---|---|
| cli · help | `cli.help-parses` `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · inventory | `cli.inventory` |  |  |
| cli · overview | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · doctor | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` `cli.named-is-listed` |  |
| cli · sync | `cli.help-effects` `cli.command-help` | `cli.named-is-listed` |  |
| cli · status | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · search | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` `cli.named-is-listed` |  |
| cli · messages | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · conversations | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · teams | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · people list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · people sync | `cli.help-effects` `cli.command-help` |  |  |
| cli · people show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · people sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · activity | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · coverage | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · calendar attendees sync | `cli.help-effects` `cli.command-help` |  |  |
| cli · calendar attendees sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · calendar attendees list | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · calendar attendees show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · calendar agenda | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · calendar sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · calendar actions | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · calendar event | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · mail list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · mail show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · mail thread | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · mail folders | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · mail unread | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · workiq enable | `cli.help-effects` `cli.command-help` |  |  |
| cli · workiq disable | `cli.help-effects` `cli.command-help` |  |  |
| cli · workiq sync | `cli.help-effects` `cli.command-help` |  |  |
| cli · workiq coverage | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · workiq status | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · writes on | `cli.help-effects` `cli.command-help` |  |  |
| cli · writes off | `cli.help-effects` `cli.command-help` |  |  |
| cli · writes status | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · writes target | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · writes send-teams-message | `cli.help-effects` `cli.command-help` `cli.others-need-why` `cli.wrong-ref-first` `cli.refusals-change-nothing` `cli.others-preview` `cli.outcome` `cli.audience-named` | `cli.fact-before-voice` |  |
| cli · writes save-mail-draft | `cli.help-effects` `cli.command-help` |  |  |
| cli · writes reply-mail | `cli.help-effects` `cli.command-help` `cli.others-need-why` `cli.wrong-ref-first` `cli.refusals-change-nothing` `cli.others-preview` `cli.outcome` `cli.audience-named` | `cli.fact-before-voice` |  |
| cli · writes reply-all-mail | `cli.help-effects` `cli.command-help` `cli.others-need-why` `cli.wrong-ref-first` `cli.refusals-change-nothing` `cli.others-preview` `cli.outcome` `cli.audience-named` | `cli.fact-before-voice` |  |
| cli · writes send-mail | `cli.help-effects` `cli.command-help` `cli.others-need-why` `cli.wrong-ref-first` `cli.refusals-change-nothing` `cli.others-preview` `cli.outcome` `cli.audience-named` | `cli.fact-before-voice` |  |
| cli · writes discard-mail-draft | `cli.help-effects` `cli.command-help` |  |  |
| cli · writes add-reaction | `cli.help-effects` `cli.command-help` `cli.others-need-why` `cli.wrong-ref-first` `cli.refusals-change-nothing` `cli.others-preview` `cli.outcome` `cli.audience-named` | `cli.fact-before-voice` |  |
| cli · writes remove-reaction | `cli.help-effects` `cli.command-help` `cli.others-need-why` `cli.wrong-ref-first` `cli.refusals-change-nothing` `cli.others-preview` `cli.outcome` `cli.audience-named` | `cli.fact-before-voice` |  |
| cli · writes verify | `cli.help-effects` `cli.command-help` |  |  |
| cli · writes settle | `cli.help-effects` `cli.command-help` |  |  |
| cli · notes sync | `cli.help-effects` `cli.command-help` |  |  |
| cli · notes list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · notes search | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · notes show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · notes sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · docs sync | `cli.help-effects` `cli.command-help` |  |  |
| cli · docs list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · docs search | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · docs show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · docs sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · loop sync | `cli.help-effects` `cli.command-help` |  |  |
| cli · loop list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · loop search | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · loop show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · loop sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · scout sync | `cli.help-effects` `cli.command-help` |  |  |
| cli · scout list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · scout search | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · scout show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · scout sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · drafts list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · drafts show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · content images | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · content capture | `cli.help-effects` `cli.command-help` |  |  |
| cli · content fetch image | `cli.help-effects` `cli.command-help` |  |  |
| cli · content fetch engage-thread | `cli.help-effects` `cli.command-help` |  |  |
| cli · content fetch engage-home | `cli.help-effects` `cli.command-help` |  |  |
| cli · content fetch page | `cli.help-effects` `cli.command-help` |  |  |
| cli · content fetch stream | `cli.help-effects` `cli.command-help` |  |  |
| cli · content fetch doc | `cli.help-effects` `cli.command-help` |  |  |
| cli · content signin | `cli.help-effects` `cli.command-help` |  |  |
| cli · content list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · content search | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · content show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · content sources | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · transcripts list | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` |  |  |
| cli · transcripts show | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` |  |
| cli · transcripts fetch | `cli.help-effects` `cli.command-help` |  |  |
| cli · transcripts signin | `cli.help-effects` `cli.command-help` |  |  |
| cli · stores | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · records | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` `cli.named-is-listed` |  |
| cli · unread | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · thread | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` `cli.named-is-listed` |  |
| cli · watch | `cli.help-effects` `cli.command-help` | `cli.named-is-listed` |  |
| cli · whoami | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · sql | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` `cli.named-is-listed` |  |
| cli · skill | `cli.help-effects` `cli.command-help` `cli.unknown-flag` | `cli.read-json` `cli.named-is-listed` |  |
| cli · metadata | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · version | `cli.help-effects` `cli.command-help` `cli.read-json` `cli.unknown-flag` | `cli.named-is-listed` |  |
| cli · --help |  | `cli.help-width` `cli.overview-short` |  |
| cli · overview --help | `cli.help-size` | `cli.help-width` |  |
| cli · doctor --help | `cli.help-size` `cli.help-width` |  |  |
| cli · sync --help | `cli.help-size` `cli.help-width` |  |  |
| cli · status --help | `cli.help-size` `cli.help-width` |  |  |
| cli · search --help | `cli.help-width` | `cli.help-size` |  |
| cli · messages --help | `cli.help-width` | `cli.help-size` |  |
| cli · conversations --help | `cli.help-width` | `cli.help-size` |  |
| cli · teams --help | `cli.help-size` `cli.help-width` |  |  |
| cli · people list --help | `cli.help-size` `cli.help-width` |  |  |
| cli · people sync --help | `cli.help-size` `cli.help-width` |  |  |
| cli · people show --help | `cli.help-size` `cli.help-width` |  |  |
| cli · people sources --help | `cli.help-size` `cli.help-width` |  |  |
| cli · activity --help | `cli.help-width` | `cli.help-size` |  |
| cli · coverage --help | `cli.help-width` | `cli.help-size` |  |
| cli · calendar attendees sync --help | `cli.help-size` `cli.help-width` |  |  |
| cli · calendar attendees sources --help | `cli.help-width` | `cli.help-size` |  |
| cli · calendar attendees list --help | `cli.help-width` | `cli.help-size` |  |
| cli · calendar attendees show --help | `cli.help-size` `cli.help-width` |  |  |
| cli · calendar agenda --help | `cli.help-width` | `cli.help-size` |  |
| cli · calendar sources --help | `cli.help-size` `cli.help-width` |  |  |
| cli · calendar actions --help | `cli.help-width` | `cli.help-size` |  |
| cli · calendar event --help | `cli.help-width` | `cli.help-size` |  |
| cli · mail list --help | `cli.help-width` | `cli.help-size` |  |
| cli · mail show --help | `cli.help-size` `cli.help-width` |  |  |
| cli · mail thread --help | `cli.help-size` `cli.help-width` |  |  |
| cli · mail folders --help | `cli.help-size` `cli.help-width` |  |  |
| cli · mail unread --help | `cli.help-size` `cli.help-width` |  |  |
| cli · workiq enable --help | `cli.help-size` `cli.help-width` |  |  |
| cli · workiq disable --help | `cli.help-size` `cli.help-width` |  |  |
| cli · workiq sync --help | `cli.help-width` | `cli.help-size` |  |
| cli · workiq coverage --help | `cli.help-width` | `cli.help-size` |  |
| cli · workiq status --help | `cli.help-size` `cli.help-width` |  |  |
| cli · writes on --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes off --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes status --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes target --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes send-teams-message --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes save-mail-draft --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes reply-mail --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes reply-all-mail --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes send-mail --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes discard-mail-draft --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes add-reaction --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes remove-reaction --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes verify --help | `cli.help-width` | `cli.help-size` |  |
| cli · writes settle --help | `cli.help-width` | `cli.help-size` |  |
| cli · notes sync --help | `cli.help-size` `cli.help-width` |  |  |
| cli · notes list --help | `cli.help-size` `cli.help-width` |  |  |
| cli · notes search --help | `cli.help-size` `cli.help-width` |  |  |
| cli · notes show --help | `cli.help-size` `cli.help-width` |  |  |
| cli · notes sources --help | `cli.help-size` `cli.help-width` |  |  |
| cli · docs sync --help | `cli.help-width` | `cli.help-size` |  |
| cli · docs list --help | `cli.help-width` | `cli.help-size` |  |
| cli · docs search --help | `cli.help-width` | `cli.help-size` |  |
| cli · docs show --help | `cli.help-width` | `cli.help-size` |  |
| cli · docs sources --help | `cli.help-size` `cli.help-width` |  |  |
| cli · loop sync --help | `cli.help-width` | `cli.help-size` |  |
| cli · loop list --help | `cli.help-width` | `cli.help-size` |  |
| cli · loop search --help | `cli.help-width` | `cli.help-size` |  |
| cli · loop show --help | `cli.help-width` | `cli.help-size` |  |
| cli · loop sources --help | `cli.help-size` `cli.help-width` |  |  |
| cli · scout sync --help | `cli.help-width` | `cli.help-size` |  |
| cli · scout list --help | `cli.help-width` | `cli.help-size` |  |
| cli · scout search --help | `cli.help-width` | `cli.help-size` |  |
| cli · scout show --help | `cli.help-size` `cli.help-width` |  |  |
| cli · scout sources --help | `cli.help-size` `cli.help-width` |  |  |
| cli · drafts list --help | `cli.help-width` | `cli.help-size` |  |
| cli · drafts show --help | `cli.help-width` | `cli.help-size` |  |
| cli · content images --help | `cli.help-size` `cli.help-width` |  |  |
| cli · content capture --help | `cli.help-width` | `cli.help-size` |  |
| cli · content fetch image --help | `cli.help-width` | `cli.help-size` |  |
| cli · content fetch engage-thread --help | `cli.help-width` | `cli.help-size` |  |
| cli · content fetch engage-home --help | `cli.help-width` | `cli.help-size` |  |
| cli · content fetch page --help | `cli.help-width` | `cli.help-size` |  |
| cli · content fetch stream --help | `cli.help-width` | `cli.help-size` |  |
| cli · content fetch doc --help | `cli.help-width` | `cli.help-size` |  |
| cli · content signin --help | `cli.help-width` | `cli.help-size` |  |
| cli · content list --help | `cli.help-width` | `cli.help-size` |  |
| cli · content search --help | `cli.help-width` | `cli.help-size` |  |
| cli · content show --help | `cli.help-width` | `cli.help-size` |  |
| cli · content sources --help | `cli.help-width` | `cli.help-size` |  |
| cli · transcripts list --help | `cli.help-width` | `cli.help-size` |  |
| cli · transcripts show --help | `cli.help-size` `cli.help-width` |  |  |
| cli · transcripts fetch --help | `cli.help-width` | `cli.help-size` |  |
| cli · transcripts signin --help | `cli.help-width` | `cli.help-size` |  |
| cli · stores --help | `cli.help-size` `cli.help-width` |  |  |
| cli · records --help | `cli.help-size` `cli.help-width` |  |  |
| cli · unread --help | `cli.help-width` | `cli.help-size` |  |
| cli · thread --help | `cli.help-width` | `cli.help-size` |  |
| cli · watch --help | `cli.help-width` | `cli.help-size` |  |
| cli · whoami --help | `cli.help-size` `cli.help-width` |  |  |
| cli · sql --help | `cli.help-width` | `cli.help-size` |  |
| cli · skill --help | `cli.help-size` `cli.help-width` |  |  |
| cli · help --help | `cli.help-size` `cli.help-width` |  |  |
| cli · metadata --help | `cli.help-size` `cli.help-width` |  |  |
| cli · version --help | `cli.help-size` `cli.help-width` |  |  |
| cli · ax-xray-no-such-command | `cli.unknown-command` `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · edit | `cli.ambiguous-verbs` `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · delete | `cli.ambiguous-verbs` `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · remove | `cli.ambiguous-verbs` `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · overview --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · doctor --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · status --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · search --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · messages --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · conversations --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · teams --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · people list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · people show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · people sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · activity --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · coverage --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · calendar attendees sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · calendar attendees list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · calendar attendees show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · calendar agenda --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · calendar sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · calendar actions --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · calendar event --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · mail list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · mail show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · mail thread --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · mail folders --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · mail unread --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · workiq coverage --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · workiq status --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes status --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes target --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · notes list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · notes search --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · notes show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · notes sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · docs list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · docs search --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · docs show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · docs sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · loop list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · loop search --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · loop show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · loop sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · scout list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · scout search --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · scout show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · scout sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · drafts list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · drafts show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · content images --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · content list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · content search --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · content show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · content sources --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · transcripts list --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · transcripts show --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · stores --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · records --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · unread --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · thread --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · whoami --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · sql --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · skill --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · help --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · metadata --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · version --ax-xray-no-such-flag | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · 63 reads, their flag probes and the missing-argument probes | `cli.reads-stay-reads` |  |  |
| cli · watch <conversation> | `cli.wait-on-refs` |  |  |
| cli · watch <conversation> --json | `cli.wait-typed` |  |  |
| cli · writes send-teams-message (no --why) | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes send-teams-message (wrong ref) | `cli.error-teaches` | `cli.one-line-error` |  |
| cli · writes reply-mail (no --why) | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes reply-mail (wrong ref) | `cli.error-teaches` | `cli.one-line-error` |  |
| cli · writes reply-all-mail (no --why) | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes reply-all-mail (wrong ref) | `cli.error-teaches` | `cli.one-line-error` |  |
| cli · writes send-mail (no --why) | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes send-mail (wrong ref) | `cli.error-teaches` | `cli.one-line-error` |  |
| cli · writes add-reaction (no --why) | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes add-reaction (wrong ref) | `cli.error-teaches` | `cli.one-line-error` |  |
| cli · writes remove-reaction (no --why) | `cli.one-line-error` `cli.error-teaches` |  |  |
| cli · writes remove-reaction (wrong ref) | `cli.error-teaches` | `cli.one-line-error` |  |
| cli · events |  |  | `cli.work-events` |
| cli · the whole run | `cli.refusals-change-nothing` |  |  |
| cli · m365crawl skill --json |  | `cli.voice-off-in-json` |  |
| cli · help --json |  |  | `cli.voice-off-when-piped` |
| cli · mail |  | `cli.named-is-listed` |  |
| cli · people |  | `cli.named-is-listed` |  |
| cli · calendar |  | `cli.named-is-listed` |  |
| cli · workiq |  | `cli.named-is-listed` |  |
| cli · writes |  | `cli.named-is-listed` |  |
| cli · notes |  | `cli.named-is-listed` |  |
| cli · docs |  | `cli.named-is-listed` |  |
| cli · loop |  | `cli.named-is-listed` |  |
| cli · scout |  | `cli.named-is-listed` |  |
| cli · drafts |  | `cli.named-is-listed` |  |
| cli · content |  | `cli.named-is-listed` |  |
| cli · transcripts |  | `cli.named-is-listed` |  |
| cli · reads |  |  | `cli.check-verdict` |
| cli · all 255 calls | `cli.no-dev-commands` `copy.precise-failure` `cli.no-stack-trace` `cli.exit-codes` |  |  |
| cli · all 256 calls | `cli.plain-pipe` |  |  |
