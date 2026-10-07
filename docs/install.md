# Install, upgrade and remove

Install m365crawl with Homebrew on macOS or from the zip on Windows, run `m365crawl doctor`, then `m365crawl sync`. This page covers each step, plus verifying a download, upgrading and removing m365crawl.

## macOS

### 1. Install

```sh
brew install ourostack/tap/m365crawl
```

Published macOS binaries are always Developer ID signed and notarized by Apple; the release workflow fails rather than ship an unsigned build. The Homebrew cask clears the quarantine flag after install.

Without Homebrew, download `m365crawl_<version>_darwin_arm64.tar.gz` (Apple silicon) or `m365crawl_<version>_darwin_amd64.tar.gz` (Intel) and `checksums.txt` from [GitHub Releases](https://github.com/ourostack/m365crawl/releases). Check the download, then unpack it:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf m365crawl_<version>_darwin_arm64.tar.gz
```

To build from source, install Go 1.27 or newer:

```sh
go install github.com/ourostack/m365crawl/cmd/m365crawl@latest
```

A binary you built yourself is not notarized, so you may need `xattr -dr com.apple.quarantine m365crawl` once.

### 2. Grant Full Disk Access

macOS protects the Teams and Outlook containers, so the app that runs m365crawl needs Full Disk Access. Open System Settings › Privacy & Security › Full Disk Access, turn it on for your terminal (or the agent host app that starts m365crawl), then quit and reopen that app. [full-disk-access.md](full-disk-access.md) explains why and covers background jobs.

### 3. Check and sync

```sh
m365crawl doctor
m365crawl sync
m365crawl
```

`doctor` exits 0 when everything required is ready; a failed check prints the fix. `sync` reads Teams chats, Outlook mail and the calendar into the archive. `m365crawl` with no arguments then shows what the archive holds.

You need the new Teams app, and for mail and the Outlook calendar the new Outlook for Mac, each signed in at least once.

## Windows

### 1. Install

Download `m365crawl_<version>_windows_amd64.zip` (most PCs) or `m365crawl_<version>_windows_arm64.zip` (Arm PCs) and `checksums.txt` from [GitHub Releases](https://github.com/ourostack/m365crawl/releases). The Windows assets are intentionally unsigned, so check the download against `checksums.txt` before you unzip it:

```powershell
(Get-FileHash .\m365crawl_<version>_windows_amd64.zip -Algorithm SHA256).Hash.ToLower()
Select-String 'windows_amd64.zip' .\checksums.txt
```

The two hashes must match. Unzip the archive somewhere under your user profile and add that directory to `PATH` if you want to call `m365crawl.exe` without the full path. Then confirm the version:

```powershell
m365crawl.exe --json version
```

### 2. Check and sync

```powershell
m365crawl.exe doctor
m365crawl.exe sync
m365crawl.exe
```

Windows needs no extra permission step. On Windows, m365crawl reads Teams chats and the Teams calendar. Outlook mail on Windows is coming next; until then every `mail` command returns the error `mail_unsupported_platform`, which names a command that works.

## Where m365crawl keeps its data

| | macOS | Windows |
| --- | --- | --- |
| Archive | `~/.m365crawl/m365crawl.db` | `%LOCALAPPDATA%\m365crawl\m365crawl.db` |
| Override | `--db PATH` or `M365CRAWL_DB` | `--db PATH` or `M365CRAWL_DB` |

The archive directory is private to your user. [privacy.md](privacy.md) says what the archive holds.

## Keeping the archive current

The apps evict old data from their caches, so sync regularly. Three ways:

- Run `m365crawl sync` yourself, or from a scheduled job (on macOS the job's parent app needs Full Disk Access).
- Pass `--max-age 15m` (or set `M365CRAWL_MAX_AGE`) so a read command syncs first when the archive is older than that.
- Run `m365crawl watch` in the background to sync as Teams writes its cache.

## Upgrading

```sh
brew upgrade ourostack/tap/m365crawl
```

On Windows, download the new zip and replace `m365crawl.exe`. The first sync after an upgrade upgrades the archive in place; nothing needs to be re-synced. An older m365crawl refuses to write to an archive a newer one upgraded (`archive_newer`, exit 3), so upgrade every copy that shares an archive.

## Removing m365crawl

macOS:

```sh
brew uninstall ourostack/tap/m365crawl
```

Then delete the `~/.m365crawl` directory to remove the archive, and turn off Full Disk Access for any app you granted it only for m365crawl.

Windows: delete `m365crawl.exe` and the `%LOCALAPPDATA%\m365crawl` directory.

m365crawl never wrote to Teams or Outlook, so there is nothing to undo in either app.
