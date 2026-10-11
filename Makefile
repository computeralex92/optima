.PHONY: build test run lint docker vendor-upstream

CONTAINER_ENGINE ?= podman

include upstream.lock

build:
	go build ./...

test:
	go test ./...

run:
	go run ./cmd/optimizer

lint:
	go vet ./...

docker:
	${CONTAINER_ENGINE} build -t optima .

vendor-upstream: ## Re-vendor openapi.yaml + test_cases from upstream at REV
	@set -e; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	git init -q "$$tmp"; \
	git -C "$$tmp" remote add origin "$(REPO)"; \
	git -C "$$tmp" fetch -q --depth 1 origin "$(REV)"; \
	git -C "$$tmp" checkout -q FETCH_HEAD; \
	cp "$$tmp/openapi.yaml" openapi.yaml; \
	rm -rf test_cases; \
	cp -r "$$tmp/test_cases" test_cases; \
	rev=$$(git -C "$$tmp" rev-parse HEAD); \
	printf 'REPO=%s\nREV=%s\nDATE=%s\n' "$(REPO)" "$$rev" "$$(date +%F)" > upstream.lock; \
	echo "vendored $$rev; review with: git diff --stat"
