SHELL := /bin/sh

PYTHON ?= python3
export BASE

GOCACHE ?= $(CURDIR)/.cache/go-build
GOFILES := $(shell rg --files -g '*.go')

.PHONY: fmt fmt-check boundaries vet test test-race build eval-check protocol-generate protocol-check test-ui check

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))" || { \
		echo "Go files need formatting:"; \
		gofmt -l $(GOFILES); \
		exit 1; \
	}

boundaries:
	GOCACHE=$(GOCACHE) go run ./scripts/repocheck

vet:
	GOCACHE=$(GOCACHE) go vet ./...

test:
	GOCACHE=$(GOCACHE) go test ./...

test-race:
	GOCACHE=$(GOCACHE) go test -race ./packages/profiles/local ./packages/agent/delegation ./packages/agent/runtime ./packages/surfaces/slack/agent ./packages/runs ./packages/safety ./packages/surfaces/slack/events ./packages/tools/hosted ./packages/appserver ./packages/surfaces/web

# Deterministic Slack product contracts, including durable cross-worker control.
# Require isolated integration stores instead of silently skipping those cases.
SLACK_EVAL_FLAGS ?=
.PHONY: eval-slack
eval-slack:
	@test -n "$$POSTGRES_TEST_DSN" || { echo "POSTGRES_TEST_DSN must name an isolated test database" >&2; exit 1; }
	@test -n "$$REDIS_TEST_URL" || { echo "REDIS_TEST_URL must name an isolated test Redis database" >&2; exit 1; }
	@GOCACHE=$(GOCACHE) go test -race -count=1 $(SLACK_EVAL_FLAGS) ./packages/surfaces/slack/... ./packages/eventinbox ./packages/sessioninput

