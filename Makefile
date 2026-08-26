SHELL := /bin/sh

GOCACHE ?= $(CURDIR)/.cache/go-build
GOFILES := $(shell rg --files -g '*.go')

.PHONY: fmt fmt-check boundaries vet test test-race build eval-check check

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))" || { \
		echo "Go files need formatting:"; \
		gofmt -l $(GOFILES); \
		exit 1; \
	}

boundaries:
	python3 scripts/check_boundaries.py

vet:
	GOCACHE=$(GOCACHE) go vet ./...

test:
	GOCACHE=$(GOCACHE) go test ./...

test-race:
	GOCACHE=$(GOCACHE) go test -race ./packages/agent/runtime ./packages/surfaces/slack/agent ./packages/runs ./packages/safety ./packages/surfaces/slack/events ./packages/tools/hosted

build:
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-gateway ./gateway/cmd/gateway
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-worker ./worker/cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-observability ./observability/cmd/observability
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/copilot-agent ./cli/cmd/copilot-agent
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot ./cli/cmd/slack-copilot
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/slack-copilot-app-server ./appserver/cmd/app-server

eval-check:
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	python3 evals/run.py --suite evals/suites/smoke.json --candidates evals/candidates.example.json --model dry-run --output "$$tmp/results" --dry-run >/dev/null; \
	python3 evals/report.py "$$tmp/results" --output "$$tmp/report.html" >/dev/null; \
	python3 -m unittest evals/test_evaluator.py; \
	python3 -c 'compile(open("evals/run.py", "rb").read(), "evals/run.py", "exec"); compile(open("evals/import_harbor.py", "rb").read(), "evals/import_harbor.py", "exec"); compile(open("evals/run_harbor.py", "rb").read(), "evals/run_harbor.py", "exec"); compile(open("evals/harbor_agents/slack_copilot.py", "rb").read(), "evals/harbor_agents/slack_copilot.py", "exec"); compile(open("evals/report.py", "rb").read(), "evals/report.py", "exec")'

check: fmt-check boundaries vet test build eval-check
