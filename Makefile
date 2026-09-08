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
# -timeout is kept explicit but is now a deadlock ceiling rather than a
# scrypt budget: the vaultlock and pty tests block on subprocesses, and
# five minutes fails a hung one well inside CI's own job timeout while
# leaving room for the slowest runner in the matrix.
.PHONY: test
test:
	GOPROXY=off GOFLAGS=-mod=readonly go test -timeout 5m ./...

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