test-ui:
	cd apps/cli && pnpm test
	node --test packages/surfaces/web/tests/*.test.mjs
	node --test observability/tests/*.test.mjs

build:
	GOCACHE=$(GOCACHE) go build -trimpath -o /dev/null ./gateway/cmd/gateway
	GOCACHE=$(GOCACHE) go build -trimpath -o /dev/null ./worker/cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o /dev/null ./observability/cmd/observability
	GOCACHE=$(GOCACHE) go build -trimpath -o /dev/null ./cli/cmd/kepler-agent
	GOCACHE=$(GOCACHE) go build -trimpath -o /dev/null ./appserver/cmd/app-server

eval-check:
	@set -eu; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	$(PYTHON) evals/run.py --suite evals/suites/smoke.json --candidates evals/candidates.example.json --model dry-run --output "$$tmp/results" --dry-run >/dev/null; \
	$(PYTHON) evals/report.py "$$tmp/results" --output "$$tmp/report.html" >/dev/null; \
	$(PYTHON) -m unittest discover -s evals -p 'test_*.py'; \
	$(PYTHON) -c 'compile(open("evals/run.py", "rb").read(), "evals/run.py", "exec"); compile(open("evals/import_harbor.py", "rb").read(), "evals/import_harbor.py", "exec"); compile(open("evals/run_harbor.py", "rb").read(), "evals/run_harbor.py", "exec"); compile(open("evals/harbor_agents/kepler_agent.py", "rb").read(), "evals/harbor_agents/kepler_agent.py", "exec"); compile(open("evals/report.py", "rb").read(), "evals/report.py", "exec"); compile(open("evals/gate.py", "rb").read(), "evals/gate.py", "exec")'

protocol-generate:
	GOCACHE=$(GOCACHE) go run ./appserver/cmd/protocolgen

protocol-check:
	@set -eu; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	GOCACHE=$(GOCACHE) go run ./appserver/cmd/protocolgen --schema "$$tmp/schema.json" --typescript "$$tmp/protocol.ts"; \
	diff -u docs/app-server.schema.json "$$tmp/schema.json"; \
	diff -u apps/cli/src/generated/appServerProtocol.ts "$$tmp/protocol.ts"

check: doctor fmt-check repo-check protocol-check vet test build eval-check

# A real, small model evaluation. The model comes from authenticated bootstrap.
EVAL_OUTPUT ?= evals/results/local-$(shell date +%Y%m%d-%H%M%S)
.PHONY: eval eval-build
eval-build:
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/kepler-agent ./cli/cmd/kepler-agent

eval: eval-build
	$(PYTHON) evals/run.py --suite evals/suites/smoke.json --candidates evals/candidates.example.json --candidate kepler-agent --output "$(EVAL_OUTPUT)"
	$(PYTHON) evals/report.py "$(EVAL_OUTPUT)"

EVAL_GOARCH ?= $(shell go env GOARCH)
EVAL_HARBOR_BENCHMARK ?= terminal-bench-2.1
EVAL_HARBOR_RESOURCE_ARGS ?=
EVAL_HARBOR_UBUNTU_MIRROR ?=
EVAL_HARBOR_APT_RECOMMENDS ?= enabled
EVAL_HARBOR_JOBS_DIR ?= evals/results/harbor-jobs
EVAL_HARBOR_OUTPUT ?= evals/results/harbor-$(shell date +%Y%m%d-%H%M%S)
EVAL_HARBOR_ARGS ?= --include-task terminal-bench/openssl-selfsigned-cert
.PHONY: eval-harbor
eval-harbor:
	$(PYTHON) evals/prepare_harbor.py
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 GOOS=linux GOARCH=$(EVAL_GOARCH) go build -trimpath -o bin/kepler-agent-linux-$(EVAL_GOARCH) ./cli/cmd/kepler-agent
	$(PYTHON) evals/run_harbor.py --benchmark "$(EVAL_HARBOR_BENCHMARK)" --candidate kepler-agent --source-ref "$$(git rev-parse HEAD)" --binary-path "bin/kepler-agent-linux-$(EVAL_GOARCH)" --jobs-dir "$(EVAL_HARBOR_JOBS_DIR)" --ripgrep-path bin/rg-linux-musl --agent-kwarg "ubuntu_mirror=$(EVAL_HARBOR_UBUNTU_MIRROR)" --agent-kwarg "apt_recommends=$(EVAL_HARBOR_APT_RECOMMENDS)" --output "$(EVAL_HARBOR_OUTPUT)" $(EVAL_HARBOR_RESOURCE_ARGS) $(EVAL_HARBOR_ARGS)

.PHONY: bench
bench:
	GOCACHE=$(GOCACHE) go test ./packages/profiles/local ./packages/agent/tool ./packages/tools/websearch -run '^$$' -bench 'Benchmark(JSONLAppend|DeferredSearch|CleanWhitespace)$$' -benchmem -benchtime=20x -count=3

.PHONY: eval-harbor-oracle
eval-harbor-oracle:
	$(PYTHON) evals/run_harbor.py --benchmark "$(EVAL_HARBOR_BENCHMARK)" --candidate oracle --jobs-dir "$(EVAL_HARBOR_JOBS_DIR)" --output "$(EVAL_HARBOR_OUTPUT)-oracle" $(EVAL_HARBOR_ARGS)

# Maintenance feedback uses normal toolchains/caches, without custom setup or service management.
.PHONY: doctor doctor-integration repo-check change-report integration-check
doctor:
	$(PYTHON) scripts/repository.py doctor

doctor-integration:
	$(PYTHON) scripts/repository.py doctor --integration

repo-check: boundaries
	$(PYTHON) scripts/repository.py docs
	GOCACHE=$(GOCACHE) go test ./scripts/repocheck
	$(PYTHON) -m unittest discover -s scripts/tests -p 'test_*.py'

change-report:
	$(PYTHON) scripts/repository.py report --base "$$BASE" --output .cache/change-report.json

integration-check: doctor-integration
	GOCACHE=$(GOCACHE) go test -race -count=1 ./packages/connections ./packages/profiles/hosted
	$(MAKE) eval-slack
