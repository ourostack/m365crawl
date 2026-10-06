BINARY ?= bin/teamscrawl
GOLANGCI_LINT_VERSION ?= v2.14.0
GOVULNCHECK_VERSION ?= v1.8.0
ACTIONLINT_VERSION ?= v1.7.12
GORELEASER_VERSION ?= v2.18.2
POWERSHELL ?= powershell
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

export GOWORK := off

.DEFAULT_GOAL := help

.PHONY: help build test e2e acceptance acceptance-calendar v8vectors fixture fmt fmt-check vet lint golangci vulncheck workflow-lint script-lint tidy-check check coverage snapshot screenshot clean

help:
	@printf '%s\n' \
		'Available targets:' \
		'  help           Print available targets (default).' \
		'  build          Build the CLI into $(BINARY).' \
		'  test           Run unit tests with the race detector (COVERPROFILE=path to write coverage).' \
		'  e2e            Run end-to-end tests (build tag e2e).' \
		'  acceptance     Run the real-cache acceptance tests (build tag acceptance; needs TEAMSCRAWL_REAL_CACHE=1, Full Disk Access, node, python3).' \
		'  acceptance-calendar  Run only the calendar and Outlook real-cache checks (TEAMSCRAWL_REAL_CACHE=1; TEAMSCRAWL_OUTLOOK_ROOT enables the Outlook ones).' \
		'  v8vectors      Regenerate testdata/v8 with Node 22 (scripts/v8vectors/gen.mjs).' \
		'  fixture        Regenerate testdata/teams-fixture with Edge via Playwright (scripts/fixture).' \
		'  fmt            Apply Go formatting.' \
		'  fmt-check      Fail if any file needs formatting.' \
		'  vet            Run go vet.' \
		'  lint           Run golangci-lint, govulncheck, actionlint, shellcheck and the release script selftests.' \
		'  tidy-check     Verify go.mod and go.sum are tidy.' \
		'  coverage       Enforce 100% function coverage on internal/... (COVERAGE_PACKAGES to narrow on Unix; on Windows the PowerShell gate proves the Windows-only files are covered).' \
		'  check          Run every local gate enforced by CI.' \
		'  snapshot       Build release artifacts locally without publishing.' \
		'  screenshot     Regenerate screenshot.png from the committed fixture (Node 22, Edge via Playwright).' \
		'  clean          Remove local build output.'

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/ourostack/teamscrawl/internal/cli.version=$(VERSION) -X github.com/ourostack/teamscrawl/internal/cli.commit=$(COMMIT) -X github.com/ourostack/teamscrawl/internal/cli.date=$(DATE)" -o "$(BINARY)" ./cmd/teamscrawl

test:
	go test -race -count=1 -timeout 15m $(if $(COVERPROFILE),-coverprofile=$(COVERPROFILE)) ./...

e2e:
	go test -count=1 -tags e2e ./e2e/...

acceptance:
	go test -count=1 -tags acceptance -timeout 30m -v ./acceptance/...

acceptance-calendar:
	go test -count=1 -tags acceptance -timeout 30m -v -run 'TestRealCalendar|TestRealOutlook' ./acceptance/...

v8vectors:
	node scripts/v8vectors/gen.mjs

fixture:
	cd scripts/fixture && npm ci --no-audit --no-fund
	GO="$$(command -v go)" node scripts/fixture/generate.mjs

fmt:
	gofmt -w acceptance cmd internal e2e

fmt-check:
	@set -e; \
	changed="$$(gofmt -l .)"; \
	if [ -n "$$changed" ]; then printf 'gofmt wants changes in:\n%s\n' "$$changed"; exit 1; fi

vet:
	go vet ./...
	go vet -tags e2e ./...
	go vet -tags acceptance ./...

lint: golangci vulncheck workflow-lint script-lint

golangci:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --build-tags e2e ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --build-tags acceptance ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

workflow-lint:
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

script-lint:
	@command -v shellcheck >/dev/null || { echo "shellcheck is required (brew install shellcheck)"; exit 1; }
	shellcheck scripts/*.sh
	scripts/sign-notarize.sh --selftest
	scripts/release-decide.sh --selftest
	scripts/publish-cask.sh --selftest
	scripts/check-tap-key.sh --selftest
	scripts/report-failure.sh --selftest
	scripts/release-flags.sh --selftest
	scripts/automerge-eligible.sh --selftest
	scripts/retry.sh --selftest
	scripts/check-release-wiring.sh --selftest

tidy-check:
	go mod verify
	go mod tidy -diff

check: tidy-check fmt-check vet lint test coverage e2e

coverage:
ifeq ($(OS),Windows_NT)
	$(POWERSHELL) -NoProfile -ExecutionPolicy Bypass -File scripts/check-coverage.ps1
else
	./scripts/check-coverage.sh
endif

screenshot:
	cd scripts/fixture && npm ci --no-audit --no-fund
	GO="$$(command -v go)" node scripts/screenshot/generate.mjs

snapshot:
	go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean --skip=publish

clean:
	rm -rf -- bin dist
