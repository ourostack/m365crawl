# Coverage allow-lists

`make coverage` (`scripts/check-coverage.sh`) requires every function in `internal/...` to be 100% statement-covered by `go test`. The e2e and acceptance build tags do not count, and neither do `cmd/teamscrawl`, `scripts/...`, `acceptance` and `e2e`, which the script leaves out. Go measures statements only, so statement coverage is the measure.

A function may be carved out only through a file here, one file per Go package, named after the package path with `/` replaced by `-` (for example `internal/leveldb` is `internal-leveldb.txt`). One entry per line:

```
<path relative to the repo root>:<FuncName> <reason>
```

`FuncName` is the name exactly as `go tool cover -func` prints it (a method is its bare name, such as `Run`, so two methods with the same name in one file share one entry). Blank lines and lines starting with `#` are ignored.

## What may be listed

Only functions whose uncovered statements are unreachable from a deterministic test without unreasonable contortion:

- OS or platform glue, such as Full Disk Access probing or signal wiring in `cmd/`.
- Error returns from syscalls that cannot be forced portably, such as an `os.Getwd` failure.

"It would take effort" is not a reason. Prefer refactoring so the failure can be injected (an interface, a function variable, a fake filesystem). Dead defensive code should be deleted, not listed. Every entry carries a concrete reason.

## The list only shrinks

The script fails on an entry with no reason, on an entry whose function is already at 100%, and on an entry whose function no longer exists. Remove the entry in the same change that covers or deletes the function.

## Narrowing the check

`COVERAGE_PACKAGES` overrides the packages to test (default `./internal/...`), for example `make coverage COVERAGE_PACKAGES="./internal/cli ./internal/errs"`. CI sets it only while some packages are still being brought up to the gate.
