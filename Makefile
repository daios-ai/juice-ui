BINARY  := juice-ui
PKG     := github.com/daios-ai/juice-ui/cmd/juice-ui
# The family's executables live in $JUICE_HOME/bin (ecosystem-standard §2).
PREFIX  ?= $(or $(JUICE_HOME),$(HOME)/.juice)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)"

# Local run defaults (override on the command line, e.g. `make run KERNEL=...`).
KERNEL  ?= http://127.0.0.1:4040
ADDR    ?= 127.0.0.1:8080
SECRET  ?= dev-secret

.PHONY: build test clean install uninstall run fmt vet check

build:
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

test:
	go test ./... -count=1

clean:
	rm -f $(BINARY) coverage.out

install: build
	mkdir -p $(PREFIX)/bin
	install -m 755 $(BINARY) $(PREFIX)/bin/$(BINARY)

# Removes the installed binary only; $JUICE_HOME state (keys, config, stores) is
# left intact.
uninstall:
	rm -f $(PREFIX)/bin/$(BINARY)

# --- juice-ui extras (offline test above is the regression gate) ---

run: build
	JUICE_KERNEL_URL=$(KERNEL) JUICE_UI_ADDR=$(ADDR) JUICE_UI_SESSION_SECRET=$(SECRET) ./$(BINARY)

fmt:
	gofmt -w .

vet:
	go vet ./...

# Regenerate the committed theme stylesheet from the Ghostty palettes. Guarded
# by server/themes_test.go (TestThemesCSSInSync), so a forgotten run fails check.
themes:
	go run ./cmd/themegen > server/static/themes.css

# CI gate: formatting, vet, and the offline suite must all pass.
check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./... -count=1
