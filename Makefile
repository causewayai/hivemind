.PHONY: build test run lint check

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
