#!/usr/bin/env python3
"""Fail a CI job when an evaluation summary breaches release thresholds."""

from __future__ import annotations

import argparse
import json
import math
from pathlib import Path
from collections import Counter

from scoring import summarize


def case_identity(record: dict) -> tuple[str, str, int]:
    candidate, task, repetition = (record.get(key) for key in ("candidate", "task", "repetition"))
    if not isinstance(candidate, str) or not candidate or not isinstance(task, str) or not task or type(repetition) is not int or repetition < 1:
        raise ValueError("invalid candidate/task/repetition identity")
    return candidate, task, repetition


def validate_artifacts(summary_path: Path, document: dict) -> list[str]:
    """Recompute evidence, including complete case coverage and measured latency."""
    try:
        return _validate_artifacts(summary_path, document)
    except (OSError, ValueError, KeyError, TypeError, AttributeError, OverflowError):
        return ["evaluation artifacts are missing, malformed or internally inconsistent"]


def _validate_artifacts(summary_path: Path, document: dict) -> list[str]:
    run = json.loads(summary_path.with_name("run.json").read_text())
    if run.get("status") != "completed" or not run.get("ended_at"):
        return ["evaluation run is not complete"]
    if run.get("inputs", {}).get("dry_run") is not False:
        return ["dry-run results cannot be used as release evidence"]
    identity = document.get("evaluation_identity")
    if not identity or identity != run.get("evaluation_identity"):
        return ["summary and run evaluation identities differ or are missing"]
    records = [json.loads(line) for line in summary_path.with_name("records.jsonl").read_text().splitlines() if line.strip()]
    plan = run["plan"]
    expected = [case_identity(case) for case in plan["order"]]
    actual = [case_identity(record) for record in records]
    if not expected or type(plan["cases"]) is not int or len(expected) != plan["cases"] or len(expected) != len(set(expected)):
        return ["evaluation plan is empty, duplicated or inconsistent"]
    if Counter(actual) != Counter(expected):
        return ["raw records do not cover each planned case exactly once"]
    if type(document.get("records")) is not int or len(records) != document["records"]:
        return ["record count does not match summary"]
    if type(identity.get("repetitions")) is not int or identity["repetitions"] < 1:
        return ["evaluation identity has invalid repetitions"]
    for key in ("candidates", "tasks"):
        values = identity[key]
        if not isinstance(values, list) or not values or any(not isinstance(value, str) or not value for value in values) or len(set(values)) != len(values):
            return ["evaluation identity has invalid task/candidate selection"]
    planned_candidates = set(identity["candidates"])
    if set(document["candidates"]) != planned_candidates or {case[0] for case in expected} != planned_candidates:
        return ["candidate coverage does not match evaluation identity"]
    planned_cases = {(candidate, task, repetition) for candidate in identity["candidates"] for task in identity["tasks"] for repetition in range(1, identity["repetitions"] + 1)}
    if set(expected) != planned_cases:
        return ["evaluation plan does not match task/candidate/repetition selection"]
    statuses = {"passed", "failed", "timeout", "agent_error", "launch_error", "skipped"}
    for record in records:
        if record["status"] not in statuses:
            return ["raw record has an invalid or dry-run outcome"]
        if record["status"] == "passed" and (record.get("agent_exit_code") != 0 or record.get("test_exit_code") != 0):
            return ["passed case has no successful agent and grader exit"]
        weight = record.get("weight", 1)
        if isinstance(weight, bool) or not isinstance(weight, (int, float)) or not math.isfinite(weight) or weight <= 0:
            return ["raw record has an invalid weight"]
        duration = record.get("duration_seconds")
        # A failed version preflight occurs before any process launches. It has
        # no measured duration and must not be assigned a fabricated latency.
        preflight_failure = record["status"] == "launch_error" and "agent_exit_code" not in record
        if record["status"] != "skipped" and not preflight_failure and duration is None:
            return ["launched case has no measured duration"]
        if duration is not None and (isinstance(duration, bool) or not isinstance(duration, (int, float)) or not math.isfinite(duration) or duration < 0):
            return ["raw record has an invalid duration"]
    computed = summarize(records, document.get("candidate_versions", {}))
    for candidate, summary in computed["candidates"].items():
        if json.dumps(document["candidates"][candidate], sort_keys=True, allow_nan=False) != json.dumps(summary, sort_keys=True, allow_nan=False):
            return [f"{candidate}: summary does not match raw outcomes, weights and latencies"]
    return []


