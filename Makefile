BINARY  := ical-cli
PREFIX  ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

GO ?= go

.PHONY: all build test lint install clean fmt vet tidy cover help

all: build

## build: compile the binary into ./bin
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

## test: run the test suite with the race detector
test:
	$(GO) test -race ./...

## cover: run tests and report per-package coverage
cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

## lint: run go vet and golangci-lint
lint: vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found; install it with:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; \
	fi

## vet: run go vet
vet:
	$(GO) vet ./...

## fmt: format all Go source
fmt:
	$(GO) fmt ./...

## tidy: prune and verify go.mod
tidy:
	$(GO) mod tidy
	$(GO) mod verify

## install: install the binary into $(PREFIX)/bin
install:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(PREFIX)/bin/$(BINARY) .
	@echo "installed $(PREFIX)/bin/$(BINARY)"

## clean: remove build artifacts
clean:
	rm -rf bin coverage.out

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
