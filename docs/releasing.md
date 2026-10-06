# Releasing teamscrawl

A release is a merged release-notes file. Merge `docs/releases/vX.Y.Z.md` to `main` and the pipeline does the rest: it decides the version from the state of `main`, creates the tag, signs and notarizes the darwin binaries, builds the unsigned Windows zips, publishes, verifies the published artifacts on native macOS and Windows runners, and pushes the Homebrew cask. Nobody pushes a tag and nothing is published by hand. Darwin binaries cannot ship unsigned: every Apple secret is required and every signing gate must pass, or the workflow fails. Windows binaries are intentionally unsigned and are verified as downloaded `.zip` artifacts before the cask is pushed.

## How to release

1. Update the product docs and add `## [X.Y.Z] - YYYY-MM-DD` to `CHANGELOG.md` (move the Unreleased entries under it). A stable release without that section is refused. The release notes must state the Windows install path (download the zip, unzip, run `teamscrawl.exe`) and that the Windows assets are intentionally unsigned.
2. Add `docs/releases/vX.Y.Z.md` with the release notes.
3. Run the local candidate checks on the branch you intend to merge: the canonical gate is `make check` plus real-cache acceptance. On Windows hosts without `make` run the underlying commands directly with `GOWORK=off`: `go mod verify`, `go mod tidy -diff`, `gofmt -l .`, the `go vet` / lint / unit-test / e2e commands from the `Makefile`, and `go test -tags acceptance ./acceptance/...` with `TEAMSCRAWL_REAL_CACHE=1`. Record only counts, timings, paths and field names from a real-cache run.
4. Merge to `main`. Follow the run in the Actions tab under "Release".

