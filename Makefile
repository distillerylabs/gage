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

.PHONY: lint
lint: fmt-check vet libpurity

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

.PHONY: vet
vet:
	go vet ./...

.PHONY: libpurity
libpurity:
	go run ./tools/libpurity/cmd/libpurity internal/gage

.PHONY: clean
clean:
	rm -f $(BIN)
	rm -rf dist/
