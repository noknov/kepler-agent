"""Pure evaluation aggregation shared by the runner and evidence validation."""
from __future__ import annotations

import math
import statistics
from typing import Any

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

def wilson_interval(passed: int, total: int, z: float = 1.959963984540054) -> list[float]:
    if total <= 0:
        return [0.0, 0.0]
    rate = passed / total
    denominator = 1 + z * z / total
    center = (rate + z * z / (2 * total)) / denominator
    margin = z * math.sqrt(rate * (1 - rate) / total + z * z / (4 * total * total)) / denominator
    return [round(max(0.0, center - margin), 6), round(min(1.0, center + margin), 6)]

def finalize_summary(summary: dict[str, Any]) -> None:
    eligible = summary["eligible"]
    summary["pass_rate"] = summary["passed"] / eligible if eligible else 0
    summary["pass_rate_confidence_95"] = wilson_interval(summary["passed"], eligible)
    summary["weighted_pass_rate"] = summary["weighted_passed"] / summary["weighted_total"] if summary["weighted_total"] else 0
    durations = summary.pop("_durations")
    summary["duration_samples"] = len(durations)
    summary["duration_seconds"] = round(summary["duration_seconds"], 3)
    summary["median_duration_seconds"] = round(statistics.median(durations), 3) if durations else 0.0
    summary["p95_duration_seconds"] = round(percentile(durations, 0.95), 3)

