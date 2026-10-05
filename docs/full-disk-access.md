# Full Disk Access (macOS only)

This page describes the macOS-only permission step. On Windows, Teams stores its cache under `%LOCALAPPDATA%\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView`, so `teamscrawl doctor` reports the `full_disk_access` check as `ok: true` with detail `not applicable on Windows; Teams cache is under LocalCache, not TCC-protected.`

teamscrawl needs one macOS permission: Full Disk Access for the app that runs it. Without it, `teamscrawl sync` and `teamscrawl doctor` fail with `no_full_disk_access` (exit 3), and the `fix` names the app to grant.

## Why it is needed

The new Teams app keeps its data in an app container, `~/Library/Containers/com.microsoft.teams2`. macOS protects that location with its privacy system (TCC). A process may list and read it only when the app that started the process has Full Disk Access. If it does not, the operating system refuses with `EPERM` on the first directory read.

The permission belongs to the app that started the process, not to teamscrawl itself. If you run `teamscrawl` in Terminal, Terminal needs it. If an agent host such as an editor, a desktop agent app or a launcher starts teamscrawl, that host needs it.

teamscrawl uses the permission only to read the Teams cache. It never writes to the container.

## How to grant it

1. Run `teamscrawl doctor`. If access is missing, the `full_disk_access` check fails and its `fix` names the app, for example `turn it on for Terminal`.
2. Open System Settings > Privacy & Security > Full Disk Access.
3. Turn the switch on for that app. If it is not in the list, press the plus button and add it from `/Applications` (or `/System/Applications/Utilities` for Terminal).
4. Quit that app completely and reopen it. macOS applies the change to new processes only.
5. Run `teamscrawl doctor` again. Success is `full_disk_access` showing `ok` and the command exiting 0.

The permission is granted once per app. You do not need to grant it again after upgrading teamscrawl.

## How `doctor` checks it

`doctor` makes the same first read that a sync makes. It lists the Teams data directory. The result decides three checks in order:

| Check | Passes when |
| --- | --- |
| `teams_installed` | The Teams data directory exists. |
| `full_disk_access` | Listing the directory and each profile's `IndexedDB` directory is allowed. It reports `not checked` when Teams is not installed. |
| `teams_origin` | A Teams IndexedDB origin exists in a profile. |

A refused read turns `full_disk_access` into a failure and `doctor` ends with exit 3 and the code `doctor_failed`. A sync that is refused ends with `no_full_disk_access` (exit 3). Both name the same fix.

## Which app does the fix name?

The fix names the app macOS will check. teamscrawl finds it by walking up the process tree from its own process to the first process whose command path sits inside a `.app` bundle, for example `Visual Studio Code` or `Terminal`. If the tree has no such process, it uses the terminal named by the `TERM_PROGRAM` environment variable (`Terminal`, `iTerm`, `Visual Studio Code`, `Ghostty`, or the raw value). If neither exists, for example under `launchd`, the message says so in general terms: grant the app that runs teamscrawl, or the teamscrawl program itself.

## Troubleshooting

- **`doctor` still fails after granting.** You did not restart the app. Quit it from its menu (not just close the window), reopen it and try again.
- **It works in Terminal but not from your agent.** The agent's host app is a different app. Run `teamscrawl doctor` from inside the agent and follow the `fix` it prints.
- **A background job fails.** A job started by `launchd` or cron has no `.app` parent. Grant Full Disk Access to the program that starts the job, or run teamscrawl from an app that already has it.
- **Safe to leave on?** Full Disk Access is broad for the app you grant it to. Grant it to the apps you already trust with your files.
