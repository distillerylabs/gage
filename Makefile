MODULE := github.com/denmark/gage

VERSION := $(shell git describe --tags --exact-match 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)'

BIN := gage$(shell go env GOEXE)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gage

# This suite used to be KDF-bound rather than accidentally slow: every
# unlock ran scrypt at the real shipped work factor (2^19) and cmd/gage
# drives ~190 CLI invocations, most of which unlock, so it needed a
# 30-minute ceiling to survive a GitHub windows-latest runner. It no
# longer does: both TestMains now lower the *live* work factor for the
# test binary only (see SetScryptWorkFactorForTests), which takes the
# whole suite to seconds. Still a real scrypt pass on every unlock, just
# a cheap one — no code path is skipped.
#
# The thing that actually ships stays covered, which was the original
# objection to a test-only cheaper KDF:
# TestScryptWorkFactorIsDeliberate checks the shipped constant,
# TestOnlyTheTestHookWritesScryptWorkFactor checks that nothing but the
# test hook can move the live copy, and
# TestShippedWorkFactorReachesARealAgeFile pays one real 2^19 scrypt to
# prove that number reaches a real age header.
#
# -timeout is kept explicit but is a deadlock ceiling rather than a
# scrypt budget: the vaultlock and pty tests block on subprocesses, so a
# hung one has to fail the run instead of hanging until CI's own job
# timeout.
#
# It is 20m because "leaving room for the slowest runner in the matrix"
# has to mean a *cold* windows-latest runner, and the 5m this used to be
# did not. Per-package wall clock, warm setup-go cache vs cold, for the
# two big packages:
#
#              cmd/gage        internal/gage
#   ubuntu     27s  / 27s      19s  / 19s
#   macOS      27s  / 44s      25s  / 44s
#   windows    131s / >300s    154s / >300s
#
# Cache warmth is free on Linux, ~1.7x on macOS, and decisive on Windows,
# where go-git's local transport spawns a git-receive-pack per push and
# the suite pushes ~80 times. Warm Windows already sat at half the 5m
# ceiling; the first cold Windows run blew through it in both packages —
# including cmd/gage, which that commit had not touched at all. Cutting
# the ceiling to 5m (1494a3a) was calibrated on the warm Linux/macOS
# numbers, where it looked like 10x headroom.
#
# So: raise the ceiling rather than trim tests, since nothing here is
# slow by accident. If Windows ever approaches 20m, the fix is the push
# count or the transport, not this number. The true cold-Windows figure
# is still unknown — both packages were killed at 300s, not measured.
.PHONY: test
test:
	GOPROXY=off GOFLAGS=-mod=readonly go test -timeout 20m ./...

# Same command as `test` plus a coverage profile. -coverpkg=./... credits
# a package for lines exercised by *other* packages' tests, which matters
# here because cmd/gage's in-process CLI tests drive most of internal/gage.
# CI runs this instead of `test`; `test` stays for a quick local run.
.PHONY: cover
cover:
	GOPROXY=off GOFLAGS=-mod=readonly go test -timeout 20m -covermode=atomic -coverprofile=coverage.out -coverpkg=./... ./...

# Pinned for the same reason as golangci-lint below. Each tool installs
# into its own version-stamped directory under bin/, so changing the pin
# yields a new path and make reinstalls without any version parsing.
GO_TEST_COVERAGE_VERSION := v2.19.0
GO_TEST_COVERAGE_BIN     := $(CURDIR)/bin/go-test-coverage-$(GO_TEST_COVERAGE_VERSION)/go-test-coverage$(shell go env GOEXE)

$(GO_TEST_COVERAGE_BIN):
	@echo "installing go-test-coverage $(GO_TEST_COVERAGE_VERSION) into bin/..."
	@GOBIN=$(dir $(GO_TEST_COVERAGE_BIN)) go install github.com/vladopajic/go-test-coverage/v2@$(GO_TEST_COVERAGE_VERSION)

# Reads coverage.out (run `make cover` first); thresholds live in
# .testcoverage.yml.
.PHONY: cover-check
cover-check: $(GO_TEST_COVERAGE_BIN)
	"$(GO_TEST_COVERAGE_BIN)" --config=.testcoverage.yml

# Unlike the rest of the toolchain this needs network: govulncheck fetches
# the Go vulnerability database at run time, so it can't run under the
# GOPROXY=off used by `test`/`cover`. The result can also change without a
# code change, when a new advisory is published.
GOVULNCHECK_VERSION := v1.8.0
GOVULNCHECK_BIN     := $(CURDIR)/bin/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck$(shell go env GOEXE)

$(GOVULNCHECK_BIN):
	@echo "installing govulncheck $(GOVULNCHECK_VERSION) into bin/..."
	@GOBIN=$(dir $(GOVULNCHECK_BIN)) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: vuln
vuln: $(GOVULNCHECK_BIN)
	"$(GOVULNCHECK_BIN)" ./...

# Pinned rather than tracking latest: golangci-lint's config schema
# changed between v1 and v2, and a linter that silently gains new checks
# on a version bump turns an unrelated CI run red.
#
# Installed into a repo-local bin/ rather than resolved from PATH, so
# `make lint` runs the same version everywhere and a developer's
# system-wide golangci-lint can't silently produce different results
# than CI does.
GOLANGCI_VERSION := v2.6.2
GOLANGCI_BIN     := $(CURDIR)/bin/golangci-lint$(shell go env GOEXE)

.PHONY: lint
lint: fmt-check golangci-lint

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmt-check
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needs to be run on:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

# golangci-lint's "standard" set already runs govet, so `make lint` does
# not invoke `go vet` separately. This target stays for a quick vet-only
# check without the full linter.
.PHONY: vet
vet:
	go vet ./...

# Installs the pinned golangci-lint into ./bin unless it's already there
# at the right version. Re-running is cheap; a version mismatch
# reinstalls rather than silently linting with the wrong one.
$(GOLANGCI_BIN):
	@echo "installing golangci-lint $(GOLANGCI_VERSION) into bin/..."
	@GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

.PHONY: golangci-lint
golangci-lint:
	@if ! "$(GOLANGCI_BIN)" --version 2>/dev/null | grep -q "$(patsubst v%,%,$(GOLANGCI_VERSION))"; then \
		$(MAKE) --no-print-directory $(GOLANGCI_BIN); \
	fi
	"$(GOLANGCI_BIN)" run ./...

.PHONY: clean
clean:
	rm -f $(BIN)
	rm -rf dist/

# Separate from `clean` so an ordinary clean doesn't force a multi-minute
# golangci-lint reinstall on the next lint run.
.PHONY: clean-tools
clean-tools:
	rm -rf bin/

# Wipes every directory gage owns on this machine ($GAGE_CONFIG, $GAGE_DATA,
# $GAGE_STATE) so you can test the tool from a fresh-install state. Deletes
# real vaults, identities, and tokens if you have any registered — the
# underlying script prints exactly what it resolved and requires typing
# "yes" before deleting anything, precisely because this is unrecoverable.
.PHONY: reset-local-state
reset-local-state:
	go run ./scripts/resetlocalstate
