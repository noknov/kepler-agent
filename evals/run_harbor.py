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


def execution_outcome(output: Path, return_code: int, only_job: str | None = None) -> tuple[int, list[dict]]:
    jobs = []
    for path in sorted(output.glob("*/result.json")):
        if only_job is not None and path.parent.name != only_job:
            continue
        data = json.loads(path.read_text())
        stats = data.get("stats", {})
        jobs.append({"result": str(path.relative_to(output)), "finished_at": data.get("finished_at"), "total": data.get("n_total_trials", 0), "completed": stats.get("n_completed_trials", 0), "errors": stats.get("n_errored_trials", 0)})
    valid = bool(jobs) and all(job["finished_at"] and job["total"] > 0 and job["completed"] == job["total"] and job["errors"] == 0 for job in jobs)
    return return_code or (0 if valid else 1), jobs


def stop_harbor(process: subprocess.Popen) -> None:
    try:
        os.killpg(process.pid, signal.SIGINT)
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()
    except ProcessLookupError:
        pass
    finally:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass


def docker_available_bytes(container: str) -> int:
    # POSIX df output: filesystem, blocks, used, available, capacity, mount.
    # Probe an existing container on the target daemon, without mounting or
    # modifying its data. Host filesystem free space is not Docker VM space.
    result = subprocess.run(["docker", "exec", container, "df", "-Pk", "/"],
                            check=True, capture_output=True, text=True, timeout=10)
    return int(result.stdout.splitlines()[-1].split()[3]) * 1024


def check_disk_budget(container: str | None, minimum: int) -> None:
    if container and docker_available_bytes(container) < minimum:
        raise RuntimeError("Docker disk reserve reached; stopping benchmark to protect other services")


def run_harbor(command: list[str], env: dict, probe_container: str | None = None, minimum_bytes: int = 0) -> int:
    check_disk_budget(probe_container, minimum_bytes)
    process = subprocess.Popen(command, env=env, start_new_session=True)
    try:
        while True:
            try:
                return process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                if probe_container:
                    try:
                        check_disk_budget(probe_container, minimum_bytes)
                    except (RuntimeError, ValueError, subprocess.SubprocessError) as error:
                        print(f"Resource check failed: {error}", flush=True)
                        stop_harbor(process)
                        return 75
    except KeyboardInterrupt:
        stop_harbor(process)
        return 130


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--benchmark", choices=BENCHMARKS, required=True)
    parser.add_argument("--candidate", choices=["kepler-agent", *sorted(BUILTIN_AGENTS)], required=True)
    parser.add_argument("--model", help="Kepler defaults to its authenticated gateway model; oracle needs no model")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--jobs-dir", type=Path, help="Shared native Harbor job directory for the viewer; invocation artifacts remain in output")
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
    parser.add_argument("--disk-probe-container", help="Existing container on Docker daemon used for read-only free-space checks")
    parser.add_argument("--min-free-disk-gib", type=float, help="Disk space reserved for other services; stop before violating it")
    parser.add_argument("--prepare-images", action="store_true", help="Download task specs and warm Docker images before timed trials")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    if bool(args.disk_probe_container) != (args.min_free_disk_gib is not None):
        parser.error("disk-probe-container and min-free-disk-gib must be used together")
    if args.min_free_disk_gib is not None and (not 0 < args.min_free_disk_gib < float("inf")):
        parser.error("min-free-disk-gib must be finite and positive")
    minimum_bytes = int((args.min_free_disk_gib or 0) * 1024**3)
    if args.disk_probe_container and not args.dry_run:
        check_disk_budget(args.disk_probe_container, minimum_bytes)
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
    jobs_dir = args.jobs_dir.resolve() if args.jobs_dir else args.output
    job_name = args.output.name if args.jobs_dir else None
    if job_name and (jobs_dir / job_name).exists():
        parser.error("native job already exists; choose a new output name")
    command = [args.harbor, "run", "--dataset", BENCHMARKS[args.benchmark], "--agent", agent, "--jobs-dir", str(jobs_dir), "--n-attempts", str(args.attempts), "--n-concurrent", str(args.concurrency)]
    if job_name:
        command.extend(["--job-name", job_name])
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
        "disk_budget": {"probe_container": args.disk_probe_container, "minimum_bytes": minimum_bytes},
        "jobs_dir": str(jobs_dir), "job_name": job_name,
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
    return_code = run_harbor(command, env, args.disk_probe_container, minimum_bytes)
    exit_code, jobs = execution_outcome(jobs_dir, return_code, job_name)
    status = "resource_exhausted" if return_code == 75 else ("cancelled" if return_code == 130 else ("completed" if exit_code == 0 else "failed"))
    manifest.update(status=status, exit_code=exit_code, harbor_exit_code=return_code, native_jobs=jobs, ended_at=datetime.now(UTC).isoformat())
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"Harbor execution {manifest['status']}: {len(jobs)} job(s); inspect native result.json for rewards", flush=True)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
