# Releasing teamscrawl

A release is a merged release-notes file. Merge `docs/releases/vX.Y.Z.md` to `main` and the pipeline does the rest: it derives the version from the file name, creates the tag, signs, notarizes, publishes, verifies and pushes the Homebrew cask. Nobody pushes a tag, nothing is published by hand, and a release cannot ship unsigned: every Apple secret is required and every signing gate must pass, or the workflow fails.

## How to release

1. Add `## [X.Y.Z] - YYYY-MM-DD` to `CHANGELOG.md` (move the Unreleased entries under it). A stable release without that section is refused.
2. Add `docs/releases/vX.Y.Z.md` with the release notes. It must be the only notes file added by the push.
3. Merge to `main`. That push is the release. Follow it in the Actions tab under "Release".

Pushing a tag by hand starts nothing. To release, merge a notes file. To redo a release that failed after its tag existed, add notes for the next version (see "How to recover").

## How to rehearse

Merge `docs/releases/vX.Y.Z-rc.N.md` (for example `v0.2.0-rc.1.md`). A version with a hyphen is a rehearsal. It runs the same pipeline with the real signing secrets, the real notarization and the real tap push, but nothing reaches users:

- GitHub marks the release as a prerelease, so it is never "latest".
- `CHANGELOG.md` needs no section for it.
- `publish-homebrew` pushes the cask to the tap's `rehearsal` branch, never `main`, so `brew install` still serves the last stable release.
- `verify-homebrew` is skipped, because the cask is not on `main`.

The cask for a rehearsal is the one goreleaser renders for the rc tag. It is attached to the rc release like a stable one: its version is the rc version and its URLs point at the rc assets. That is the simplest honest cask, because it is exactly what the pipeline would publish, and `publish-cask.sh` checks it names the tag's version.

## How a release runs

`.github/workflows/release.yml` (name "Release") runs on a push to `main` that touches `docs/releases/*.md`. One release runs at a time; a second push waits.

1. `decide` runs `scripts/release-decide.sh` and outputs the tag, version, commit and whether it is a rehearsal. The commit is the one the push moved `main` to. If the push added no notes file (for example it edited an existing one), the run ends here and nothing is released. It refuses, with the reason in the log and an annotation, when more than one notes file was added, when the tag or a GitHub release already exists, when the name is not valid semantic versioning, or (stable only) when `CHANGELOG.md` has no section for the version.
2. `verify` runs `make check` on that commit.
3. `release` creates the tag at that commit with the workflow's token (a tag made this way starts no other workflow, which is why everything continues in this run). It then requires all six Apple secrets (`scripts/sign-notarize.sh --check-secrets`, which names any missing secret and never prints values) and runs goreleaser with `GORELEASER_CURRENT_TAG` set to the derived tag. goreleaser's post-build hook (`scripts/sign-notarize.sh`) signs, notarizes and gates each darwin binary (arm64 and amd64) before it is archived. The notes always say the binaries are signed and notarized. It attaches `teamscrawl.rb` (the Homebrew cask) to the release.
4. `verify-release` downloads the published tarballs and `checksums.txt` and checks them as a user would receive them (`scripts/verify-release.sh`).
5. `publish-homebrew` pushes the cask to the tap, ourostack/homebrew-tap, as `teamscrawl <version>` (`scripts/publish-cask.sh`): to `main` for a stable release, to the `rehearsal` branch for a rehearsal. It runs only after `verify-release` passes, so a release with bad artifacts never reaches the tap. It uses the `HOMEBREW_TAP_DEPLOY_KEY` secret, a deploy key that can write to the tap and nothing else.
6. `verify-homebrew` (stable only) installs the cask from the tap and checks the installed binary (`scripts/verify-homebrew.sh`).
7. `contain` runs only when `verify-release` failed: it marks that GitHub release as a prerelease (and not latest) so users and `brew` do not resolve it as the newest.
8. `report-failure` runs when any job above failed: it opens an issue titled "Workflow failed: Release", or comments on the open one with that title, with the workflow name, the version and the run link.

Each job has the narrowest `permissions` it needs: `contents: write` only in `release` and `contain`, `issues: write` only in `report-failure`.

## Credential health

`.github/workflows/credential-health.yml` ("Credential health") runs weekly (Monday 06:17 UTC) and on manual start from the Actions tab. It checks that every Apple secret is set (`scripts/sign-notarize.sh --check-secrets`, no values printed) and that the tap deploy key still authenticates (`scripts/check-tap-key.sh`: `git ls-remote` against the tap over SSH with the key in a private temporary file, never echoed). It cannot check that the Apple credentials are still accepted by Apple; only a release or rehearsal proves that. A failure opens or updates the issue "Workflow failed: Credential health".

## Dependency updates

`.github/dependabot.yml` opens weekly, grouped pull requests for GitHub Actions and Go modules. `.github/workflows/dependabot-automerge.yml` turns on auto-merge (squash) for them, so they merge when the required checks pass. Pinned actions keep their commit SHA and version comment; Dependabot updates both.

