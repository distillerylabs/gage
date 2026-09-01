MODULE := github.com/denmark/gage

VERSION := $(shell git describe --tags --exact-match 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)'

BIN := gage$(shell go env GOEXE)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gage

.PHONY: test
test:
	GOPROXY=off GOFLAGS=-mod=readonly go test ./...

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
