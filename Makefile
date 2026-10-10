.PHONY: build test run lint docker

CONTAINER_ENGINE ?= podman

build:
	go build ./...

test:
	go test ./...

run:
	go run ./cmd/optimizer

lint:
	go vet ./...

docker:
	${CONTAINER_ENGINE} build -t optimizer-v2 .
