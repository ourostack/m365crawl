# Releasing teamscrawl

A release is a pushed tag. Nothing is published by hand, and a release cannot ship unsigned: every Apple secret is required and every signing gate must pass, or the workflow fails.

## How a release runs

Pushing a tag `v*` starts `.github/workflows/release.yml`:

1. `verify` runs `make check` on the tagged commit.
2. `release` first requires all six Apple secrets (`scripts/sign-notarize.sh --check-secrets`, which names any missing secret and never prints values). It then runs goreleaser, whose post-build hook (`scripts/sign-notarize.sh`) signs, notarizes and gates each darwin binary (arm64 and amd64) before it is archived. The notes always say the binaries are signed and notarized. For a stable tag it also attaches `teamscrawl.rb` (the Homebrew cask) to the release.
3. `verify-release` downloads the published tarballs and `checksums.txt` and checks them as a user would receive them (`scripts/verify-release.sh`).
4. `verify-homebrew` (stable tags only) waits for the tap, installs the cask and checks the installed binary (`scripts/verify-homebrew.sh`).

Tags with a hyphen, such as `v0.1.0-rc.2`, are prereleases: GitHub marks them as prereleases, they never become "latest", they attach no cask (so the tap's `update-casks` finds nothing to pull) and `verify-homebrew` is skipped. Use `v0.1.0-rc.N` to rehearse the whole pipeline before a stable tag.

## The gates

Each darwin binary must pass all of these, at signing time and again on the downloaded copy:

| Gate | What it checks |
| --- | --- |
| `codesign` | `codesign --verify --strict` succeeds |
| `team` | `TeamIdentifier` equals `APPLE_TEAM_ID` |
| `authority` | signed by a `Developer ID Application` certificate |
| `runtime` | hardened runtime flag (`flags=0x10000(runtime)`) |
| `timestamp` | a secure timestamp is present |
| notarization | at signing time, `notarytool` final status is exactly `Accepted`; on a downloaded binary, Apple's own verdict below |
| `spctl` | `spctl --assess --type install -vv` reports `accepted` and `source=Notarized Developer ID` (a bare CLI binary must be assessed as `install`: `--type exec` rejects every non-app binary); retried with 15, 30, 60 and 90 second backoff because Gatekeeper's online lookup can lag notarization |

`verify-release` also checks that the release is a prerelease exactly when the tag has a hyphen and is never "latest" when it is one; that every tarball matches `checksums.txt`; that `file` and `lipo` report the right architecture per tarball; that Gatekeeper still accepts each binary after a `com.apple.quarantine` attribute is added (simulating a browser download); and that the arm64 binary's `teamscrawl --json version` reports the tag without the leading `v` and the tagged commit.

`verify-homebrew` waits up to 75 minutes for `brew info --cask --json=v2 ourostack/tap/teamscrawl` to report the tag's version (the tap's `update-casks` runs hourly and this repo cannot trigger it), runs `brew install --cask ourostack/tap/teamscrawl`, checks `teamscrawl --json version` equals the tag, and runs the codesign and spctl gates on the installed binary.

The gate logic is tested on every PR: `make lint` runs `scripts/sign-notarize.sh --selftest`, which drives every gate with stubbed `codesign`, `spctl`, `xattr` and `xcrun`.

## What a failure means

- `missing Apple signing secrets: NAME`: a repository secret is unset or empty. Set it on ourostack/teamscrawl and re-run. Nothing was published.
- `gate ...` failure in `release`: the binary built in CI is not correctly signed or notarized. Nothing was published; read the notary log printed above the failure. A `gate spctl` failure after all retries usually means notarization was not accepted or Apple's service is down.
- `notarization was not accepted`: Apple rejected the submission; the log is printed.
- Failure in `verify-release`: the release is already published but a downloaded artifact is wrong. Treat the release as bad: delete or mark it as a prerelease, fix the cause and publish a new tag. Do not leave a failing release as "latest".
- `verify-homebrew` timeout: the tap did not pick up the cask within 75 minutes. Check the tap's `update-casks` run, then re-run the job.

## How to re-run

- A failure before publish: fix the cause, then either re-run the failed workflow run in the Actions tab (the tag still points at the same commit), or delete the tag and push a corrected one.
- `verify-release` or `verify-homebrew` flaked: re-run just that job from the workflow run page. They only read the published release and the tap.
- Run the gates by hand on a binary: `APPLE_TEAM_ID=... scripts/sign-notarize.sh --verify ./teamscrawl`.

A prerelease and a release may point at the same commit (for example `v0.1.0-rc.4` and `v0.1.0`). The release job sets `GORELEASER_CURRENT_TAG` to the tag that triggered the run, so goreleaser always builds that tag rather than whichever tag `git describe` returns.
