.PHONY: build test run lint check

# The sqlite-vec-go-bindings cgo package #includes "sqlite3ext.h", which it
# doesn't ship itself; it relies on that header being reachable on the C
# include path. mattn/go-sqlite3 vendors it. This happens to already be
# discoverable on the Linux and macOS GitHub-hosted runner images (likely via
# a preinstalled system sqlite3-dev package), but not on Windows, where the
# build fails with "sqlite3ext.h: No such file or directory". Point CGO at
# go-sqlite3's module directory explicitly so the build is portable rather
# than relying on that platform-specific coincidence.
# $(subst) normalizes to forward slashes: on Windows, `go list` returns a
# backslash-separated path, and an unquoted backslash gets eaten as a shell
# escape character when this expands into a recipe's command line (the
# include path silently collapses, reproducing the same "not found" error).
# Forward slashes are accepted by MinGW GCC on Windows too, so this is safe
# on every platform.
export CGO_CFLAGS := -I$(subst \,/,$(shell go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3))

debug-cgo:
	@echo "CGO_CFLAGS=[$$CGO_CFLAGS]"

build:
	CGO_ENABLED=1 go build -o hivemindd ./cmd/hivemindd

test:
	go test ./...

run: build
	./hivemindd

lint:
	go vet ./...
	golangci-lint run ./...

check: lint test
