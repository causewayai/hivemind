.PHONY: build test run

build:
	CGO_ENABLED=1 go build -o hivemindd ./cmd/hivemindd

test:
	go test ./...

run: build
	./hivemindd
