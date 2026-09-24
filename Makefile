GO := $(if $(wildcard .tools/go/bin/go),$(CURDIR)/.tools/go/bin/go,go)
export GOPATH ?= $(CURDIR)/.tools/gopath
export GOMODCACHE ?= $(GOPATH)/pkg/mod

.PHONY: build test check-core
# Historical model fixtures are data, not standalone product packages.
CORE_PACKAGES := ./cmd/... ./internal/... ./web ./runtime ./tools/...
build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/strixglm ./cmd/strixglm
test:
	$(GO) test $(CORE_PACKAGES)
	node --test web/tests/*.test.mjs benchmarks/*.test.mjs

check-core:
	python3 tools/verify-common-core.py
