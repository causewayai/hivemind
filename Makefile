.PHONY: build test run lint check

# The sqlite-vec-go-bindings cgo package #includes "sqlite3ext.h", which it
# doesn't ship itself; it relies on that header being reachable on the C
# include path. mattn/go-sqlite3 vendors it. This happens to already be
# discoverable on the Linux and macOS GitHub-hosted runner images (likely via
# a preinstalled system sqlite3-dev package), but not on Windows, where the
# build fails with "sqlite3ext.h: No such file or directory". Point CGO at
# go-sqlite3's module directory explicitly so the build is portable rather
# than relying on that platform-specific coincidence.
export CGO_CFLAGS := -I$(shell go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)

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
