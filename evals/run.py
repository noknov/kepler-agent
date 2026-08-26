#!/usr/bin/env python3
"""Black-box harness evaluator. Standard library only."""

from __future__ import annotations

import argparse
import dataclasses
import datetime
import json
import os
import random
import shutil
import statistics
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent

@dataclasses.dataclass(frozen=True)
class Candidate:
    name: str
    command: list[str]
    env: dict[str, str]
    files: dict[str, str]
    model: str
    version_command: list[str]
    capabilities: frozenset[str]

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
    required_capabilities: frozenset[str]
    weight: float

def load_candidates(path: Path) -> list[Candidate]:
    data = json.loads(path.read_text())
    candidates = []
    for item in data["candidates"]:
        capabilities = item.get("capabilities", [])
        if not isinstance(capabilities, list) or not all(isinstance(value, str) and value for value in capabilities):
            raise ValueError(f"candidate {item.get('name', '<unknown>')} capabilities must be a string list")
        candidates.append(Candidate(
            item["name"], item["command"], item.get("env", {}), item.get("files", {}),
            item.get("model", "{model}"), item.get("version_command", []), frozenset(capabilities),
        ))
    return candidates

def utc_now() -> str:
    return datetime.datetime.now(datetime.UTC).isoformat(timespec="seconds").replace("+00:00", "Z")

def git_output(args: list[str]) -> str:
    return subprocess.check_output(["git", "-C", str(ROOT), *args], text=True, stderr=subprocess.STDOUT, timeout=10).strip()

def git_metadata() -> dict[str, Any]:
    try:
        status = git_output(["status", "--porcelain"])
        return {
            "commit": git_output(["rev-parse", "HEAD"]),
            "branch": git_output(["branch", "--show-current"]),
            "dirty": bool(status),
            "status_porcelain": status.splitlines(),
        }
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        return {"error": str(error)}

def write_run_manifest(path: Path, manifest: dict[str, Any]) -> None:
    path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")

def probe_environment(home: Path) -> dict[str, str]:
    allowed = ("PATH", "TMPDIR", "LANG", "LC_ALL", "TERM", "SSL_CERT_FILE", "SSL_CERT_DIR")
    env = {key: value for key, value in os.environ.items() if key in allowed}
    env.update({
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "XDG_DATA_HOME": str(home / ".local" / "share"),
        "XDG_STATE_HOME": str(home / ".local" / "state"),
    })
    return env

