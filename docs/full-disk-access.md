# Full Disk Access (macOS only)

On macOS, m365crawl needs one permission: Full Disk Access for the app that runs it, such as your terminal or your agent's host app. One grant covers Teams, Outlook mail and the Outlook calendar. Without it, `m365crawl sync` and `m365crawl doctor` fail with `no_full_disk_access` (exit 3), and the `fix` names the app to grant.

Windows has no such step. Teams keeps its cache under `%LOCALAPPDATA%\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView`, so `m365crawl doctor` reports the `full_disk_access` check as `ok: true` with detail `not applicable on Windows; Teams cache is under LocalCache, not TCC-protected.`

## Why it is needed

The new Teams app keeps its data in an app container, `~/Library/Containers/com.microsoft.teams2`. macOS protects that location with its privacy system (TCC). A process may list and read it only when the app that started the process has Full Disk Access. If it does not, the operating system refuses with `EPERM` on the first directory read.

The permission belongs to the app that started the process, not to m365crawl itself. If you run `m365crawl` in Terminal, Terminal needs it. If an agent host such as an editor, a desktop agent app or a launcher starts m365crawl, that host needs it.

m365crawl uses the permission only to read:

- the Teams cache;
- the new Outlook for Mac store, `HxStore.hxd`, which holds mail and the calendar (Outlook keeps it in a TCC-protected group container under `~/Library/Group Containers`, and the same grant covers it);
- the message bodies Outlook has downloaded, which are separate files under the profile's `Files` directory, opened read-only and only for messages the sync reads.

It never writes to any of them. Without the grant a default Outlook is not a failure: the sync reports the Outlook source as `unavailable` with the fix, and the Teams sync carries on.

## How to grant it

1. Run `m365crawl doctor`. If access is missing, the `full_disk_access` check fails and its `fix` names the app, for example `turn it on for Terminal`.
2. Open System Settings > Privacy & Security > Full Disk Access.
3. Turn the switch on for that app. If it is not in the list, press the plus button and add it from `/Applications` (or `/System/Applications/Utilities` for Terminal).
4. Quit that app completely and reopen it. macOS applies the change to new processes only.
5. Run `m365crawl doctor` again. Success is `full_disk_access` showing `ok` and the command exiting 0.

The permission is granted once per app. You do not need to grant it again after upgrading m365crawl.

## How `doctor` checks it

`doctor` makes the same first read that a sync makes. It lists the Teams data directory. The result decides three checks in order (the Outlook store has its own check, `outlook_store`, which warns when access is denied):

| Check | Passes when |
| --- | --- |
| `teams_installed` | The Teams data directory exists. |
| `full_disk_access` | Listing the directory and each profile's `IndexedDB` directory is allowed. It reports `not checked` when Teams is not installed. |
| `teams_origin` | A Teams IndexedDB origin exists in a profile. |

A refused read turns `full_disk_access` into a failure and `doctor` ends with exit 3 and the code `doctor_failed`. A sync that is refused ends with `no_full_disk_access` (exit 3). Both name the same fix.

## Which app does the fix name?

The fix names the app macOS will check. m365crawl finds it by walking up the process tree from its own process to the first process whose command path sits inside a `.app` bundle, for example `Visual Studio Code` or `Terminal`. If the tree has no such process, it uses the terminal named by the `TERM_PROGRAM` environment variable (`Terminal`, `iTerm`, `Visual Studio Code`, `Ghostty`, or the raw value). If neither exists, for example under `launchd`, the message says so in general terms: grant the app that runs m365crawl, or the m365crawl program itself.

## Troubleshooting

- **`doctor` still fails after granting.** You did not restart the app. Quit it from its menu (not just close the window), reopen it and try again.
- **It works in Terminal but not from your agent.** The agent's host app is a different app. Run `m365crawl doctor` from inside the agent and follow the `fix` it prints.
- **A background job fails.** A job started by `launchd` or cron has no `.app` parent. Grant Full Disk Access to the program that starts the job, or run m365crawl from an app that already has it.
- **Safe to leave on?** Full Disk Access is broad for the app you grant it to. Grant it to the apps you already trust with your files.
