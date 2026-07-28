# spendlease developer tasks. `make setup` then `make test` is the whole
# onboarding path; everything else here is convenience.

BINARY      := spendlease
CMD         := ./cmd/spendlease
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
MODULE      := github.com/premhiru/spendlease
LDFLAGS     := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

GOLANGCI_VERSION := v2.12.2

.DEFAULT_GOAL := help

## help: list available targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | awk -F': ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## setup: install dev tooling and download modules
.PHONY: setup
setup:
	go mod download
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

## test: run the full suite with the race detector, as CI does
.PHONY: test
test:
	CGO_ENABLED=1 go test -race -count=1 ./...

## test-short: run tests without the race detector (no C toolchain needed)
.PHONY: test-short
test-short:
	go test -count=1 ./...

## cover: run tests and open a coverage report
.PHONY: cover
cover:
	CGO_ENABLED=1 go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

## lint: run golangci-lint and go vet
.PHONY: lint
lint:
	go vet ./...
	golangci-lint run

## fmt: format the tree
.PHONY: fmt
fmt:
	go fmt ./...

## build: build a static binary into ./bin
.PHONY: build
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(CMD)

## run: build and start the gateway on :4000
.PHONY: run
run: build
	./bin/$(BINARY) serve

## demo: run the simulated agent fleet against a local gateway
.PHONY: demo
demo: build
	./bin/$(BINARY) demo

## docker: build the container image
.PHONY: docker
docker:
	docker build -t ghcr.io/premhiru/spendlease:$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) .

## clean: remove build artefacts
.PHONY: clean
clean:
	rm -rf bin coverage.out
