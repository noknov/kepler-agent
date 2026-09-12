#!/usr/bin/env python3
"""Fail a CI job when an evaluation summary breaches release thresholds."""

from __future__ import annotations

import argparse
import json
import math
from pathlib import Path


def validate_artifacts(summary_path: Path, document: dict) -> list[str]:
    run_path = summary_path.with_name("run.json")
    records_path = summary_path.with_name("records.jsonl")
    if not run_path.is_file() or not records_path.is_file():
        return ["evaluation artifacts are incomplete (run.json or records.jsonl missing)"]
    run = json.loads(run_path.read_text())
    if run.get("status") != "completed" or run.get("ended_at") is None:
        return ["evaluation run is not complete"]
    if run.get("inputs", {}).get("dry_run") is not False:
        return ["dry-run results cannot be used as release evidence"]
    records = [json.loads(line) for line in records_path.read_text().splitlines() if line.strip()]
    if len(records) != document.get("records"):
        return ["record count does not match summary"]
    failures: list[str] = []
    for candidate, summary in document.get("candidates", {}).items():
        selected = [record for record in records if record.get("candidate") == candidate]
        eligible = [record for record in selected if record.get("status") != "skipped"]
        passed = [record for record in eligible if record.get("status") == "passed"]
        timeout = [record for record in eligible if record.get("status") == "timeout"]
        weighted_total = sum(float(record.get("weight", 1)) for record in eligible)
        weighted_passed = sum(float(record.get("weight", 1)) for record in passed)
        expected = (len(selected), len(eligible), len(passed), len(timeout))
        actual = tuple(int(summary.get(key, -1)) for key in ("total", "eligible", "passed", "timeout"))
        rate = weighted_passed / weighted_total if weighted_total else 0
        if actual != expected or not math.isclose(float(summary.get("weighted_pass_rate", -1)), rate, abs_tol=1e-9):
            failures.append(f"{candidate}: summary does not match raw records")
    return failures


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
    if not 0 <= args.min_pass_rate <= 1 or not 0 <= args.max_timeout_rate <= 1 or not 0 <= args.max_pass_rate_regression <= 1 or not 0 <= args.min_coverage <= 1 or args.min_eligible < 1:
        parser.error("rate thresholds must be between 0 and 1")
    document = json.loads(args.summary.read_text())
    artifact_failures = validate_artifacts(args.summary, document)
    if artifact_failures:
        parser.error("; ".join(artifact_failures))
    candidates = document.get("candidates", {})
    baseline_candidates = json.loads(args.baseline.read_text()).get("candidates", {}) if args.baseline else {}
    selected = args.candidate or sorted(candidates)
    if not selected: parser.error("evaluation summary has no candidates")
    missing = [name for name in selected if name not in candidates]
    if missing:
        parser.error("unknown candidates: " + ", ".join(missing))
    if args.baseline:
        missing_baseline = [name for name in selected if name not in baseline_candidates]
        if missing_baseline: parser.error("baseline missing candidates: " + ", ".join(missing_baseline))
        current_identity = document.get("evaluation_identity")
        baseline_identity = json.loads(args.baseline.read_text()).get("evaluation_identity")
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
