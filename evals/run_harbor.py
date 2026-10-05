#!/usr/bin/env python3
"""Launch Harbor's native grader, recording execution identity and failures."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shlex
import shutil
import signal
import subprocess
from datetime import UTC, datetime
from pathlib import Path

BENCHMARKS = {
    "harbor-hello-world": "harbor/hello-world",
    "terminal-bench-2.1": "terminal-bench/terminal-bench-2-1",
    "swe-bench-verified": "swe-bench/swe-bench-verified",
    "harbor-index-1.0": "harbor-index/harbor-index-1.0",
}
BUILTIN_AGENTS = {"codex", "claude-code", "opencode", "pi", "oracle"}
KEPLER_AGENT = "evals.harbor_agents.kepler_agent:KeplerAgent"
FULL_GIT_SHA = re.compile(r"^[0-9a-f]{40}$")


def commit() -> str | None:
    result = subprocess.run(["git", "rev-parse", "HEAD"], text=True, capture_output=True)
    return result.stdout.strip() if result.returncode == 0 else None


def execution_outcome(output: Path, return_code: int) -> tuple[int, list[dict]]:
    jobs = []
    for path in sorted(output.glob("*/result.json")):
        data = json.loads(path.read_text())
        stats = data.get("stats", {})
        jobs.append({"result": str(path.relative_to(output)), "finished_at": data.get("finished_at"), "total": data.get("n_total_trials", 0), "completed": stats.get("n_completed_trials", 0), "errors": stats.get("n_errored_trials", 0)})
    valid = bool(jobs) and all(job["finished_at"] and job["total"] > 0 and job["completed"] == job["total"] and job["errors"] == 0 for job in jobs)
    return return_code or (0 if valid else 1), jobs


def run_harbor(command: list[str], env: dict) -> int:
    # One process group owns Harbor and its Docker/compose subprocesses.
    process = subprocess.Popen(command, env=env, start_new_session=True)
    try:
        return process.wait()
    except KeyboardInterrupt:
        try:
            os.killpg(process.pid, signal.SIGINT)
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
        except ProcessLookupError:
            pass
        finally:
            # A timed-out Docker exec/pull may outlive Harbor itself.
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        return 130


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--benchmark", choices=BENCHMARKS, required=True)
    parser.add_argument("--candidate", choices=["kepler-agent", *sorted(BUILTIN_AGENTS)], required=True)
    parser.add_argument("--model", help="Kepler defaults to its authenticated gateway model; oracle needs no model")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--attempts", type=int, default=1)
    parser.add_argument("--concurrency", type=int, default=1)
    parser.add_argument("--agent-setup-timeout-multiplier", type=float)
    parser.add_argument("--verifier-timeout-multiplier", type=float)
    parser.add_argument("--tasks", type=int)
    parser.add_argument("--include-task", action="append", default=[], help="Full Harbor task name, e.g. terminal-bench/cancel-async-tasks")
    parser.add_argument("--source-ref", help="Required full source commit for Kepler")
    parser.add_argument("--binary-path", type=Path, help="Upload a prebuilt Linux CLI instead of cloning source")
    parser.add_argument("--ripgrep-path", type=Path, help="Prepared static Linux ripgrep; avoids per-task package installation")
    parser.add_argument("--agent-kwarg", action="append", default=[])
    parser.add_argument("--harbor", default="harbor")
    parser.add_argument("--prepare-images", action="store_true", help="Download task specs and warm Docker images before timed trials")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    if args.attempts < 1 or args.concurrency < 1 or (args.tasks is not None and args.tasks < 1):
        parser.error("attempts, concurrency and tasks must be positive")
    for value in (args.agent_setup_timeout_multiplier, args.verifier_timeout_multiplier):
        if value is not None and value <= 0:
            parser.error("timeout multipliers must be positive")
    if args.source_ref and not FULL_GIT_SHA.fullmatch(args.source_ref):
        parser.error("--source-ref must be a full 40-character Git commit SHA")
    if args.candidate == "kepler-agent" and not args.source_ref:
        parser.error("--source-ref is required for kepler-agent")
    if args.candidate != "kepler-agent" and (args.source_ref or args.binary_path or args.ripgrep_path):
        parser.error("source-ref and binary-path only apply to kepler-agent")
    if any("=" not in item or item.startswith("=") for item in args.agent_kwarg):
        parser.error("--agent-kwarg must use key=value form")
    keys = [item.split("=", 1)[0] for item in args.agent_kwarg]
    if len(keys) != len(set(keys)):
        parser.error("--agent-kwarg keys must be unique")
    if args.candidate == "kepler-agent" and set(keys).intersection({"source_ref", "source_repo", "binary_path", "ripgrep_path", "provider", "protocol", "api_key_env", "base_url_env"}):
        parser.error("source identity is launcher-managed; Kepler uses gateway auth, not direct-provider kwargs")
    if args.binary_path:
        args.binary_path = args.binary_path.resolve()
        if not args.binary_path.is_file() or args.binary_path.read_bytes()[:4] != b"\x7fELF":
            parser.error("--binary-path must be a Linux ELF executable; build with GOOS=linux")
    if args.ripgrep_path:
        args.ripgrep_path = args.ripgrep_path.resolve()
        if not args.binary_path or not args.ripgrep_path.is_file() or args.ripgrep_path.read_bytes()[:4] != b"\x7fELF":
            parser.error("ripgrep-path requires binary-path and a prepared Linux ELF binary")
    args.output = args.output.resolve()
    if args.output.exists():
        parser.error("output already exists; preserve results and choose a new directory")
    image_preflight = None
    preflight = None
    if not args.dry_run:
        if not shutil.which(args.harbor):
            parser.error("Harbor is missing: uv tool install harbor")
        if args.candidate == "kepler-agent":
            from kepler_gateway import prepare
            try:
                preflight = prepare(args.model)
            except (ValueError, OSError) as error:
                parser.error(str(error))
            args.model = preflight["model"]
    if not args.model and args.candidate != "oracle":
        parser.error("--model is required without a live Kepler gateway")
    if args.prepare_images and not args.dry_run:
        from prepare_harbor import prepare_images
        image_preflight = prepare_images(BENCHMARKS[args.benchmark], args.include_task, args.harbor)
    args.output = args.output.resolve()
    agent = KEPLER_AGENT if args.candidate == "kepler-agent" else args.candidate
    command = [args.harbor, "run", "--dataset", BENCHMARKS[args.benchmark], "--agent", agent, "--jobs-dir", str(args.output), "--n-attempts", str(args.attempts), "--n-concurrent", str(args.concurrency)]
    if args.model:
        command.extend(["--model", args.model])
    for flag, value in (("--n-tasks", args.tasks), ("--agent-setup-timeout-multiplier", args.agent_setup_timeout_multiplier), ("--verifier-timeout-multiplier", args.verifier_timeout_multiplier)):
        if value is not None:
            command.extend([flag, str(value)])
    for task in args.include_task:
        command.extend(["--include-task-name", task])
    if args.candidate == "kepler-agent":
        command.extend(["--agent-kwarg", f"source_ref={args.source_ref}"])
    if args.binary_path:
        command.extend(["--agent-kwarg", f"binary_path={args.binary_path}"])
    if args.ripgrep_path:
        command.extend(["--agent-kwarg", f"ripgrep_path={args.ripgrep_path}"])
    for item in args.agent_kwarg:
        command.extend(["--agent-kwarg", item])
    args.output.mkdir(parents=True, exist_ok=False)
    manifest = {
        "schema_version": 1, "created_at": datetime.now(UTC).isoformat(),
        "benchmark": args.benchmark, "dataset": BENCHMARKS[args.benchmark],
        "candidate": args.candidate, "agent": agent, "model": args.model,
        "source_ref": args.source_ref, "repository_commit": commit(),
        "repository_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], text=True).strip()),
        "binary_sha256": hashlib.sha256(args.binary_path.read_bytes()).hexdigest() if args.binary_path else None,
        "ripgrep_sha256": hashlib.sha256(args.ripgrep_path.read_bytes()).hexdigest() if args.ripgrep_path else None,
        "image_preflight": image_preflight,
        "preflight": preflight, "dry_run": args.dry_run, "status": "dry_run" if args.dry_run else "running", "command": command,
    }
    manifest_path = args.output / "invocation.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    print(shlex.join(command), flush=True)
    if args.dry_run:
        return 0
    env = dict(os.environ)
    project_root = str(Path(__file__).resolve().parents[1])
    env["PYTHONPATH"] = project_root + os.pathsep + env.get("PYTHONPATH", "")
    return_code = run_harbor(command, env)
    exit_code, jobs = execution_outcome(args.output, return_code)
    status = "cancelled" if return_code == 130 else ("completed" if exit_code == 0 else "failed")
    manifest.update(status=status, exit_code=exit_code, harbor_exit_code=return_code, native_jobs=jobs, ended_at=datetime.now(UTC).isoformat())
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"Harbor execution {manifest['status']}: {len(jobs)} job(s); inspect native result.json for rewards", flush=True)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
