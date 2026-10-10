.PHONY: build test run lint

build:
	go build ./...

test:
	go test ./...

run:
	go run ./cmd/optimizer

lint:
	go vet ./...
