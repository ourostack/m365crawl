# Releasing teamscrawl

A release is a merged release-notes file. Merge `docs/releases/vX.Y.Z.md` to `main` and the pipeline does the rest: it derives the version from the file name, creates the tag, signs, notarizes, publishes, verifies and pushes the Homebrew cask. Nobody pushes a tag, nothing is published by hand, and a release cannot ship unsigned: every Apple secret is required and every signing gate must pass, or the workflow fails.

## How to release

1. Add `## [X.Y.Z] - YYYY-MM-DD` to `CHANGELOG.md` (move the Unreleased entries under it). A stable release without that section is refused.
2. Add `docs/releases/vX.Y.Z.md` with the release notes.
3. Merge to `main`. Follow the run in the Actions tab under "Release".

The release comes from the state of `main`, not from the push: the pipeline looks at every `docs/releases/v*.md` on `main`, and releases the lowest version whose release is not finished. So a refused or failed release is retried by any later run: a fix-up merge that touches `docs/releases/` or `CHANGELOG.md`, or "Run workflow" on `main` in the Actions tab. Several notes files merged back to back all get released, one per run, lowest version first (a rehearsal `v0.3.0-rc.1` goes before `v0.3.0`); after a clean run the workflow starts itself again while notes remain.

Pushing a tag by hand starts nothing, and nothing needs it. A version is released once: its tag and GitHub release are never reused.

## How to rehearse

Merge `docs/releases/vX.Y.Z-rc.N.md` (for example `v0.2.0-rc.1.md`). A version with a hyphen is a rehearsal. It runs the same pipeline with the real signing secrets, the real notarization and the real tap push, but nothing reaches users:

- GitHub marks the release as a prerelease, so it is never "latest".
- `CHANGELOG.md` needs no section for it.
- `publish-homebrew` pushes the cask to the tap's `rehearsal` branch, never `main`, so `brew install` still serves the last stable release.
- `verify-homebrew` is skipped, because the cask is not on `main`.

The cask for a rehearsal is the one goreleaser renders for the rc tag. It is attached to the rc release like a stable one: its version is the rc version and its URLs point at the rc assets. That is the simplest honest cask, because it is exactly what the pipeline would publish, and `publish-cask.sh` checks it names the tag's version.

### The first rehearsal, in this order

Nothing in this pipeline has run for real until these have. Do them before the first stable release made this way.

1. Run "Credential health" by hand (Actions tab, "Run workflow"). It proves the six Apple secrets are set and the tap key authenticates.
2. Merge an rc notes file. Watch every job go green: the tag appears, goreleaser signs and notarizes, `verify-release` passes, and the tap's `rehearsal` branch gets the commit.
3. Deliberately fail one rehearsal, to watch containment and reporting work. For example, merge a second rc notes file after temporarily removing the tap deploy key from the tap, which fails `publish-homebrew` and must open the issue "Workflow failed: Release"; restore the key, then run the workflow again and watch it resume. Only ever do this with an `-rc.N` version, never with a stable one.

## How a release runs

`.github/workflows/release.yml` (name "Release") runs on a push to `main` that touches `docs/releases/*.md` or `CHANGELOG.md`, and on "Run workflow" (`workflow_dispatch`, no inputs) for `main`. One release runs at a time. A run waiting behind another can be replaced by a newer waiting run; nothing is lost, because the next run reads the state of `main`.

