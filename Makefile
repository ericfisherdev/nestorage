# Nestorage developer entrypoints.
#
# The Makefile is the single source of truth for every gate: hooks and CI call
# `make <target>`, never a raw tool invocation, so a check cannot pass locally
# and fail in CI because the two ran different flags.

GOLANGCI_LINT_VERSION := v2.11.4

# Coverage profile written by `make test` and read by `make cover`.
COVERAGE_OUT := coverage.out

.PHONY: all build test cover lint fmt hooks hooks-uninstall tidy clean help

## all: default aggregate target (alias for build)
all: build

## build: type-check the module (a library emits no binary artifact yet)
build:
	go build ./...

## test: run the test suite with the race detector and write a coverage profile
test:
	go test -race -cover -coverprofile=$(COVERAGE_OUT) ./...

## cover: summarise the coverage profile written by `make test`
cover:
	go tool cover -func=$(COVERAGE_OUT)

## lint: run golangci-lint using the pinned version
lint:
	golangci-lint run

## fmt: apply the configured formatters in place
fmt:
	golangci-lint fmt

## tidy: prune and refresh go.mod / go.sum
tidy:
	go mod tidy

## hooks: install the Lefthook git hooks
hooks:
	lefthook install

## hooks-uninstall: remove the Lefthook git hooks
hooks-uninstall:
	lefthook uninstall

## clean: remove build artifacts
clean:
	rm -f $(COVERAGE_OUT)

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## //'
