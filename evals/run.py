#!/usr/bin/env python3
"""Black-box harness evaluator. Standard library only."""

from __future__ import annotations

import argparse
import dataclasses
import json
import os
import random
import shutil
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

@dataclasses.dataclass(frozen=True)
class Candidate:
    name: str
    command: list[str]
    env: dict[str, str]
    files: dict[str, str]
    model: str

@dataclasses.dataclass(frozen=True)
class Task:
    id: str
    prompt: str
    fixture: Path
    test: list[str]
    timeout_seconds: int

def load_candidates(path: Path) -> list[Candidate]:
    data = json.loads(path.read_text())
    return [Candidate(item["name"], item["command"], item.get("env", {}), item.get("files", {}), item.get("model", "{model}")) for item in data["candidates"]]

def load_tasks(path: Path) -> list[Task]:
    data = json.loads(path.read_text())
    base = path.parent
    tasks = []
    for item in data["tasks"]:
        fixture = Path(item["fixture"])
        if not fixture.is_absolute(): fixture = (base / fixture).resolve()
        tasks.append(Task(item["id"], item["prompt"], fixture, item["test"], int(item.get("timeout_seconds", 900))))
    return tasks

def expand(values: list[str], mapping: dict[str, str]) -> list[str]:
    return [render(value, mapping) for value in values]

def render(value: str, mapping: dict[str, str]) -> str:
    for key, replacement in mapping.items():
        value = value.replace("{" + key + "}", replacement)
    return value

def clean_environment(candidate: Candidate, model: str, workspace: Path, home: Path, mapping: dict[str, str]) -> dict[str, str]:
    allowed = ("PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TERM", "SSL_CERT_FILE", "SSL_CERT_DIR")
    env = {key: value for key, value in os.environ.items() if key in allowed and key != "HOME"}
    passthrough = ("EVAL_OPENAI_BASE_URL", "EVAL_ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "ANTHROPIC_API_KEY")
    for key in passthrough:
        if key in os.environ: env[key] = os.environ[key]
    env.update({
        "EVAL_MODEL": model, "OPENAI_MODEL": model, "ANTHROPIC_MODEL": model,
        "OPENAI_BASE_URL": os.environ.get("EVAL_OPENAI_BASE_URL", ""),
        "ANTHROPIC_BASE_URL": os.environ.get("EVAL_ANTHROPIC_BASE_URL", ""),
        "EVAL_WORKSPACE": str(workspace),
        "HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"),
        "XDG_DATA_HOME": str(home / ".local" / "share"), "XDG_STATE_HOME": str(home / ".local" / "state"),
    })
    env.update({key: render(value, mapping) for key, value in candidate.env.items()})
    return env

def run_case(candidate: Candidate, task: Task, model: str, run_root: Path, repetition: int, dry_run: bool) -> dict[str, Any]:
    case_root = run_root / f"{task.id}__{candidate.name}__{repetition}"
    workspace = case_root / "workspace"
    case_root.mkdir(parents=True, exist_ok=False)
    shutil.copytree(task.fixture, workspace, symlinks=True)
    mapping = {
        "workspace": str(workspace), "prompt": task.prompt, "model": model, "case_root": str(case_root),
        "repo": str(Path(__file__).resolve().parent.parent),
        "env_openai_base": os.environ.get("EVAL_OPENAI_BASE_URL", "https://api.openai.com/v1"),
        "env_anthropic_base": os.environ.get("EVAL_ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
    }
    mapping["candidate_model"] = render(candidate.model, mapping)
    home = case_root / "home"
    home.mkdir()
    mapping["home"] = str(home)
    for relative, content in candidate.files.items():
        destination = home / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(render(content, mapping))
    command = expand(candidate.command, mapping)
    record: dict[str, Any] = {"task": task.id, "candidate": candidate.name, "repetition": repetition, "command": command, "model": model}
    if dry_run:
        record["status"] = "dry_run"
        return record
    started = time.monotonic()
    try:
        agent = subprocess.run(command, cwd=workspace, env=clean_environment(candidate, model, workspace, home, mapping), text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=task.timeout_seconds)
        (case_root / "agent.log").write_text(agent.stdout)
        record["agent_exit_code"] = agent.returncode
        if agent.returncode != 0:
            record["status"] = "agent_error"
        else:
            test = subprocess.run(task.test, cwd=workspace, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=task.timeout_seconds)
            (case_root / "test.log").write_text(test.stdout)
            record["test_exit_code"] = test.returncode
            record["status"] = "passed" if test.returncode == 0 else "failed"
    except subprocess.TimeoutExpired as error:
        output = error.stdout or ""
        if isinstance(output, bytes): output = output.decode(errors="replace")
        (case_root / "timeout.log").write_text(output)
        record["status"] = "timeout"
    except OSError as error:
        record["status"] = "launch_error"
        record["error"] = str(error)
    record["duration_seconds"] = round(time.monotonic() - started, 3)
    return record

def summarize(records: list[dict[str, Any]]) -> dict[str, Any]:
    candidates: dict[str, dict[str, Any]] = {}
    for record in records:
        summary = candidates.setdefault(record["candidate"], {"total": 0, "passed": 0, "failed": 0, "timeout": 0, "agent_error": 0, "launch_error": 0, "duration_seconds": 0.0})
        summary["total"] += 1
        status = record["status"]
        if status in summary: summary[status] += 1
        summary["duration_seconds"] += record.get("duration_seconds", 0.0)
    for summary in candidates.values():
        summary["pass_rate"] = summary["passed"] / summary["total"] if summary["total"] else 0
        summary["duration_seconds"] = round(summary["duration_seconds"], 3)
    return {"candidates": candidates, "records": len(records)}

def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--suite", type=Path, required=True)
    parser.add_argument("--candidates", type=Path, required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, default=1)
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--candidate", action="append", default=[])
    parser.add_argument("--task", action="append", default=[])
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    candidates = [item for item in load_candidates(args.candidates) if not args.candidate or item.name in args.candidate]
    tasks = [item for item in load_tasks(args.suite) if not args.task or item.id in args.task]
    if not candidates or not tasks: parser.error("candidate/task selection is empty")
    args.output.mkdir(parents=True, exist_ok=False)
    plan = [(candidate, task, repetition) for repetition in range(1, args.repetitions + 1) for task in tasks for candidate in candidates]
    random.Random(args.seed).shuffle(plan)
    records = []
    with (args.output / "records.jsonl").open("w") as stream:
        for candidate, task, repetition in plan:
            record = run_case(candidate, task, args.model, args.output, repetition, args.dry_run)
            records.append(record)
            stream.write(json.dumps(record, sort_keys=True) + "\n")
            stream.flush()
            print(f'{record["status"]:>12}  {task.id}  {candidate.name}')
    (args.output / "summary.json").write_text(json.dumps(summarize(records), indent=2, sort_keys=True) + "\n")
    return 0 if all(record["status"] in ("passed", "dry_run") for record in records) else 1

if __name__ == "__main__": raise SystemExit(main())
