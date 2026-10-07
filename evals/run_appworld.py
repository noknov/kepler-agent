#!/usr/bin/env python3
"""Evaluate the real Kepler CLI against AppWorld's official MCP and grader.

Run with the isolated AppWorld Python environment. Data and grading stay outside
the agent workspace; the adapter neither generates model calls nor executes tools.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import sys
import tempfile
import time

from kepler_gateway import prepare
from run import git_metadata, kepler_metrics, utc_now


def write_json(path: Path, value: dict) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, default=str) + "\n")
    temporary.replace(path)


def agent_config(mcp_url: str, max_steps: int) -> str:
    return (
        f"max_steps = {max_steps}\n"
        "unsafe_allow_no_sandbox = false\n"
        "[[mcp_servers]]\n"
        'name = "appworld"\n'
        f"url = {json.dumps(mcp_url.rstrip('/') + '/mcp')}\n"
        'effects = ["external_write"]\n'
    )


def task_prompt(task) -> str:
    # Only public task fields. No ground truth, reference actions or grading hints.
    return (
        "Complete this AppWorld task using the appworld MCP tools. "
        "The apps contain simulated data. Discover the tools you need. "
        "Use the task's date/time rather than the host clock, and call "
        "supervisor complete_task when finished.\n\n"
        + json.dumps({"instruction": task.instruction, "supervisor": task.supervisor,
                      "datetime": str(task.datetime)}, default=str)
    )


def agent_environment(preflight: dict) -> dict[str, str]:
    env = {k: v for k, v in os.environ.items() if k in {
        "PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR"}}
    env.update(KEPLER_TOKEN=os.environ["KEPLER_TOKEN"], KEPLER_API_URL=preflight["api_url"])
    return env


def stop_agent(process: subprocess.Popen) -> None:
    if process.poll() is not None:
        return
    os.killpg(process.pid, signal.SIGINT)
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()


def terminal_event(log: str) -> dict:
    terminal = {}
    for line in log.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict) and event.get("type") in {"turn_completed", "turn_failed", "turn_canceled"}:
            terminal = {"terminal_event": event["type"]}
            if event.get("error"):
                terminal["error"] = event["error"]
    return terminal


def execute_agent(command: list[str], workspace: Path, env: dict, log: Path,
                  timeout: float, expected_model: str) -> dict:
    started = time.monotonic()
    status = "completed"
    with log.open("w") as stream:
        process = subprocess.Popen(command, cwd=workspace, env=env, stdin=subprocess.DEVNULL,
                                   stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            while process.poll() is None:
                remaining = timeout - (time.monotonic() - started)
                if remaining <= 0:
                    status = "agent_timeout"
                    break
                try:
                    process.wait(timeout=min(5, remaining))
                except subprocess.TimeoutExpired:
                    # A live gateway is shared with Slack. Never silently score a
                    # run against a different model after an operator switches it.
                    prepare(expected_model)
        finally:
            stop_agent(process)
    transcript = log.read_text()
    return {"status": status if status != "completed" or process.returncode == 0 else "agent_error",
            "exit_code": process.returncode, "duration_seconds": round(time.monotonic() - started, 3),
            "metrics": kepler_metrics(transcript), **terminal_event(transcript)}


def parse_step_limit(value: str) -> int:
    limit = int(value)
    if limit != -1 and limit < 1:
        raise argparse.ArgumentTypeError("max-steps must be positive, or -1 for unlimited")
    return limit


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True, help="Prepared AppWorld data root outside agent workspaces")
    parser.add_argument("--binary-path", type=Path, required=True, help="Native Kepler CLI, built in deploy repository")
    parser.add_argument("--output", type=Path, required=True, help="New invocation directory; never overwrite a run")
    parser.add_argument("--split", choices=["train", "dev", "test_normal", "test_challenge"], default="dev")
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--tasks", type=int, default=3, help="First N tasks in official split order")
    selection.add_argument("--task-id", action="append", default=[])
    selection.add_argument("--all", action="store_true", help="Run every task in the selected official split")
    parser.add_argument("--model")
    parser.add_argument("--timeout-seconds", type=float, default=900)
    parser.add_argument("--max-steps", type=parse_step_limit, default=100,
                        help="Maximum model rounds; -1 disables this limit while keeping the task timeout")
    args = parser.parse_args()
    if args.tasks < 1 or args.timeout_seconds <= 0:
        parser.error("tasks and timeout-seconds must be positive")
    args.root, args.binary_path, args.output = (p.resolve() for p in (args.root, args.binary_path, args.output))
    if not args.binary_path.is_file() or not os.access(args.binary_path, os.X_OK):
        parser.error("binary-path must be an executable native CLI")
    if not (args.root / "data/datasets" / (args.split + ".txt")).is_file():
        parser.error("AppWorld data missing: run appworld install, then appworld download data --root ROOT")
    if args.output.exists():
        parser.error("output already exists; choose a new directory")
    os.environ["APPWORLD_ROOT"] = str(args.root)
    # Import only after setting APPWORLD_ROOT. No dependency on AppWorld in the
    # product runtime, normal eval runner, or standard-library regression suite.
    from appworld import AppWorld, load_task_ids
    from appworld.common.constants import DATA_VERSION
    from appworld.environment import AppWorldServers

    available = load_task_ids(args.split)
    task_ids = available if args.all else args.task_id or available[:args.tasks]
    if len(set(task_ids)) != len(task_ids) or not set(task_ids).issubset(available):
        parser.error("task-id values must be unique members of the selected split")
    preflight = prepare(args.model)
    args.output.mkdir(parents=True)
    version = importlib.metadata.distribution("appworld")
    manifest = {"benchmark": "appworld", "split": args.split, "task_ids": task_ids,
                "status": "running", "created_at": utc_now(), "gateway": preflight,
                "python": sys.version, "platform": platform.platform(),
                "appworld_version": version.version,
                "appworld_data_version": DATA_VERSION,
                "appworld_revision": json.loads((args.root / "source.json").read_text()) if (args.root / "source.json").is_file() else None,
                "appworld_source": json.loads(version.read_text("direct_url.json") or "null"),
                "binary_sha256": hashlib.sha256(args.binary_path.read_bytes()).hexdigest(),
                "repository": git_metadata(), "max_steps": args.max_steps,
                "timeout_seconds": args.timeout_seconds, "interface": "official-mcp",
                "results": [], "scope": ("full split via official MCP; not a leaderboard submission"
                                         if set(task_ids) == set(available)
                                         else "subset smoke test; not a leaderboard score")}
    manifest_path = args.output / "invocation.json"
    write_json(manifest_path, manifest)
    # Child tools inherit no AppWorld paths, provider credentials, or operator
    # secrets. Only the gateway token is supplied to the Kepler parent process.
    env = agent_environment(preflight)
    experiment = args.output.name
    try:
        # The environment freezes task time. Keep it in another process so the
        # launcher's monotonic deadlines and gateway authentication stay real.
        with AppWorldServers(experiment_name=experiment, remote_apis_port="{port}",
                             remote_environment_port="{port}",
                             remote_mcp_port="{port}", mcp_server_kwargs={"output_type": "content_only"}) as servers:
            for task_id in task_ids:
                prepare(preflight["model"])
                directory = args.output / task_id
                directory.mkdir()
                result = {"task_id": task_id, "status": "running", "started_at": utc_now()}
                manifest["results"].append(result)
                write_json(manifest_path, manifest)
                print(json.dumps(result), flush=True)
                with tempfile.TemporaryDirectory(prefix="kepler-appworld-") as folder:
                    workspace = Path(folder).resolve()
                    if args.root.is_relative_to(workspace) or args.output.is_relative_to(workspace):
                        raise ValueError("AppWorld data and evaluator outputs must be outside the agent workspace")
                    config = directory / "cli.toml"
                    config.write_text(agent_config(servers.defaults["remote_mcp_url"], args.max_steps))
                    with AppWorld(task_id=task_id, **servers.defaults) as world:
                        command = [str(args.binary_path), "--cwd", str(workspace), "--config", str(config),
                                   "--state-dir", str(directory / "state"), "--approval", "project",
                                   "--output", "jsonl", "--", task_prompt(world.task)]
                        result.update(execute_agent(command, workspace, env, directory / "agent.jsonl",
                                                    args.timeout_seconds, preflight["model"]))
                        # Direct MCP mutations must be persisted explicitly before
                        # the official grader loads final database state.
                        world.save()
                        tracker = world.evaluate()
                        evaluation = tracker.to_dict()
                        write_json(directory / "evaluation.json", evaluation)
                        result["success"] = tracker.success
                        result["evaluation_file"] = str(directory / "evaluation.json")
                        result["task_completed"] = world.task_completed()
                        prepare(preflight["model"])
                result["finished_at"] = utc_now()
                write_json(manifest_path, manifest)
                print(json.dumps(result, default=str), flush=True)
        manifest["status"] = "completed" if all(r["status"] == "completed" for r in manifest["results"]) else "execution_failed"
        manifest["summary"] = {"tasks": len(task_ids),
                               "passed": sum(r.get("success") is True for r in manifest["results"]),
                               "execution_errors": sum(r["status"] != "completed" for r in manifest["results"])}
    except (Exception, KeyboardInterrupt) as error:
        manifest["status"] = "canceled" if isinstance(error, KeyboardInterrupt) else "failed"
        manifest["error"] = {"type": type(error).__name__, "message": str(error)}
        for result in manifest["results"]:
            if result["status"] == "running":
                result.update(status=manifest["status"], finished_at=utc_now())
        print(json.dumps(manifest["error"]), flush=True)
    finally:
        manifest["finished_at"] = utc_now()
        write_json(manifest_path, manifest)
    return 0 if manifest["status"] == "completed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
