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
    category: str
    source: str
    prompt: str
    fixture: Path
    test: list[str]
    timeout_seconds: int
    tags: list[str]
    metadata: dict[str, Any]

def load_candidates(path: Path) -> list[Candidate]:
    data = json.loads(path.read_text())
    return [Candidate(item["name"], item["command"], item.get("env", {}), item.get("files", {}), item.get("model", "{model}")) for item in data["candidates"]]

def require_string(item: dict[str, Any], field: str) -> str:
    value = item.get(field)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"task {item.get('id', '<unknown>')} requires non-empty {field}")
    return value

def require_string_list(item: dict[str, Any], field: str) -> list[str]:
    value = item.get(field)
    if not isinstance(value, list) or not all(isinstance(part, str) and part for part in value):
        raise ValueError(f"task {item.get('id', '<unknown>')} requires string list {field}")
    return value

def load_tasks(path: Path) -> list[Task]:
    data = json.loads(path.read_text())
    if data.get("schema_version") != 1:
        raise ValueError(f"{path} must declare schema_version: 1")
    if not isinstance(data.get("name"), str) or not data["name"].strip():
        raise ValueError(f"{path} must declare non-empty name")
    base = path.parent
    tasks = []
    for item in data["tasks"]:
        task_id = require_string(item, "id")
        category = require_string(item, "category")
        source = require_string(item, "source")
        prompt = require_string(item, "prompt")
        test = require_string_list(item, "test")
        tags = require_string_list(item, "tags")
        metadata = item.get("metadata")
        if not isinstance(metadata, dict):
            raise ValueError(f"task {task_id} requires metadata object")
        fixture = Path(require_string(item, "fixture"))
        if not fixture.is_absolute(): fixture = (base / fixture).resolve()
        tasks.append(Task(task_id, category, source, prompt, fixture, test, int(item["timeout_seconds"]), tags, metadata))
    return tasks

def filter_tasks(tasks: list[Task], ids: list[str], categories: list[str], sources: list[str], tags: list[str]) -> list[Task]:
    id_set = set(ids)
    category_set = set(categories)
    source_set = set(sources)
    tag_set = set(tags)
    out = []
    for task in tasks:
        if id_set and task.id not in id_set:
            continue
        if category_set and task.category not in category_set:
            continue
        if source_set and task.source not in source_set:
            continue
        if tag_set and not tag_set.intersection(task.tags):
            continue
        out.append(task)
    return out

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

def capture_workspace_diff(original: Path, workspace: Path, case_root: Path) -> str:
    diff_path = case_root / "workspace.diff"
    try:
        diff = subprocess.run(["diff", "-ruN", str(original), str(workspace)], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        diff_path.write_text(diff.stdout)
    except (OSError, subprocess.TimeoutExpired) as error:
        diff_path.write_text(f"workspace diff unavailable: {error}\n")
    return str(diff_path)

def run_case(candidate: Candidate, task: Task, model: str, run_root: Path, repetition: int, dry_run: bool) -> dict[str, Any]:
    case_root = run_root / f"{task.id}__{candidate.name}__{repetition}"
    original = case_root / "original"
    workspace = case_root / "workspace"
    case_root.mkdir(parents=True, exist_ok=False)
    shutil.copytree(task.fixture, original, symlinks=True)
    shutil.copytree(original, workspace, symlinks=True)
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
    record: dict[str, Any] = {
        "task": task.id, "category": task.category, "source": task.source,
        "tags": task.tags, "metadata": task.metadata,
        "candidate": candidate.name, "repetition": repetition, "command": command, "model": model,
    }
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
    record["workspace_diff"] = capture_workspace_diff(original, workspace, case_root)
    record["duration_seconds"] = round(time.monotonic() - started, 3)
    return record

def summarize(records: list[dict[str, Any]]) -> dict[str, Any]:
    candidates: dict[str, dict[str, Any]] = {}
    categories: dict[str, dict[str, Any]] = {}
    for record in records:
        summary = candidates.setdefault(record["candidate"], {"total": 0, "passed": 0, "failed": 0, "timeout": 0, "agent_error": 0, "launch_error": 0, "duration_seconds": 0.0})
        summary["total"] += 1
        status = record["status"]
        if status in summary: summary[status] += 1
        summary["duration_seconds"] += record.get("duration_seconds", 0.0)
        category_key = f'{record["candidate"]}:{record["category"]}'
        category = categories.setdefault(category_key, {"candidate": record["candidate"], "category": record["category"], "total": 0, "passed": 0, "failed": 0, "timeout": 0, "agent_error": 0, "launch_error": 0})
        category["total"] += 1
        if status in category: category[status] += 1
    for summary in candidates.values():
        summary["pass_rate"] = summary["passed"] / summary["total"] if summary["total"] else 0
        summary["duration_seconds"] = round(summary["duration_seconds"], 3)
    for category in categories.values():
        category["pass_rate"] = category["passed"] / category["total"] if category["total"] else 0
    return {"candidates": candidates, "categories": categories, "records": len(records)}

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
    parser.add_argument("--category", action="append", default=[])
    parser.add_argument("--source", action="append", default=[])
    parser.add_argument("--tag", action="append", default=[])
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    candidates = [item for item in load_candidates(args.candidates) if not args.candidate or item.name in args.candidate]
    tasks = filter_tasks(load_tasks(args.suite), args.task, args.category, args.source, args.tag)
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