def candidate_versions(candidates: list[Candidate], mapping: dict[str, str]) -> dict[str, dict[str, Any]]:
    versions: dict[str, dict[str, Any]] = {}
    with tempfile.TemporaryDirectory(prefix="kepler-agent-version-") as temp:
        home = Path(temp) / "home"
        home.mkdir()
        env = probe_environment(home)
        scoped_mapping = dict(mapping)
        scoped_mapping["home"] = str(home)
        for candidate in candidates:
            record: dict[str, Any] = {"command": expand(candidate.version_command, scoped_mapping) if candidate.version_command else []}
            if not candidate.version_command:
                record["status"] = "not_configured"
                versions[candidate.name] = record
                continue
            try:
                result = subprocess.run(record["command"], cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
                record["status"] = "ok" if result.returncode == 0 else "error"
                record["exit_code"] = result.returncode
                record["output"] = result.stdout.strip()
            except (OSError, subprocess.TimeoutExpired) as error:
                record["status"] = "error"
                record["error"] = str(error)
            versions[candidate.name] = record
    return versions

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

def optional_string_list(item: dict[str, Any], field: str) -> list[str]:
    value = item.get(field, [])
    if not isinstance(value, list) or not all(isinstance(part, str) and part for part in value):
        raise ValueError(f"task {item.get('id', '<unknown>')} {field} must be a string list")
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
        required_capabilities = frozenset(optional_string_list(item, "required_capabilities"))
        weight = float(item.get("weight", 1))
        if weight <= 0:
            raise ValueError(f"task {task_id} weight must be positive")
        tasks.append(Task(task_id, category, source, prompt, fixture, test, int(item["timeout_seconds"]), tags, metadata, required_capabilities, weight))
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

def set_status(record: dict[str, Any], status: str) -> None:
    record["status"] = status
    failure_classes = {
        "launch_error": "launch",
        "agent_error": "agent_exit",
        "timeout": "timeout",
        "failed": "test_failure",
    }
    if status in failure_classes:
        record["failure_class"] = failure_classes[status]

def run_case(candidate: Candidate, task: Task, model: str, run_root: Path, repetition: int, dry_run: bool, versions: dict[str, dict[str, Any]]) -> dict[str, Any]:
    case_root = run_root / f"{task.id}__{candidate.name}__{repetition}"
    original = case_root / "original"
    workspace = case_root / "workspace"
    case_root.mkdir(parents=True, exist_ok=False)
    shutil.copytree(task.fixture, original, symlinks=True)
    shutil.copytree(original, workspace, symlinks=True)
    mapping = {
        "workspace": str(workspace), "prompt": task.prompt, "model": model, "case_root": str(case_root),
        "repo": str(ROOT),
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
        "tags": task.tags, "metadata": task.metadata, "required_capabilities": sorted(task.required_capabilities), "weight": task.weight,
        "candidate": candidate.name, "repetition": repetition, "command": command, "model": model,
        "candidate_version": versions.get(candidate.name, {}), "candidate_capabilities": sorted(candidate.capabilities),
    }
    missing_capabilities = sorted(task.required_capabilities - candidate.capabilities)
    if missing_capabilities:
        set_status(record, "skipped")
        record["skip_reason"] = "missing_capabilities"
        record["missing_capabilities"] = missing_capabilities
        return record
    if dry_run:
        set_status(record, "dry_run")
        return record
    started = time.monotonic()
    try:
        agent = subprocess.run(command, cwd=workspace, env=clean_environment(candidate, model, workspace, home, mapping), text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=task.timeout_seconds)
        (case_root / "agent.log").write_text(agent.stdout)
        record["agent_exit_code"] = agent.returncode
        if agent.returncode != 0:
            set_status(record, "agent_error")
        else:
            test = subprocess.run(task.test, cwd=workspace, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=task.timeout_seconds)
            (case_root / "test.log").write_text(test.stdout)
            record["test_exit_code"] = test.returncode
            set_status(record, "passed" if test.returncode == 0 else "failed")
    except subprocess.TimeoutExpired as error:
        output = error.stdout or ""
        if isinstance(output, bytes): output = output.decode(errors="replace")
        (case_root / "timeout.log").write_text(output)
        set_status(record, "timeout")
    except OSError as error:
        set_status(record, "launch_error")
        record["error"] = str(error)
    record["workspace_diff"] = capture_workspace_diff(original, workspace, case_root)
    record["duration_seconds"] = round(time.monotonic() - started, 3)
    return record

def summarize(records: list[dict[str, Any]], versions: dict[str, dict[str, Any]]) -> dict[str, Any]:
    candidates: dict[str, dict[str, Any]] = {}
    categories: dict[str, dict[str, Any]] = {}
    tags: dict[str, dict[str, Any]] = {}
    profiles: dict[str, list[str]] = {}
    for record in records:
        candidate_name = record["candidate"]
        profiles[candidate_name] = record.get("candidate_capabilities", [])
        summary = candidates.setdefault(candidate_name, new_summary())
        accumulate(summary, record)
        category_key = f'{candidate_name}:{record["category"]}'
        category = categories.setdefault(category_key, {**new_summary(), "candidate": candidate_name, "category": record["category"]})
        accumulate(category, record)
        for tag in record["tags"]:
            tag_key = f"{candidate_name}:{tag}"
            tag_summary = tags.setdefault(tag_key, {**new_summary(), "candidate": candidate_name, "tag": tag})
            accumulate(tag_summary, record)
    for summary in [*candidates.values(), *categories.values(), *tags.values()]:
        finalize_summary(summary)
    return {
        "candidates": candidates,
        "categories": categories,
        "tags": tags,
        "candidate_capabilities": profiles,
        "candidate_versions": versions,
        "records": len(records),
    }

def new_summary() -> dict[str, Any]:
    return {
        "total": 0, "eligible": 0, "passed": 0, "failed": 0, "timeout": 0,
        "agent_error": 0, "launch_error": 0, "dry_run": 0, "skipped": 0,
        "duration_seconds": 0.0, "weighted_total": 0.0, "weighted_passed": 0.0,
        "_durations": [],
    }

def accumulate(summary: dict[str, Any], record: dict[str, Any]) -> None:
    summary["total"] += 1
    status = record["status"]
    if status in summary:
        summary[status] += 1
    if status != "skipped":
        summary["eligible"] += 1
        weight = float(record.get("weight", 1))
        summary["weighted_total"] += weight
        if status == "passed":
            summary["weighted_passed"] += weight
    if "duration_seconds" in record:
        duration = float(record["duration_seconds"])
        summary["duration_seconds"] += duration
        summary["_durations"].append(duration)
    failure_class = record.get("failure_class")
    if failure_class:
        classes = summary.setdefault("failure_classes", {})
        classes[failure_class] = classes.get(failure_class, 0) + 1

def percentile(values: list[float], fraction: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    position = (len(ordered) - 1) * fraction
    lower, upper = int(position), min(int(position) + 1, len(ordered) - 1)
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)

def finalize_summary(summary: dict[str, Any]) -> None:
    eligible = summary["eligible"]
    summary["pass_rate"] = summary["passed"] / eligible if eligible else 0
    summary["weighted_pass_rate"] = summary["weighted_passed"] / summary["weighted_total"] if summary["weighted_total"] else 0
    durations = summary.pop("_durations")
    summary["duration_seconds"] = round(summary["duration_seconds"], 3)
    summary["median_duration_seconds"] = round(statistics.median(durations), 3) if durations else 0.0
    summary["p95_duration_seconds"] = round(percentile(durations, 0.95), 3)
    summary["weighted_total"] = round(summary["weighted_total"], 3)
    summary["weighted_passed"] = round(summary["weighted_passed"], 3)

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
    if args.repetitions <= 0:
        parser.error("--repetitions must be positive")
    candidates = [item for item in load_candidates(args.candidates) if not args.candidate or item.name in args.candidate]
    tasks = filter_tasks(load_tasks(args.suite), args.task, args.category, args.source, args.tag)
    if not candidates or not tasks: parser.error("candidate/task selection is empty")
    args.output.mkdir(parents=True, exist_ok=False)
    run_manifest = {
        "schema_version": 1,
        "status": "running",
        "started_at": utc_now(),
        "ended_at": None,
        "repo": str(ROOT),
        "git": git_metadata(),
        "inputs": {
            "suite": str(args.suite),
            "candidates": str(args.candidates),
            "model": args.model,
            "dry_run": args.dry_run,
            "repetitions": args.repetitions,
            "seed": args.seed,
            "filters": {
                "candidate": args.candidate,
                "task": args.task,
                "category": args.category,
                "source": args.source,
                "tag": args.tag,
            },
        },
        "selection": {
            "candidates": [candidate.name for candidate in candidates],
            "candidate_capabilities": {candidate.name: sorted(candidate.capabilities) for candidate in candidates},
            "tasks": [task.id for task in tasks],
        },
        "outputs": {
            "summary": "summary.json",
            "records": "records.jsonl",
            "candidate_versions": "candidate_versions.json",
            "report": "report.html",
            "cases": "<task>__<candidate>__<repetition>/",
        },
    }
    write_run_manifest(args.output / "run.json", run_manifest)
    versions = candidate_versions(candidates, {"repo": str(ROOT), "model": args.model})
    (args.output / "candidate_versions.json").write_text(json.dumps(versions, indent=2, sort_keys=True) + "\n")
    plan = [(candidate, task, repetition) for repetition in range(1, args.repetitions + 1) for task in tasks for candidate in candidates]
    random.Random(args.seed).shuffle(plan)
    run_manifest["plan"] = {
        "cases": len(plan),
        "order": [{"candidate": candidate.name, "task": task.id, "repetition": repetition} for candidate, task, repetition in plan],
    }
    write_run_manifest(args.output / "run.json", run_manifest)
    records = []
    with (args.output / "records.jsonl").open("w") as stream:
        for candidate, task, repetition in plan:
            record = run_case(candidate, task, args.model, args.output, repetition, args.dry_run, versions)
            records.append(record)
            stream.write(json.dumps(record, sort_keys=True) + "\n")
            stream.flush()
            print(f'{record["status"]:>12}  {task.id}  {candidate.name}')
    summary = summarize(records, versions)
    (args.output / "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
    ok = all(record["status"] in ("passed", "dry_run") for record in records)
    run_manifest["status"] = "passed" if ok else "failed"
    run_manifest["ended_at"] = utc_now()
    run_manifest["summary"] = {
        "records": summary["records"],
        "candidates": summary["candidates"],
    }
    write_run_manifest(args.output / "run.json", run_manifest)
    return 0 if ok else 1

if __name__ == "__main__": raise SystemExit(main())