1. `decide` runs `scripts/release-decide.sh` and outputs the tag, version, commit, whether it is a rehearsal and whether more notes are waiting. If every notes file already has its release, the run ends here with nothing released. It refuses, with the reason in the log and an annotation, when a notes file name is not valid semantic versioning or not a valid tag name, when a GitHub release exists without its tag, when a tag exists at a commit that is not on `main` or lacks the notes file (and has no release), or (stable only) when `CHANGELOG.md` has no section for the version. A tag that exists at a commit of `main` that holds the notes, but has no GitHub release, is the resume case: the release continues at that tag's commit.
2. `verify` runs `make check` on that commit.
3. `release` first requires all six Apple secrets (`scripts/sign-notarize.sh --check-secrets`, which names any missing secret and never prints values), so a missing secret leaves no tag behind. It then creates the tag at the commit with the workflow's token (a tag made this way starts no other workflow, which is why everything continues in this run) and runs goreleaser with `GORELEASER_CURRENT_TAG` set to the derived tag. goreleaser's post-build hook (`scripts/sign-notarize.sh`) signs, notarizes and gates each darwin binary (arm64 and amd64) before it is archived. The notes always say the binaries are signed and notarized. It attaches `teamscrawl.rb` (the Homebrew cask) to the release.
4. `verify-release` downloads the published tarballs and `checksums.txt` and checks them as a user would receive them (`scripts/verify-release.sh`). Last, `scripts/release-flags.sh settle` checks the flags: a rehearsal is a prerelease and not latest; a stable release that an earlier flake demoted is restored (not prerelease, latest) instead of failing.
5. `publish-homebrew` pushes the cask to the tap, ourostack/homebrew-tap, as `teamscrawl <version>` (`scripts/publish-cask.sh`): to `main` for a stable release, to the `rehearsal` branch for a rehearsal. It runs only after `verify-release` passes, so a release with bad artifacts never reaches the tap. It uses the `HOMEBREW_TAP_DEPLOY_KEY` secret, a deploy key that can write to the tap and nothing else, and verifies GitHub's SSH host keys against `scripts/github_known_hosts`.
6. `verify-homebrew` (stable only) installs the cask from the tap and checks the installed binary (`scripts/verify-homebrew.sh`).
7. `contain` runs when `release`, `verify-release` or `verify-homebrew` failed. It marks the release as a prerelease and not latest (`release-flags.sh demote`; harmless when no release exists yet). If `verify-homebrew` failed it also puts the tap's `main` back to the cask it served before, as a new commit and never a force push (`publish-cask.sh --restore-previous`).
8. `continue-release` runs after a clean run when more notes files are waiting, and starts the workflow again on `main` (`actions: write`, `workflow_dispatch`), so the next version is released in its own run.
9. `report-failure` runs when any job above failed or was cancelled: it opens an issue titled "Workflow failed: Release", or comments on the open one with that title, with the workflow name, the outcome (failed or cancelled), the version and the run link. A run cancelled before any job started cannot report, and needs no report: the next run releases what it would have.

Each job has the narrowest `permissions` it needs: `contents: write` only in `release` and `contain`, `actions: write` only in `continue-release`, `issues: write` only in `report-failure`. Checkouts do not keep the workflow token (`persist-credentials: false`) except in `release`, which pushes the tag.

## Credential health

`.github/workflows/credential-health.yml` ("Credential health") runs weekly (Monday 06:17 UTC) and on manual start from the Actions tab. It checks that every Apple secret is set (`scripts/sign-notarize.sh --check-secrets`, no values printed) and that the tap deploy key still authenticates (`scripts/check-tap-key.sh`: `git ls-remote` against the tap over SSH with the key in a private temporary file, never echoed). It cannot check that the Apple credentials are still accepted by Apple; only a release or rehearsal proves that. A failure opens or updates the issue "Workflow failed: Credential health".

## Dependency updates

`.github/dependabot.yml` opens weekly, grouped pull requests for GitHub Actions and Go modules. `.github/workflows/dependabot-automerge.yml` turns on auto-merge (squash) only for Go module updates that are all semver patch or minor, decided by `dependabot/fetch-metadata` and `scripts/automerge-eligible.sh`, and only when both the pull request author and the actor are `dependabot[bot]`. GitHub Actions updates and major versions stay open for a person to read: a moved upstream action tag would otherwise reach the job that holds the Apple secrets. Pinned actions keep their commit SHA and version comment; Dependabot updates both.

## Secrets

Repository secrets on ourostack/teamscrawl:

| Secret | Used by |
| --- | --- |
| `APPLE_DEVELOPER_ID_CERTIFICATE_BASE64`, `APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD`, `APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY`, `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD`, `APPLE_TEAM_ID` | `release` (signing and notarization), `verify-release` and `verify-homebrew` (`APPLE_TEAM_ID`), credential health |
| `HOMEBREW_TAP_DEPLOY_KEY` | `publish-homebrew`, credential health. A write deploy key on ourostack/homebrew-tap |

The workflow token (`GITHUB_TOKEN`) needs these repository settings: it may create tags and releases (no ruleset blocks creating `v*` tags for it), issues are enabled, and "Allow auto-merge" is on for the Dependabot workflow.

Hardening follow-up, not done yet: move the Apple secrets and the tap key into a GitHub Environment restricted to `main`, so a workflow on another branch can never read them. The secret values cannot be read back to move them, so it needs someone to enter them again.

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

