# CorralAI — build the daemon + its client apps.
#
#   make build     build all binaries into ./bin (version-stamped)
#   make install   go install all binaries
#   make test      go test ./...
#   make vet       go vet ./...
#
# (The demo lives under deploy/demo — see deploy/demo/Makefile.)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
BINS    := corral corral-agent corral-observe corral-admin corral-desktop

.PHONY: build install test vet tidy clean

build: ## build all binaries into ./bin, version-stamped
	@mkdir -p bin
	@for b in $(BINS); do echo "build $$b ($(VERSION))"; \
		go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; done

install: ## go install all binaries, version-stamped
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/...

test: ## run the test suite
	go test ./...

vet: ## go vet
	go vet ./...

tidy: ## go mod tidy
	go mod tidy

clean: ## remove build output
	rm -rf bin
