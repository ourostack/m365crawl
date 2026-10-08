# Troubleshooting

Start with `m365crawl doctor`. It checks every prerequisite, and each failed check prints a `fix` you can follow. Every error m365crawl prints has the same shape, `{"error": {"code", "message", "fix"}}`, so the `fix` is usually the answer. This page covers the problems that need more than that. [SPEC.md](../SPEC.md) section 6.7 lists every error code.

## Setup

**`no_full_disk_access` (exit 3) on macOS.** The app that runs m365crawl lacks Full Disk Access. Grant it to the app the `fix` names, then quit and reopen that app; macOS applies the change only to new processes. If it works in Terminal but not from your agent, the agent's host app is a different app: run `m365crawl doctor` from inside the agent and follow its `fix`. [full-disk-access.md](full-disk-access.md) has the details.

**`teams_not_installed` or `no_teams_origin` (exit 3).** m365crawl reads the new Teams app's cache, which exists only after you install the new Teams and sign in once. Classic Teams is not supported.

**Outlook shows as `unavailable`.** The new Outlook for Mac is not installed, has no profile, or the app that runs m365crawl lacks Full Disk Access. The Teams sync carries on. `m365crawl doctor` (the `outlook_store` check) says which. Classic Outlook is not supported.

**`outlook_root_missing` (exit 3).** You named an Outlook directory with `--outlook-root` or `M365CRAWL_OUTLOOK_ROOT` that does not exist. Fix the path, or use `none` to turn Outlook off.

**Outlook is not read at all.** Outlook is off when `--teams-root` is set without `--outlook-root`, and when `--outlook-root none` is set. Name the Outlook directory to read it.

## Sync

**`locked` (exit 4).** Another `sync` or `watch` is using the archive. Wait for it to finish, or stop the other `watch`, and run again.

**`partial_sync` (exit 1).** Some sources synced and at least one failed. The report on stdout names each failed source and its code. What synced is already in the archive; fix the failing source (`m365crawl doctor` helps) and sync again.

**`snapshot_inconsistent` (exit 1).** Teams kept changing its cache during all three copy attempts. Run again; if Teams is busy syncing, wait a minute first.

**`archive_newer` (exit 3).** A newer m365crawl wrote the archive. Upgrade (`brew upgrade ourostack/tap/m365crawl`), or point `--db` at a different archive. Read commands still work.

**Outlook says `skipped_interval`.** Not a failure: m365crawl copies the Outlook store at most once every five minutes per profile and reused the last read.

## Mail

**`mail_unsupported_platform` (exit 3) on Windows.** m365crawl does not read Outlook mail on Windows yet. Teams chats and the calendar work: try `m365crawl calendar`.

**A mail list is empty.** Read its `note`. It says which cause applies: no archive yet, the Outlook source is off, no Outlook profile, mail not read yet (run `m365crawl sync`), the filters matched nothing, or the cache covers only mail since a date.

**Fewer unread messages than Outlook shows.** Outlook's local cache does not hold every message the server knows about, so counts from it are lower bounds. The result says how many messages the cache holds in the folder.

**Older mail is missing.** Outlook keeps a window of recent mail per folder. Mail you synced while it was in the cache stays in the archive and is marked `evicted` when Outlook drops it; list it with `mail list --include-evicted`. Mail that was never in the cache while you synced cannot be read.

**A message I deleted is still listed.** Outlook writes no deletion marker. m365crawl marks a message `gone` once two reads of the store in a row miss it, which can take a while after the deletion. A message moved to Deleted Items is not gone; its folder changes.

**`unknown_folder`.** The folder name or kind did not match. The error lists the folders; `m365crawl mail folders` lists them too.

**`bad_mail_id` or `mail_not_found`.** A mail id is `<account>:<number>`, exactly as `mail list` prints it, for example `outlook/Main:12345`.

**To and Cc are mixed together.** Outlook's local store does not tell them apart, so m365crawl lists all recipients without a To or Cc label.

## Search and reading

**An empty result.** Check `note` and `needs_sync`. `needs_sync: true` means the archive has no complete sync yet: run `m365crawl sync`. For the calendar, check `coverage_gap`: Teams caches only the days you looked at, so an event missing from an uncovered day is not evidence that there was none.

**`flag_source_conflict` from `search`.** The flag belongs to the other source, for example `--mentions-me` (Teams chats) with `--source mail`. Drop the flag or change `--source`. With the default `--source all`, such a flag narrows the search to its own source instead, and `note` says so.

**A field is missing from a calendar event.** A field the source never stated has no key, and its name is in `unknown_fields`. Teams fetches attendees, body and rooms only for meetings you opened.

**An Outlook profile's meetings are not merged with Teams.** The profile is not linked to your Teams account. The agenda's `unlinked_fix` prints the command that links it, for example `m365crawl sync --outlook-profile Main --outlook-account <tenantId>/<userId>`. `m365crawl whoami` lists the Teams accounts.

**The answer is stale.** `archive_age_seconds` says how old the archive is. Pass `--max-age 15m` to sync first when it is older, or run `m365crawl watch` in the background.

## Reporting a problem

Report a bug at [github.com/ourostack/m365crawl/issues](https://github.com/ourostack/m365crawl/issues) with the command you ran, the error's `code` and `message`, and the output of `m365crawl doctor`. For a crash, run the command again with `M365CRAWL_DEBUG=1` and include the stack trace. Never include message or mail content: `doctor` output and error codes are enough.