def violation(candidate: str, summary: dict, minimum_pass_rate: float, maximum_timeout_rate: float, maximum_p95_seconds: float | None, baseline: dict | None = None, maximum_pass_rate_regression: float = 0, minimum_eligible: int = 1, minimum_coverage: float = 1.0) -> list[str]:
    required = ("total", "eligible", "passed", "timeout", "p95_duration_seconds")
    missing = [field for field in required if field not in summary]
    if "weighted_pass_rate" not in summary and "pass_rate" not in summary: missing.append("weighted_pass_rate/pass_rate")
    if missing: return [f"{candidate}: invalid summary missing {', '.join(missing)}"]
    eligible = int(summary.get("eligible", 0))
    total = int(summary.get("total", 0))
    if eligible < minimum_eligible: return [f"{candidate}: eligible cases {eligible} < {minimum_eligible}"]
    if total <= 0 or eligible < 0 or eligible > total: return [f"{candidate}: invalid total/eligible counts"]
    failures: list[str] = []
    pass_rate = float(summary.get("weighted_pass_rate", summary.get("pass_rate", 0)))
    timeout_rate = float(summary.get("timeout", 0)) / eligible
    p95_seconds = float(summary.get("p95_duration_seconds", 0))
    coverage = eligible / total
    if not all(math.isfinite(value) for value in (pass_rate, timeout_rate, p95_seconds, coverage)) or not 0 <= pass_rate <= 1 or p95_seconds < 0:
        return [f"{candidate}: invalid non-finite or out-of-range metrics"]
    if int(summary.get("passed", 0)) < 0 or int(summary.get("passed", 0)) > eligible or int(summary.get("timeout", 0)) < 0 or int(summary.get("timeout", 0)) > eligible:
        return [f"{candidate}: invalid outcome counts"]
    if coverage < minimum_coverage: failures.append(f"{candidate}: coverage {coverage:.1%} < {minimum_coverage:.1%}")
    if pass_rate < minimum_pass_rate:
        failures.append(f"{candidate}: pass rate {pass_rate:.1%} < {minimum_pass_rate:.1%}")
    if timeout_rate > maximum_timeout_rate:
        failures.append(f"{candidate}: timeout rate {timeout_rate:.1%} > {maximum_timeout_rate:.1%}")
    if maximum_p95_seconds is not None and summary.get("duration_samples") == 0:
        failures.append(f"{candidate}: latency threshold has no measured samples")
    if maximum_p95_seconds is not None and p95_seconds > maximum_p95_seconds:
        failures.append(f"{candidate}: p95 {p95_seconds:.3f}s > {maximum_p95_seconds:.3f}s")
    if baseline is not None:
        baseline_rate = float(baseline.get("weighted_pass_rate", baseline.get("pass_rate", 0)))
        if not math.isfinite(baseline_rate) or not 0 <= baseline_rate <= 1:
            failures.append(f"{candidate}: invalid baseline pass rate")
            return failures
        if baseline_rate - pass_rate > maximum_pass_rate_regression:
            failures.append(f"{candidate}: pass rate regressed {(baseline_rate - pass_rate):.1%} from baseline (limit {maximum_pass_rate_regression:.1%})")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("summary", type=Path, help="eval summary.json")
    parser.add_argument("--candidate", action="append", default=[], help="candidate to gate (defaults to all)")
    parser.add_argument("--min-pass-rate", type=float, required=True)
    parser.add_argument("--max-timeout-rate", type=float, default=0.05)
    parser.add_argument("--max-p95-seconds", type=float)
    parser.add_argument("--baseline", type=Path, help="previous compatible summary.json")
    parser.add_argument("--max-pass-rate-regression", type=float, default=0.0)
    parser.add_argument("--min-eligible", type=int, default=1)
    parser.add_argument("--min-coverage", type=float, default=1.0)
    args = parser.parse_args()
    if args.max_p95_seconds is not None and (not math.isfinite(args.max_p95_seconds) or args.max_p95_seconds < 0):
        parser.error("latency threshold must be finite and non-negative")
    if not 0 <= args.min_pass_rate <= 1 or not 0 <= args.max_timeout_rate <= 1 or not 0 <= args.max_pass_rate_regression <= 1 or not 0 <= args.min_coverage <= 1 or args.min_eligible < 1:
        parser.error("rate thresholds must be between 0 and 1")
    document = json.loads(args.summary.read_text())
    artifact_failures = validate_artifacts(args.summary, document)
    if artifact_failures:
        parser.error("; ".join(artifact_failures))
    candidates = document.get("candidates", {})
    baseline_document = json.loads(args.baseline.read_text()) if args.baseline else None
    if args.baseline:
        baseline_failures = validate_artifacts(args.baseline, baseline_document)
        if baseline_failures:
            parser.error("invalid baseline: " + "; ".join(baseline_failures))
    baseline_candidates = baseline_document.get("candidates", {}) if baseline_document else {}
    selected = args.candidate or sorted(candidates)
    if not selected: parser.error("evaluation summary has no candidates")
    missing = [name for name in selected if name not in candidates]
    if missing:
        parser.error("unknown candidates: " + ", ".join(missing))
    if args.baseline:
        missing_baseline = [name for name in selected if name not in baseline_candidates]
        if missing_baseline: parser.error("baseline missing candidates: " + ", ".join(missing_baseline))
        current_identity = document.get("evaluation_identity")
        baseline_identity = baseline_document.get("evaluation_identity")
        if not current_identity or current_identity != baseline_identity: parser.error("baseline is not evaluation-compatible")
    failures = [item for name in selected for item in violation(name, candidates[name], args.min_pass_rate, args.max_timeout_rate, args.max_p95_seconds, baseline_candidates.get(name), args.max_pass_rate_regression, args.min_eligible, args.min_coverage)]
    if failures:
        print("evaluation gate failed:")
        print("\n".join("- " + item for item in failures))
        return 1
    print("evaluation gate passed for " + ", ".join(selected))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
