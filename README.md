# teamscrawl 🟣 — Your Teams, readable by your agents.

[![CI](https://img.shields.io/github/actions/workflow/status/ourostack/teamscrawl/ci.yml?branch=main&style=flat-square&label=ci)](https://github.com/ourostack/teamscrawl/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ourostack/teamscrawl?include_prereleases&style=flat-square)](https://github.com/ourostack/teamscrawl/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/ourostack/teamscrawl?style=flat-square)](https://go.dev/)
[![Platform](https://img.shields.io/badge/platform-macOS-lightgrey?style=flat-square)](https://github.com/ourostack/teamscrawl/releases)
[![License](https://img.shields.io/github/license/ourostack/teamscrawl?style=flat-square)](LICENSE)
[![Homebrew](https://img.shields.io/badge/homebrew-ourostack%2Ftap-FBB040?style=flat-square&logo=homebrew&logoColor=black)](https://github.com/ourostack/homebrew-tap)

`teamscrawl` mirrors the Microsoft Teams desktop app's local cache into a SQLite archive on your Mac, with full-text search, unread state, mentions and the activity feed, so an AI agent can read your Teams history in milliseconds, offline and read-only. It reads the local cache of the signed-in desktop app. It never talks to the Teams service, never reads your Teams credentials and never writes to Teams' storage.

<p align="center"><img src="screenshot.png" alt="teamscrawl doctor output" width="801"></p>

## Why not a Teams MCP server or the Graph API?

- **No tokens, app registration or admin consent.** Graph needs an Entra app, delegated scopes and often a tenant admin to approve them. teamscrawl needs a signed-in Teams app and one macOS permission.
- **No network, no rate limits.** A search over 50,000 messages returns in about 100 ms from local SQLite. No paging, no throttling, no round trips.
- **Works offline and under conditional access.** Device-compliance and location policies gate API tokens, not a file on your disk.
- **Read-only by construction.** There is no write path in the code. It cannot post, react or mark anything read, so handing it to an agent is safe.
- **Keeps what Teams evicts.** Teams trims its cache as it runs, and its cached message count can drop by thousands between two reads. The archive never deletes a message, so history survives as long as you sync regularly.
- **Full-text search and SQL over everything.** Messages, the activity feed, unread state and mentions sit in one database that an agent can query with FTS5 or plain SQL.

**When you want something else.** Use the Graph API or a Teams MCP server if you need to send or react, need data the desktop app never cached (old history you never scrolled to, other people's chats), run Teams on anything but macOS new Teams, or cannot grant Full Disk Access to your terminal or agent host.

## Install

Homebrew is the shortest path:

```sh
brew install ourostack/tap/teamscrawl
```

Alpha builds are unsigned. The Homebrew cask clears the macOS quarantine flag for you. If you download a binary by hand, run `xattr -dr com.apple.quarantine teamscrawl` once.

[GitHub Releases](https://github.com/ourostack/teamscrawl/releases) has `teamscrawl_<version>_darwin_arm64.tar.gz` and `teamscrawl_<version>_darwin_amd64.tar.gz` with a `checksums.txt`. To build from source, install Go 1.27 or newer:

```sh
go install github.com/ourostack/teamscrawl/cmd/teamscrawl@latest
```

### Grant Full Disk Access

macOS protects Teams' container, so the app that runs teamscrawl needs Full Disk Access: open System Settings > Privacy & Security > Full Disk Access, turn it on for your terminal (or the agent host app that launches teamscrawl), then quit and reopen that app. Verify with:

```sh
teamscrawl doctor
```

`doctor` checks every prerequisite and prints the exact app to grant when access is missing. It exits 3 if a required check fails.

## Quick start

The examples below run against the repository's committed test fixture, so every name and message is synthetic.

```sh
teamscrawl doctor
teamscrawl sync
teamscrawl search planning
teamscrawl unread --limit 5
teamscrawl activity --unread
teamscrawl thread 19:topicchannel1@thread.tacv2 1700000045000
```

Output is JSON when stdout is not a terminal and readable text on a terminal. Force either with `--format json|text`.

```sh
teamscrawl sync --json
```

```json
{"status":"ok","sources":[{"source":"WV2Profile_fixture|https_teams.microsoft.com_0","status":"ok"}],"conversations":{"seen":14,"inserted":14,"updated":0,"unchanged":0},"messages":{"seen":104,"inserted":104,"updated":0,"unchanged":0},"people":{"seen":6,"inserted":6,"updated":0,"unchanged":0},"activity":{"seen":16,"inserted":16,"updated":0,"unchanged":0},"omissions":{},"other_origins":[],"started_at":"2026-09-30T22:23:43.002609Z","finished_at":"2026-09-30T22:23:43.189474Z"}
```

```sh
teamscrawl search planning --limit 1
```

```json
{"items":[{"tenant_id":"00000000-0000-4000-8000-000000000001","user_id":"00000000-0000-4000-8000-0000000000a1","conversation_id":"19:planningchannel1@thread.tacv2","conversation_display_name":"Fixture team 1 › Planning","id":"1700000047000","reply_chain_id":"1700000047000","parent_message_id":"1700000047000","sender_id":"8:orgid:00000000-0000-4000-8000-0000000000ee","sender_name":"Pat Example","sent_at":"2023-11-14T22:14:07Z","message_type":"RichText/Html","text":"Planning channel message","mentions_me":false,"importance":"normal","pinned":false,"link":"https://teams.microsoft.com/l/message/19:planningchannel1@thread.tacv2/1700000047000?tenantId=00000000-0000-4000-8000-000000000001&context=%7B%22contextType%22%3A%22channel%22%7D"}],"count":1,"truncated":false,"archive_age_seconds":8}
```

```sh
teamscrawl activity --unread --limit 1 --fields at,type,conversation_display_name,text
```

```json
{"items":[{"at":"2023-11-14T22:22:00Z","type":"follow","conversation_display_name":"Fixture team 2 › Planning","text":"Planning channel message"}],"count":1,"truncated":true,"archive_age_seconds":0}
```

`--account <tenantId>/<userId>` limits any command to one signed-in account; `teamscrawl whoami` lists them. The archive lives at `~/.teamscrawl/teamscrawl.db` (override with `--db` or `TEAMSCRAWL_DB`).

## For agents

Agents should read [`.agents/skills/teamscrawl/SKILL.md`](.agents/skills/teamscrawl/SKILL.md). It holds the workflow, every command, every error code and what to do about each. The contract in five bullets:

- Results go to stdout, progress and warnings to stderr. In JSON mode each command prints exactly one document.
- Lists are `{"items": [...], "count": N, "truncated": bool}` with `--limit` (default 50). Keys are snake_case and stable; new fields may appear, renames are breaking changes.
- Errors are `{"error": {"code", "message", "fix"}}` on stderr, and `fix` is an instruction you can follow. Exit codes: 0 success, 1 runtime failure, 2 usage, 3 environment not ready, 4 another run holds the lock.
- Every item carries a `link` (a Teams deep link) for citing, and every read result carries `archive_age_seconds`.
- Nothing ever writes to Teams.

Flags that matter for agents:

- `--max-age 15m` (or `TEAMSCRAWL_MAX_AGE`) makes a read command run a sync first when the last successful one is older. `0` disables it. If the implicit sync fails, the command still answers from the archive, warns on stderr and adds a `sync_error` field.
- `--fields a,b,c` keeps only those top-level keys of each item.
- `--max-text N` truncates each item's text to N characters and sets `text_truncated`.
- `archive_age_seconds` tells you how stale the answer can be.

## Commands

| Command | What it does |
| --- | --- |
| `doctor` | Checks Teams, Full Disk Access, origins, the archive and the last sync. Exits 3 on a blocking failure. |
| `whoami` | Lists the accounts in the archive and the archive's state. |
| `sync` | Copies the Teams cache into the archive once and prints what changed. |
| `status` | Shows archive counts per account, the last sync and other Teams origins seen. |
| `search <query>` | Full-text search over message text, newest first. Filters: `--conversation`, `--from`, `--since`, `--until`, `--mentions-me`, `--include-deleted`. |
| `messages` | Lists messages chronologically. Same filters as `search`, plus `--unread`. |
| `unread` | Lists unread messages, newest first. |
| `activity` | Lists activity-feed items (mentions, replies, reactions) joined with their messages. Filters: `--unread`, `--type`, `--since`. |
| `thread <conversation> <root-id>` | Shows one thread. Accepts a Teams message link instead of the two arguments. |
| `conversations` | Lists conversations by latest activity. Filters: `--kind`, `--query`. |
| `people` | Lists people seen as senders or members; use it to resolve `--from`. |
| `sql <query>` | Runs one read-only SELECT against the archive. |
| `version` | Prints the build version. |

Every command takes the global flags `--format`, `--json`, `--db`, `--teams-root`, `--account`, `--no-color`, `--max-age`, `--fields` and `--max-text`. Run `teamscrawl <command> --help` for the rest.

## How it works

1. **Snapshot.** teamscrawl copies the new Teams app's IndexedDB (Chromium LevelDB plus blob files) into a private 0700 temp directory, retrying if Teams writes mid-copy. The copy contains Teams' sign-in database, so it is removed on every exit path, including failure and Ctrl-C.
2. **Decode.** A built-in reader parses LevelDB, Chromium's IndexedDB coding and V8's structured-clone format. No Node, Python or browser is needed at runtime.
3. **Allowlist.** Only the conversation, reply-chain (message) and activity-feed stores are decoded. Everything else, including sign-in data, is listed by name and never read.
4. **Store.** Rows go into SQLite (WAL) with FTS5 indexes, using idempotent upserts. A second sync with no new Teams activity changes nothing. Messages that vanish from Teams' cache stay in the archive; Teams deletions set `deleted_at`.

Full Disk Access is the only permission it asks for. If the cache fingerprint has not changed since the last sync, `sync` skips decoding.

## Privacy

The archive holds your real Teams conversations. It stays on your Mac in `~/.teamscrawl/` (directory 0700, database 0600) and teamscrawl has no network code. Treat the database like the chats it contains: do not commit it, sync it to shared storage or paste it into tools you would not show the original messages to. Tests and this README use only synthetic fixture data.

## Limits

- It sees only what the desktop app has cached. History you never scrolled to may be missing, and Teams evicts old messages from its cache, so sync regularly.
- macOS and the new Teams app (`com.microsoft.teams2`) only. Classic Teams, Windows and Linux are not supported.
- Full Disk Access is required for the app that runs it.
- Read-only: no sending, reacting or marking read.
- Attachments and media are not downloaded; files and links are recorded as metadata.
- Teams can change its storage layout. When it does, `sync` fails with a named error or reports counted omissions instead of guessing.
- Alpha builds are unsigned.

## Development

```sh
make build      # bin/teamscrawl
make test       # unit tests with the race detector
make e2e        # end-to-end tests against the committed fixture
make check      # every gate CI runs: tidy, fmt, vet, lint, test, e2e
```

The integration tests run against `testdata/teams-fixture/`, an IndexedDB cache written by a real Microsoft Edge through `scripts/fixture/`. Regenerate it with `make fixture` (needs Node and Edge) and V8 test vectors with `make v8vectors` (needs Node 22). Real-cache acceptance runs locally only (`TEAMSCRAWL_REAL_CACHE=1`, needs Full Disk Access); its results are recorded as counts and pass/fail, never content. See [`AGENTS.md`](AGENTS.md) for the development rules.

## Credits

- [slacrawl](https://github.com/openclaw/slacrawl) (openclaw, MIT) is the model for this tool's design and commands, and the source of the terminal renderer.
- [ccl_chromium_reader](https://github.com/cclgroupltd/ccl_chromium_reader) (MIT) documented the Chromium IndexedDB and V8 formats and served as the reference decoder.

## License

MIT. See [LICENSE](LICENSE).