The release comes from the state of `main`, not from the push: the pipeline looks at every `docs/releases/v*.md` on `main` and releases the lowest version whose release is not finished. A version is finished only when its GitHub release exists and its cask is published on the tap (the tap's `main` for a stable release, its `rehearsal` branch for a rehearsal). A release whose publish failed is therefore not finished, and the next run resumes it at the publish step (see "Resuming a publish"). So a refused or failed release is retried by any later run: a fix-up merge that touches `docs/releases/` or `CHANGELOG.md`, or "Run workflow" on `main` in the Actions tab. Several notes files merged back to back all get released, one per run, lowest version first (a rehearsal `v0.3.0-rc.1` goes before `v0.3.0`); after a clean run the workflow starts itself again while notes remain.

Pushing a tag by hand starts nothing, and nothing needs it. A version is released once: its tag and GitHub release are never reused.

### Versions only move forward

A notes file for a version that is not greater than the highest stable version already released is refused by `decide`, with the file named: it would otherwise become "latest" and move the Homebrew cask backwards. A stable version must be greater than the latest stable release, and a rehearsal must be for a version greater than it (`v0.2.0-rc.1` after `v0.2.0` is refused). The refusal blocks every release until that notes file is deleted or a higher version is released. Separately, `scripts/release-flags.sh settle` never marks a release "latest" while a higher stable release exists. By design this also refuses a patch for an older line (`1.2.4` after `1.3.0`): the Homebrew cask and "latest" only move forward. To ship that fix, release it in a version above the latest stable (`1.3.1`), with the fix included.

Existing releases are not a problem: a notes file whose tag and GitHub release both exist is skipped without looking at the tag, unless it is the highest stable version or a rehearsal above it, whose cask on the tap `decide` reads (older releases are superseded and never resumed, because publishing their cask would move the tap backwards). Tags may be lightweight or annotated (the existing tags are annotated); an annotated tag is read through to its commit only when a release has to resume.

## How to rehearse

Merge a notes file named `docs/releases/vX.Y.Z-rc.N.md` (for example `v0.3.0-rc.1.md`). A version with a hyphen is a rehearsal. It runs the same pipeline with the real signing secrets, the real notarization, the Windows builds and the real tap push, but nothing reaches users:

- GitHub marks the release as a prerelease, so it is never "latest".
- `CHANGELOG.md` needs no section for it.
- `publish-homebrew` pushes the cask to the tap's `rehearsal` branch, never `main`, so `brew install` still serves the last stable release.
- `verify-homebrew` is skipped, because the cask is not on `main`.

The cask for a rehearsal is the one goreleaser renders for the rc tag. It is attached to the rc release like a stable one: its version is the rc version and its URLs point at the rc assets. goreleaser renders it even though `skip_upload: true` (which only skips its own push to the tap); this was checked with a local snapshot build of a prerelease version, and the attach step fails with a clear message if the file is ever missing. That is the simplest honest cask, because it is exactly what the pipeline would publish, and `scripts/publish-cask.sh` checks it names the tag's version.

### The first rehearsal, in this order

The state-based pipeline has not run for real yet (v0.2.0 was released through the old tag trigger, which proved the signing, Windows verification and stable cask push). Do these in order before the first stable release made this way. Every rehearsal version must be above the latest stable release.

1. Run "Credential health" by hand (Actions tab, "Run workflow"). It proves the six Apple secrets are set and the tap key authenticates.
2. Merge one `-rc.1` notes file for a version above the latest stable (for example `v0.3.0-rc.1.md`). Watch every job go green: the tag appears, goreleaser signs and notarizes, the darwin and Windows verification jobs pass, and the tap's `rehearsal` branch gets the commit.
3. Merge two rc notes files together (for example `v0.3.0-rc.2.md` and `v0.4.0-rc.1.md` in one merge). The lowest, `v0.3.0-rc.2`, must release first, then `continue-release` must start the workflow again and release `v0.4.0-rc.1` in its own run.
4. Last, deliberately fail one rc to watch containment and reporting work: merge another rc notes file after temporarily removing the tap deploy key from the tap, which fails `publish-homebrew` and must open the issue "Workflow failed: Release"; restore the key, run the workflow again from `main` (not "Re-run failed jobs") and watch it resume at the publish step: `decide` must report `publish_only`, `verify` and `release` must be skipped, the darwin and Windows verification and `settle` run against the published assets, `publish-homebrew` puts the cask on the `rehearsal` branch, and nothing is rebuilt or signed again. Never do this with a stable version.

## When notarization is slow

Each darwin binary is notarized with `notarytool submit --wait`, bounded by `NOTARY_TIMEOUT` (default 20 minutes). When Apple's notary queue is slow, the `release` job fails with that reason before anything is published: the tag exists and no GitHub release does. Run the Release workflow again ("Run workflow") once the queue recovers; `decide` resumes from the tag.

## Resuming a publish

`decide` reads the tap itself (`ourostack/homebrew-tap` is public; it first checks that the tap repository itself is readable and refuses when it is not, so a private or renamed tap never looks like a missing cask, then reads `Casks/teamscrawl.rb` through the API with the workflow token) rather than keeping a marker: the tap is the fact that matters, it survives any re-run, and a marker could say "published" while the tap says otherwise. A version is finished when its release exists and the cask on the tap's `main` (stable) or `rehearsal` branch (rehearsal) is for that version or a later one, and a stable release is not demoted to a prerelease. Otherwise the run resumes at the tag's commit with `publish_only=true`:

- `verify` (`make check`) and `release` (the build, signing, notarization and the tag) are skipped. Nothing is rebuilt or signed again, and the assets already on the release are reused.
- `verify-release-darwin`, `verify-release-windows` and `settle` run against the published assets. This is deliberate: it is cheap, it reads only, and it is what restores a stable release that `contain` demoted.
- `publish-homebrew` pushes the cask; `verify-homebrew` installs it from the tap (stable only).
- The scripts these jobs run come from the commit the workflow itself runs from (`main`), not from the tag's older commit, so a tag cut before a script fix still gets the fix. The release assets always come from the release. "Re-run failed jobs" on an old run skips `decide` and uses that run's own commit.

Only the highest stable version, and rehearsals above it, are ever resumed. A release below the highest stable release with a missing cask is left alone, and `scripts/publish-cask.sh` also refuses to put a stable cask on the tap's `main` when `main` already serves a later one, so a resume can never move the tap backwards. A cask on `main` does not count for a rehearsal, and a cask for an earlier rehearsal does not count for a later one.

A higher version whose notes file is pending supersedes a lower unfinished release: `decide` leaves the unfinished one alone and releases the new version. So after a failed install (`verify-homebrew`) the next release is a new patch version, and merging its notes file is all it takes; the demoted release does not block it.

Two cases `decide` cannot resume, and what to do:

- A half-uploaded release: the release exists but goreleaser or the cask attach step stopped before all assets were attached. A resume does not rebuild, so `verify-release-*` fails and `contain` demotes the release, on every run. Repair it by hand: delete the GitHub release and its tag (`gh release delete vX.Y.Z --cleanup-tag`), then run the workflow again from `main`; with the tag gone `decide` starts the version fresh.
- A stable release with a missing cask whose tag is not on `main` (or whose commit lacks the notes file): `decide` fails with the reason, and because it fails before choosing, it blocks every release until this is fixed. Publish the cask from the release by hand, or merge the next patch version's notes file (which supersedes it) after deleting the offending tag if it has no release.

What state each failure leaves, and whether it resumes cleanly:

- `publish-homebrew` failed, stable: `contain` does not run (by design: the release verified and `settle` restored its flags, so it stays a normal, latest release with the tap still on the previous cask). The next run resumes at publish.
- `verify-homebrew` failed, stable: `contain` marked the release a prerelease and put the tap's `main` back to the previous cask, so the release is demoted and its cask is not on `main`. The next run resumes at publish: the cask is pushed again, the install check runs again, and `settle` restores the flags only after the verification jobs pass. If the tap could not be put back, the cask is still on `main` but the demoted release keeps the version unfinished, so the run still resumes. A resume fits a failure that was not the cask itself (a flaky runner, a lost key). When the cask itself is bad, merge the notes file of the next patch version instead: it supersedes the demoted release, which is never re-released.

## How a release runs

`.github/workflows/release.yml` (name "Release") runs on a push to `main` that touches `docs/releases/*.md` or `CHANGELOG.md`, and on "Run workflow" (`workflow_dispatch`, no inputs) for `main`. One release runs at a time. A run waiting behind another can be replaced by a newer waiting run; nothing is lost, because the next run reads the state of `main`.

1. `decide` runs `scripts/release-decide.sh` and outputs the tag, version, commit, whether it is a rehearsal and whether more notes are waiting. If every notes file already has its release, the run ends here with nothing released. It refuses, with the reason in the log and an annotation, when a notes file name is not valid semantic versioning or not a valid tag name; when a GitHub release exists without its tag; when a tag exists at a commit that is not on `main` or lacks the notes file (and has no release); when a version is not above the latest released stable version; or (stable only) when `CHANGELOG.md` has no section for the version. A tag that exists at a commit of `main` that holds the notes, but has no GitHub release, is the resume case: the release continues at that tag's commit. A release that exists while its cask is not on the tap is the other resume case (`publish_only=true`): see "Resuming a publish".
2. `verify` runs the macOS `make check` gate on that commit. The Windows test, e2e and coverage gates (`test (windows-latest)`, `e2e (windows-latest)`, `coverage-windows`) are required checks on pull requests, so they have passed before a merge.
3. `release` first requires all six Apple secrets (`scripts/sign-notarize.sh --check-secrets`, which names any missing secret and never prints values), so a missing secret leaves no tag behind. It then creates the tag at the commit with the workflow's token (a tag made this way starts no other workflow, which is why everything continues in this run) and runs goreleaser with `GORELEASER_CURRENT_TAG` set to the derived tag. goreleaser's post-build hook (`scripts/sign-notarize.sh`) signs, notarizes and gates each darwin binary (arm64 and amd64) before it is archived. The notes say the darwin binaries are signed and notarized and the Windows zip assets are intentionally unsigned. It attaches `teamscrawl.rb` (the Homebrew cask) to the release.
4. `verify-release-darwin` downloads the published darwin tarballs and `checksums.txt` and checks them as a user would receive them (`scripts/verify-release.sh`). 
5. `verify-release-windows` (amd64 on `windows-latest`, arm64 on `windows-11-arm`, in parallel with the darwin job) downloads the published Windows zip files, checks them against `checksums.txt`, confirms the PE architecture, proves they are intentionally unsigned, and runs `teamscrawl.exe --json version` from the downloaded archive (`scripts/verify-release.ps1`). It requires a rehearsal to be a prerelease and not latest; it does not fail a demoted stable release, because the `settle` job restores that.
6. `settle` (`scripts/release-flags.sh settle`) runs after the darwin job and both Windows jobs pass, and `publish-homebrew` needs it. A rehearsal must be a prerelease and not latest; a stable release that an earlier flake demoted (by `contain`) is restored (not prerelease, latest unless a higher stable release exists) instead of failing. Because it is its own job downstream of every verification job, re-running a failed verification job always runs it again before the cask is published.
7. `publish-homebrew` pushes the cask to the tap, ourostack/homebrew-tap, as `teamscrawl <version>` (`scripts/publish-cask.sh`): to `main` for a stable release, to the `rehearsal` branch for a rehearsal. It runs only after the darwin and Windows verification jobs and `settle` all pass, so a release with bad artifacts never reaches the tap. It uses the `HOMEBREW_TAP_DEPLOY_KEY` secret, a deploy key that can write to the tap and nothing else, and verifies GitHub's SSH host keys against `scripts/github_known_hosts`.
8. `verify-homebrew` (stable only) installs the cask from the tap and checks the installed binary (`scripts/verify-homebrew.sh`). It runs up to three times, a minute apart (`scripts/retry.sh 3 60`), so one transient failure does not roll back a good release.
9. `contain` runs when `release`, `verify-release-darwin`, `verify-release-windows`, `settle` or `verify-homebrew` failed. It marks the release as a prerelease and not latest (`release-flags.sh demote`; harmless when no release exists yet). If `verify-homebrew` failed it also puts the tap's `main` back to the cask it served before, as a new commit and never a force push (`publish-cask.sh --restore-previous`).
10. `continue-release` runs after a clean run when more notes files are waiting, and starts the workflow again on `main` (`actions: write`, `workflow_dispatch`), so the next version is released in its own run.
11. `report-failure` runs when any job above failed or was cancelled: it opens an issue titled "Workflow failed: Release", or comments on the open one with that title, with the workflow name, the outcome (failed or cancelled), the version and the run link. A run cancelled before any job started cannot report, and needs no report: the next run releases what it would have.

Each job has the narrowest `permissions` it needs: `contents: write` only in `release`, `settle` and `contain`, `actions: write` only in `continue-release`, `issues: write` only in `report-failure`. Checkouts do not keep the workflow token (`persist-credentials: false`) except in `release`, which pushes the tag. Every job reads the tag and commit from `decide`'s outputs, never from `github.ref_name`.

## Credential health

`.github/workflows/credential-health.yml` ("Credential health") runs weekly (Monday 06:17 UTC) and on manual start from the Actions tab. It checks that every Apple secret is set (`scripts/sign-notarize.sh --check-secrets`, no values printed) and that the tap deploy key still authenticates (`scripts/check-tap-key.sh`: `git ls-remote` against the tap over SSH with the key in a private temporary file, never echoed). It cannot check that the Apple credentials are still accepted by Apple; only a release or rehearsal proves that. A failure opens or updates the issue "Workflow failed: Credential health".

## Dependency updates

`.github/dependabot.yml` opens weekly, grouped pull requests for GitHub Actions and Go modules. `.github/workflows/dependabot-automerge.yml` turns on auto-merge (squash) only for Go module updates that are all semver patch or minor, decided by `dependabot/fetch-metadata` and `scripts/automerge-eligible.sh`, and only when both the pull request author and the actor are `dependabot[bot]`. GitHub Actions updates and major versions stay open for a person to read: a moved upstream action tag would otherwise reach the job that holds the Apple secrets. Pinned actions keep their commit SHA and version comment; Dependabot updates both.

## Secrets

Repository secrets on ourostack/teamscrawl:

| Secret | Used by |
| --- | --- |
| `APPLE_DEVELOPER_ID_CERTIFICATE_BASE64`, `APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD`, `APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY`, `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD`, `APPLE_TEAM_ID` | `release` (signing and notarization), `verify-release-darwin` and `verify-homebrew` (`APPLE_TEAM_ID`), credential health |
| `HOMEBREW_TAP_DEPLOY_KEY` | `publish-homebrew`, `contain`, credential health. A write deploy key on ourostack/homebrew-tap |

The workflow token (`GITHUB_TOKEN`) needs these repository settings: it may create tags and releases (no ruleset blocks creating `v*` tags for it), issues are enabled, and "Allow auto-merge" is on for the Dependabot workflow. Windows needs no secrets.

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

`verify-release-darwin` also checks that every tarball matches `checksums.txt`; that `file` and `lipo` report the right architecture per tarball; that Gatekeeper still accepts each binary after a `com.apple.quarantine` attribute is added (simulating a browser download); and that the arm64 binary's `teamscrawl --json version` reports the tag without the leading `v` and the tagged commit.

`verify-release-windows` performs the checksum check for the downloaded Windows zip files, asserts that the extracted `teamscrawl.exe` is amd64 on `windows-latest` and arm64 on `windows-11-arm`, verifies that each Windows binary is intentionally unsigned, and checks `teamscrawl.exe --json version` against the tag and commit.

`verify-homebrew` waits up to 10 minutes for `brew info --cask --json=v2 ourostack/tap/teamscrawl` to report the tag's version (the cask is already pushed, so this only covers GitHub serving the new commit), runs `brew install --cask ourostack/tap/teamscrawl`, checks `teamscrawl --json version` equals the tag, and runs the codesign and spctl gates on the installed binary.

## What is tested before a release

Pull-request CI runs on every pull request (the required checks include `test`, `e2e` on macOS, Ubuntu and Windows, `coverage` and `coverage-windows`). `make lint`, which CI runs on Ubuntu and `make check` runs on macOS, also runs a `--selftest` for each release script, with a stand-in `gh` and throwaway local git repositories. These are bash scripts and run on those two systems only; the Windows jobs do not run them, and the PowerShell release scripts have their own regression test (`scripts/verify-release_test.ps1`, run in the Windows test job).

- `scripts/release-decide.sh --selftest`: a stable and a rehearsal version, lowest-first order, a rename, add plus edit, a quoted and a non-ASCII file name, the resume case, a release whose cask is missing resuming at publish (stable and rehearsal), a release whose cask is on the tap counting as finished, a demoted stable release resuming, a demoted stable release not blocking a newer pending notes file, older and superseded releases never resuming, a tap that cannot be read, or whose repository is private or missing, refusing, a tag at the wrong commit, a release without a tag, a stable version without a changelog section, a tag name git rejects, annotated tags (released: skipped; unreleased: resumed), the exact present state of `main` (nothing to release), and versions only moving forward.
- `scripts/publish-cask.sh --selftest`: the first publish pushes one commit, a repeat changes nothing, a cask for another version is refused, an older stable cask never replaces a later one on `main`, a prerelease is refused on `main`, a rehearsal pushes only to its branch, the tap is restored to the previous cask with a new commit, a tap file with no version line not blocking the publish (the selftest cask is shaped like the one goreleaser renders), a missing input is named.
- `scripts/release-flags.sh --selftest`: demote, restore of a demoted stable release (never latest while a higher stable exists, also with 400 releases listed), and the rehearsal flag checks.
- `scripts/retry.sh --selftest`, `scripts/automerge-eligible.sh --selftest`, `scripts/check-tap-key.sh --selftest` and `scripts/report-failure.sh --selftest`.
- `scripts/sign-notarize.sh --selftest`: every signing gate.
- `scripts/check-release-wiring.sh --selftest`: every job in `release.yml` that runs after `release` states its own `if:` starting from `!cancelled()` or `always()`. A job without one is skipped whenever `release` is, which is what a publish-only resume does on purpose; rehearsal 4 of v0.4.0-rc.3 found `settle`, `publish-homebrew` and `verify-homebrew` skipped that way.

What a pull request cannot prove: the workflow wiring beyond what `check-release-wiring.sh` checks, the token's ability to create the tag, start the workflow and open issues, containment and the tap restore against real GitHub, that a cancelled run still reports, and the Dependabot auto-merge. The first rehearsal exercises all of them. Already proven by the v0.2.0 release: Apple signing and notarization, the darwin and Windows verification jobs, the deploy key and `publish-cask.sh` on the stable path, and the tap's install check.

## What a failure means

Every failed or cancelled job also opens or updates an issue (see `report-failure`).

- `release refused: ...` in `decide`: something is wrong and nothing was done. The message says which rule. Fix the notes file or changelog and merge again, or run the workflow again.
- `missing Apple signing secrets: NAME`: a repository secret is unset or empty. Nothing was tagged or published. Set it on ourostack/teamscrawl and run the workflow again.
- `gate ...` failure in `release`: the binary built in CI is not correctly signed or notarized. The tag exists and `contain` demoted any release; read the notary log printed above the failure. A `gate spctl` failure after all retries usually means notarization was not accepted or Apple's service is down.
- `notarization was not accepted`: Apple rejected the submission; the log is printed.
- Failure in `settle`: the flags are wrong in a way it will not fix (a rehearsal that is not a prerelease or is marked latest) or GitHub could not be read. `contain` demoted the release; read the message.
- Failure in `verify-release-darwin` or `verify-release-windows`: the release is published but a downloaded artifact is wrong. `contain` already marked it as a prerelease so it is not "latest". Treat it as bad.
- Failure in `publish-homebrew`: the release is published and verified but the tap still serves the previous version. `HOMEBREW_TAP_DEPLOY_KEY is required` means the secret is unset; a push failure means the deploy key was removed from the tap or deploy keys were turned off for the organization; `Host key verification failed` means GitHub rotated its SSH host keys (refresh `scripts/github_known_hosts`, see its header).
- Failure in `verify-homebrew`: the install check failed three times in a row, a minute apart (one transient failure does not count), so the tap served a cask that does not install or verify. `contain` put the tap's `main` back to the previous cask and marked the release as a prerelease. After a real failure like this the next release is a new patch version: the failed version is never re-released.
- A credential-health failure names the secret that is missing or says the tap key no longer authenticates. Fix it before the next release.

## How to recover

Two different re-runs exist in the Actions tab, and they behave differently:

- "Re-run failed jobs" repeats only the failed jobs of that run, with the same derived tag and commit. Use it for a flake: `verify-release-darwin`, `verify-release-windows` (they only read the release), `publish-homebrew` (safe to run twice), a transient failure in `release` after the tag was made (the tag step accepts a tag that already points at the commit).
- "Re-run all jobs", or "Run workflow" on `main`, starts over from `decide`, which reads the state of `main` again. A version counts as released only when its tag, its GitHub release and its cask on the tap all exist, and is then not run again. So this is the way to resume a release that stopped before the GitHub release was published (the tag exists, no release: it continues at the tag's commit), a release whose cask never reached the tap (it resumes at publish, without a rebuild), and to release a version that a refusal had blocked.

By failure:

- Refused in `decide`, or failed in `verify` or before the tag exists: fix the cause on `main` (the notes file name, the changelog section) and merge. The merge re-runs `decide`; nothing else is needed.
- Failed in `release` after the tag exists, release not published: "Re-run failed jobs", or "Run workflow".
- A tag exists at a commit that is not on `main` or lacks the notes file, and there is no release: `decide` says so. Delete the tag (`git push origin :refs/tags/vX.Y.Z`) and run the workflow again.
- A verification job (darwin or either Windows job) failed because of a flake, and `contain` demoted a good stable release: "Re-run failed jobs" re-runs only that job and then `settle`, which restores the release (not prerelease, latest unless a higher stable exists) before `publish-homebrew` runs. It works the same whichever verification job flaked.
- A verification job failed because an artifact is bad, or `verify-homebrew` failed: the release is demoted and the tap is back on the previous cask. Fix the cause and release a new version (a patch). Versions are never reused, and a version already released cannot be re-released.
- Run the gates by hand on a binary: `APPLE_TEAM_ID=... scripts/sign-notarize.sh --verify ./teamscrawl`.

A prerelease and a release may point at the same commit (for example `v0.1.0-rc.4` and `v0.1.0`). The release job sets `GORELEASER_CURRENT_TAG` to the tag derived for the run, so goreleaser always builds that tag rather than whichever tag `git describe` returns.
