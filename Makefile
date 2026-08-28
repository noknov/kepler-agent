SHELL := /bin/sh

GOCACHE ?= $(CURDIR)/.cache/go-build
GOFILES := $(shell rg --files -g '*.go')

DESKTOP_DIR := apps/desktop
DESKTOP_SIDECAR := $(DESKTOP_DIR)/src-tauri/binaries/kepler-agent-app-server-$(shell uname -m)-apple-darwin

.PHONY: fmt fmt-check boundaries vet test test-race build appserver-bin desktop-deps desktop-dev desktop-build eval-check check

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
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/kepler-agent-gateway ./gateway/cmd/gateway
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/kepler-agent-worker ./worker/cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/kepler-agent-observability ./observability/cmd/observability
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/kepler-agent ./cmd/kepler
	$(MAKE) appserver-bin

appserver-bin:
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/kepler-agent-app-server ./appserver/cmd/app-server

# The GUI is a native Tauri application, not a browser surface. It uses the
# same local app-server as the CLI over stdio and bundles that binary for release.
desktop-deps:
	cd $(DESKTOP_DIR) && pnpm install

desktop-dev: appserver-bin desktop-deps
	cd $(DESKTOP_DIR) && KEPLER_APP_SERVER=$(CURDIR)/bin/kepler-agent-app-server pnpm tauri dev

desktop-build: appserver-bin desktop-deps
	mkdir -p $(dir $(DESKTOP_SIDECAR))
	cp bin/kepler-agent-app-server $(DESKTOP_SIDECAR)
	cd $(DESKTOP_DIR) && pnpm tauri build

eval-check:
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	python3 evals/run.py --suite evals/suites/smoke.json --candidates evals/candidates.example.json --model dry-run --output "$$tmp/results" --dry-run >/dev/null; \
	python3 evals/report.py "$$tmp/results" --output "$$tmp/report.html" >/dev/null; \
	python3 -m unittest evals/test_evaluator.py; \
	python3 -c 'compile(open("evals/run.py", "rb").read(), "evals/run.py", "exec"); compile(open("evals/import_harbor.py", "rb").read(), "evals/import_harbor.py", "exec"); compile(open("evals/run_harbor.py", "rb").read(), "evals/run_harbor.py", "exec"); compile(open("evals/harbor_agents/kepler_agent.py", "rb").read(), "evals/harbor_agents/kepler_agent.py", "exec"); compile(open("evals/report.py", "rb").read(), "evals/report.py", "exec")'

check: fmt-check boundaries vet test build eval-check
