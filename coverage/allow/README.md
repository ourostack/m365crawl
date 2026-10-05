# Coverage allow-lists

`make coverage` requires every function in `internal/...` to be 100% statement-covered by `go test`. It runs `scripts/check-coverage.sh`, and the CI `coverage` job keeps that 100% function rule on macOS. `scripts/check-coverage.ps1` is the additive Windows companion gate used by the CI `coverage-windows` job: it runs `go test -coverprofile` on Windows and fails unless every Windows-only file shows executed coverage. The e2e and acceptance build tags do not count, and neither do `cmd/teamscrawl`, `scripts/...`, `acceptance` and `e2e`, which the Unix gate leaves out. Go measures statements only, so statement coverage is the measure.

A function may be carved out only through a file here, one file per Go package, named after the package path with `/` replaced by `-` (for example `internal/leveldb` is `internal-leveldb.txt`). One entry per line:

```
<path relative to the repo root>:<FuncName> <reason>
```

`FuncName` is the name exactly as `go tool cover -func` prints it. That is the bare name even for a method (`Run`, not `(*searchCmd).Run`), so the receiver cannot be part of the key. The gate therefore rejects an entry when its file holds more than one function with that name: rename one of them so the entry names a single function. Blank lines and lines starting with `#` are ignored.

## What may be listed

Only functions whose uncovered statements are unreachable from a deterministic test without unreasonable contortion:

- OS or platform glue, such as Full Disk Access probing or signal wiring in `cmd/`.
- Error returns from syscalls that cannot be forced portably, such as an `os.Getwd` failure.

"It would take effort" is not a reason. Prefer refactoring so the failure can be injected (an interface, a function variable, a fake filesystem). Dead defensive code should be deleted, not listed. Every entry carries a concrete reason.

## The list only shrinks

The script fails on an entry with no reason, an entry whose path is outside its package, an allow file that matches no Go package, on an entry whose function is already at 100%, and on an entry whose function no longer exists. Remove the entry in the same change that covers or deletes the function.

## Narrowing the check

`COVERAGE_PACKAGES` overrides the packages to test (default `./internal/...`), for example `make coverage COVERAGE_PACKAGES="./internal/cli ./internal/errs"`. CI checks every package.

## Files outside the gate

Platform-tagged files that do not compile on the CI platform (for example `internal/cli/width_other.go`, built only off Unix) never appear in the coverage profile, so the gate neither measures nor lists them. Keep such files trivial.
