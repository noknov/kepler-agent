SHELL := /bin/sh

GOCACHE ?= $(CURDIR)/.cache/go-build
GOFILES := $(shell rg --files -g '*.go')

.PHONY: fmt fmt-check vet test test-race build eval-check check

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
	GOCACHE=$(GOCACHE) go test -race ./packages/agent/runtime ./packages/slackagent ./packages/runs ./packages/safety ./packages/slackevents ./packages/toolkit/tools/registry

build:
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-gateway ./gateway/cmd/gateway
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-worker ./worker/cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-observability ./observability/cmd/observability
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot ./cli/cmd/slack-copilot

eval-check:
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	python3 evals/run.py --suite evals/suites/smoke.json --candidates evals/candidates.example.json --model dry-run --output "$$tmp/results" --dry-run >/dev/null; \
	python3 -c 'compile(open("evals/run.py", "rb").read(), "evals/run.py", "exec"); compile(open("evals/import_harbor.py", "rb").read(), "evals/import_harbor.py", "exec")'

check: fmt-check vet test build eval-check
