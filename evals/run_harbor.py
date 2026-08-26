#!/usr/bin/env python3
"""Launch a public Harbor benchmark with a reproducible agent invocation."""

from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
from datetime import UTC, datetime
from pathlib import Path


BENCHMARKS = {
    "terminal-bench-2.1": "terminal-bench/terminal-bench-2-1",
    "swe-bench-verified": "swe-bench/swe-bench-verified",
    "harbor-index-1.0": "harbor-index/harbor-index-1.0",
}
BUILTIN_AGENTS = {"codex", "claude-code", "opencode", "pi"}
SLACK_COPILOT = "evals.harbor_agents.slack_copilot:SlackCopilot"
FULL_GIT_SHA = re.compile(r"^[0-9a-f]{40}$")


def commit() -> str | None:
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"], text=True, capture_output=True, check=False
    )
    return result.stdout.strip() if result.returncode == 0 else None


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Run a Harbor-hosted public benchmark; use --dry-run to inspect first."
    )
    parser.add_argument("--benchmark", choices=BENCHMARKS, required=True)
    parser.add_argument(
        "--candidate", choices=["slack-copilot", *sorted(BUILTIN_AGENTS)], required=True
    )
    parser.add_argument("--model", required=True)
    parser.add_argument("--output", type=Path, required=True, help="Harbor jobs directory")
    parser.add_argument("--attempts", type=int, default=1)
    parser.add_argument("--concurrency", type=int, default=1)
    parser.add_argument("--tasks", type=int, help="Optional sampled task count; omit for full suite")
    parser.add_argument("--include-task", action="append", default=[])
    parser.add_argument("--source-ref", help="Required full commit SHA for slack-copilot")
    parser.add_argument("--harbor", default="harbor")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    if args.attempts < 1 or args.concurrency < 1:
        parser.error("--attempts and --concurrency must be positive")
    if args.tasks is not None and args.tasks < 1:
        parser.error("--tasks must be positive")
    if args.candidate == "slack-copilot" and not args.source_ref:
        parser.error("--source-ref is required for slack-copilot")
    if args.source_ref and not FULL_GIT_SHA.fullmatch(args.source_ref):
        parser.error("--source-ref must be a full 40-character Git commit SHA")
    if args.candidate != "slack-copilot" and args.source_ref:
        parser.error("--source-ref only applies to slack-copilot")

    agent = SLACK_COPILOT if args.candidate == "slack-copilot" else args.candidate
    command = [
        args.harbor,
        "run",
        "--dataset",
        BENCHMARKS[args.benchmark],
        "--agent",
        agent,
        "--model",
        args.model,
        "--jobs-dir",
        str(args.output),
        "--n-attempts",
        str(args.attempts),
        "--n-concurrent",
        str(args.concurrency),
    ]
    if args.tasks is not None:
        command.extend(["--n-tasks", str(args.tasks)])
    for task_name in args.include_task:
        command.extend(["--include-task-name", task_name])
    if args.candidate == "slack-copilot":
        command.extend(["--agent-kwarg", f"source_ref={args.source_ref}"])

    args.output.mkdir(parents=True, exist_ok=True)
    manifest = {
        "schema_version": 1,
        "created_at": datetime.now(UTC).isoformat(),
        "benchmark": args.benchmark,
        "dataset": BENCHMARKS[args.benchmark],
        "candidate": args.candidate,
        "agent": agent,
        "model": args.model,
        "source_ref": args.source_ref,
        "repository_commit": commit(),
        "command": command,
    }
    (args.output / "invocation.json").write_text(json.dumps(manifest, indent=2) + "\n")

    print(shlex.join(command))
    if args.dry_run:
        return 0

    env = dict(os.environ)
    project_root = str(Path(__file__).resolve().parents[1])
    env["PYTHONPATH"] = project_root + os.pathsep + env.get("PYTHONPATH", "")
    return subprocess.run(command, env=env, check=False).returncode


if __name__ == "__main__":
    raise SystemExit(main())