- `scripts/release-decide.sh --selftest`: a stable and a rehearsal version, lowest-first order, a rename, add plus edit, a quoted and a non-ASCII file name, the resume case, a tag at the wrong commit, a release without a tag, a stable version without a changelog section, and a tag name git rejects.
- `scripts/publish-cask.sh --selftest`: the first publish pushes one commit, a repeat changes nothing, a cask for another version is refused, a prerelease is refused on `main`, a rehearsal pushes only to its branch, the tap is restored to the previous cask with a new commit, a missing input is named.
- `scripts/release-flags.sh --selftest`: demote, restore of a demoted stable release, and the rehearsal flag checks.
- `scripts/automerge-eligible.sh --selftest`, `scripts/check-tap-key.sh --selftest` and `scripts/report-failure.sh --selftest`.
- `scripts/sign-notarize.sh --selftest`: every signing gate.

What a pull request cannot prove: the workflow wiring, the token's ability to create the tag, start the workflow and open issues, the SSH deploy key and pinned host keys, Apple signing and the real tap, and that a cancelled run still reports. The first rehearsal exercises all of them.

## What a failure means

Every failed or cancelled job also opens or updates an issue (see `report-failure`).

- `release refused: ...` in `decide`: something is wrong and nothing was done. The message says which rule. Fix the notes file or changelog and merge again, or run the workflow again.
- `missing Apple signing secrets: NAME`: a repository secret is unset or empty. Nothing was tagged or published. Set it on ourostack/teamscrawl and run the workflow again.
- `gate ...` failure in `release`: the binary built in CI is not correctly signed or notarized. The tag exists and `contain` demoted any release; read the notary log printed above the failure. A `gate spctl` failure after all retries usually means notarization was not accepted or Apple's service is down.
- `notarization was not accepted`: Apple rejected the submission; the log is printed.
- Failure in `verify-release`: the release is published but a downloaded artifact is wrong. `contain` already marked it as a prerelease so it is not "latest". Treat it as bad.
- Failure in `publish-homebrew`: the release is published and verified but the tap still serves the previous version. `HOMEBREW_TAP_DEPLOY_KEY is required` means the secret is unset; a push failure means the deploy key was removed from the tap or deploy keys were turned off for the organization; `Host key verification failed` means GitHub rotated its SSH host keys (refresh `scripts/github_known_hosts`, see its header).
- Failure in `verify-homebrew`: the tap served a cask whose install did not verify. `contain` put the tap's `main` back to the previous cask and marked the release as a prerelease.
- A credential-health failure names the secret that is missing or says the tap key no longer authenticates. Fix it before the next release.

## How to recover

Two different re-runs exist in the Actions tab, and they behave differently:

- "Re-run failed jobs" repeats only the failed jobs of that run, with the same derived tag and commit. Use it for a flake: `verify-release` (it only reads the release), `publish-homebrew` (safe to run twice), a transient failure in `release` after the tag was made (the tag step accepts a tag that already points at the commit).
- "Re-run all jobs", or "Run workflow" on `main`, starts over from `decide`, which reads the state of `main` again. A version whose tag and GitHub release both exist counts as released and is not run again, so this is the way to resume a release that stopped before the GitHub release was published (the tag exists, no release: it continues at the tag's commit), and to release a version that a refusal had blocked.

By failure:

- Refused in `decide`, or failed in `verify` or before the tag exists: fix the cause on `main` (the notes file name, the changelog section) and merge. The merge re-runs `decide`; nothing else is needed.
- Failed in `release` after the tag exists, release not published: "Re-run failed jobs", or "Run workflow".
- A tag exists at a commit that is not on `main` or lacks the notes file, and there is no release: `decide` says so. Delete the tag (`git push origin :refs/tags/vX.Y.Z`) and run the workflow again.
- `verify-release` failed because of a flake, and `contain` demoted a good stable release: "Re-run failed jobs" re-runs `verify-release`; if every artifact check passes it restores the release (not prerelease, latest).
- `verify-release` failed because an artifact is bad, or `verify-homebrew` failed: the release is demoted and the tap is back on the previous cask. Fix the cause and release a new version (a patch). Versions are never reused, and a version already released cannot be re-released.
- Run the gates by hand on a binary: `APPLE_TEAM_ID=... scripts/sign-notarize.sh --verify ./teamscrawl`.

A prerelease and a release may point at the same commit (for example `v0.1.0-rc.4` and `v0.1.0`). The release job sets `GORELEASER_CURRENT_TAG` to the tag derived for the run, so goreleaser always builds that tag rather than whichever tag `git describe` returns.
