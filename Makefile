# juice-ui — backend-for-frontend for the Juice kernel.
# Standard-library-first Go; the only deps are scs and gorilla/csrf.

BIN        := bin/juice-ui
PKG        := ./...
CMD        := ./cmd/juice-ui

# Local run defaults (override on the command line, e.g. `make run KERNEL=...`).
KERNEL     ?= http://127.0.0.1:4040
ADDR       ?= 127.0.0.1:8080
SECRET     ?= dev-secret

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## build: compile the server to bin/juice-ui
build:
	go build -o $(BIN) $(CMD)

## run: build and run the server against a local kernel
run: build
	JUICE_KERNEL_URL=$(KERNEL) JUICE_UI_ADDR=$(ADDR) JUICE_UI_SESSION_SECRET=$(SECRET) ./$(BIN)

## test: run the offline unit/integration suite (no network; kernel is faked)
test:
	go test $(PKG)

## race: run the suite under the race detector
race:
	go test -race $(PKG)

## cover: run tests with a coverage profile and print the summary
cover:
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

## e2e: run the live end-to-end flow harness (needs the `juice` binary + a free :4790/:4791/:8790)
e2e:
	./e2e/flows.sh

## fmt: format all Go sources in place
fmt:
	gofmt -w .

## vet: run go vet
vet:
	go vet $(PKG)

## tidy: sync go.mod/go.sum
tidy:
	go mod tidy

## check: CI gate — formatting, vet, and the offline suite must all pass
check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet $(PKG)
	go test $(PKG)

## clean: remove build and coverage artifacts
clean:
	rm -rf bin coverage.out

.PHONY: help build run test race cover e2e fmt vet tidy check clean