## Secrets

Repository secrets on ourostack/teamscrawl:

| Secret | Used by |
| --- | --- |
| `APPLE_DEVELOPER_ID_CERTIFICATE_BASE64`, `APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD`, `APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY`, `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD`, `APPLE_TEAM_ID` | `release` (signing and notarization), `verify-release` and `verify-homebrew` (`APPLE_TEAM_ID`), credential health |
| `HOMEBREW_TAP_DEPLOY_KEY` | `publish-homebrew`, credential health. A write deploy key on ourostack/homebrew-tap |

The workflow token (`GITHUB_TOKEN`) needs these repository settings: it may create tags and releases (no ruleset blocks creating `v*` tags for it), issues are enabled, and "Allow auto-merge" is on for the Dependabot workflow.

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

`verify-homebrew` waits up to 10 minutes for `brew info --cask --json=v2 ourostack/tap/teamscrawl` to report the tag's version (the cask is already pushed, so this only covers GitHub serving the new commit), runs `brew install --cask ourostack/tap/teamscrawl`, checks `teamscrawl --json version` equals the tag, and runs the codesign and spctl gates on the installed binary.

The gate logic is tested on every PR: `make lint` runs `scripts/sign-notarize.sh --selftest`, which drives every gate with stubbed `codesign`, `spctl`, `xattr` and `xcrun`. The release scripts have selftests too (see "What is tested before a release").

## What is tested before a release

`make lint` runs on every pull request and runs a `--selftest` for each release script, with a stand-in `gh` and throwaway local git repositories:

- `scripts/release-decide.sh --selftest`: a stable and a rehearsal version, a first push, and every refusal (two notes files, existing tag, existing release, invalid version, missing changelog section).
- `scripts/publish-cask.sh --selftest`: the first publish pushes one commit, a repeat changes nothing, a cask for another version is refused, a prerelease is refused on `main`, a rehearsal pushes only to its branch, a missing input is named.
- `scripts/check-tap-key.sh --selftest` and `scripts/report-failure.sh --selftest`.
- `scripts/sign-notarize.sh --selftest`: every signing gate.

What a pull request cannot prove: the workflow wiring, the token's ability to create the tag, the SSH deploy key, Apple signing and the real tap. The first rehearsal exercises all of them.

## What a failure means

Every failed job also opens or updates an issue (see `report-failure`).

- `release refused: ...` in `decide`: the push was ambiguous or wrong and nothing was done. The message says which rule. Fix the notes file or changelog and merge again.
- `missing Apple signing secrets: NAME`: a repository secret is unset or empty. Set it on ourostack/teamscrawl and run the failed jobs again. Nothing was published; the tag may exist.
- `gate ...` failure in `release`: the binary built in CI is not correctly signed or notarized. Nothing was published; read the notary log printed above the failure. A `gate spctl` failure after all retries usually means notarization was not accepted or Apple's service is down.
- `notarization was not accepted`: Apple rejected the submission; the log is printed.
- Failure in `verify-release`: the release is already published but a downloaded artifact is wrong. The pipeline already marked the release as a prerelease so it is not "latest" (`contain`). Treat the release as bad: fix the cause and release a new version.
- Failure in `publish-homebrew`: the release is published and verified but the tap still serves the previous version. `HOMEBREW_TAP_DEPLOY_KEY is required` means the secret is unset; a push failure means the deploy key was removed from the tap or deploy keys were turned off for the organization. Fix the cause and re-run the job; it is safe to run twice.
- `verify-homebrew` timeout: the tap does not serve the version 10 minutes after the push. Check the tap's `main` for the `teamscrawl <version>` commit, then re-run the job.
- A credential-health failure names the secret that is missing or says the tap key no longer authenticates. Fix it before the next release.

## How to recover

- A failure in `verify` or before the tag exists (for example `decide` refused): fix the cause and merge again; no tag was made.
- A failure after the tag exists, before publish: open the failed run in the Actions tab and choose "Re-run failed jobs". The run keeps its derived tag and commit, and the tag step accepts a tag that already points at that commit. A brand new push for the same version is refused because the tag exists.
- `verify-release` or `verify-homebrew` flaked: re-run just that job from the workflow run page. They only read the published release and the tap. (If `contain` already marked a stable release as a prerelease, `verify-release` fails the flag check on a re-run: publish a new version instead.)
- A bad release: delete the GitHub release and the tag in the repository's settings only if nothing was published to the tap, then merge notes for the next version. Versions are not reused.
- Run the gates by hand on a binary: `APPLE_TEAM_ID=... scripts/sign-notarize.sh --verify ./teamscrawl`.

A prerelease and a release may point at the same commit (for example `v0.1.0-rc.4` and `v0.1.0`). The release job sets `GORELEASER_CURRENT_TAG` to the tag derived for the run, so goreleaser always builds that tag rather than whichever tag `git describe` returns.
