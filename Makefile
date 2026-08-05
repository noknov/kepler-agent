SHELL := /bin/sh

GOCACHE ?= $(CURDIR)/.cache/go-build
GOFILES := $(shell rg --files -g '*.go')

.PHONY: fmt fmt-check vet test test-race build check

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))" || { \
		echo "Go files need formatting:"; \
		gofmt -l $(GOFILES); \
		exit 1; \
	}

vet:
	GOCACHE=$(GOCACHE) go vet ./...

test:
	GOCACHE=$(GOCACHE) go test ./...

test-race:
	GOCACHE=$(GOCACHE) go test -race ./packages/agent ./packages/conversation ./packages/memory ./packages/runs ./packages/safety ./packages/slackevents ./packages/toolkit/tools/registry

build:
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-agent ./cmd/slack-copilot-agent
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-gateway ./gateway/cmd/gateway
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-worker ./worker/cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-observability ./observability/cmd/observability
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot ./cli/cmd/slack-copilot
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-app-server ./appserver/cmd/app-server

check: fmt-check vet test build
