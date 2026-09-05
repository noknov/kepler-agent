#!/usr/bin/env python3
"""Fail a CI job when an evaluation summary breaches release thresholds."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def violation(candidate: str, summary: dict, minimum_pass_rate: float, maximum_timeout_rate: float, maximum_p95_seconds: float | None, baseline: dict | None = None, maximum_pass_rate_regression: float = 0) -> list[str]:
    eligible = int(summary.get("eligible", 0))
    if eligible == 0:
        return [f"{candidate}: no eligible cases"]
    failures: list[str] = []
    pass_rate = float(summary.get("weighted_pass_rate", summary.get("pass_rate", 0)))
    timeout_rate = float(summary.get("timeout", 0)) / eligible
    p95_seconds = float(summary.get("p95_duration_seconds", 0))
    if pass_rate < minimum_pass_rate:
        failures.append(f"{candidate}: pass rate {pass_rate:.1%} < {minimum_pass_rate:.1%}")
    if timeout_rate > maximum_timeout_rate:
        failures.append(f"{candidate}: timeout rate {timeout_rate:.1%} > {maximum_timeout_rate:.1%}")
    if maximum_p95_seconds is not None and p95_seconds > maximum_p95_seconds:
        failures.append(f"{candidate}: p95 {p95_seconds:.3f}s > {maximum_p95_seconds:.3f}s")
    if baseline is not None:
        baseline_rate = float(baseline.get("weighted_pass_rate", baseline.get("pass_rate", 0)))
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
    args = parser.parse_args()
    if not 0 <= args.min_pass_rate <= 1 or not 0 <= args.max_timeout_rate <= 1 or not 0 <= args.max_pass_rate_regression <= 1:
        parser.error("rate thresholds must be between 0 and 1")
    candidates = json.loads(args.summary.read_text()).get("candidates", {})
    baseline_candidates = json.loads(args.baseline.read_text()).get("candidates", {}) if args.baseline else {}
    selected = args.candidate or sorted(candidates)
    missing = [name for name in selected if name not in candidates]
    if missing:
        parser.error("unknown candidates: " + ", ".join(missing))
    failures = [item for name in selected for item in violation(name, candidates[name], args.min_pass_rate, args.max_timeout_rate, args.max_p95_seconds, baseline_candidates.get(name), args.max_pass_rate_regression)]
    if failures:
        print("evaluation gate failed:")
        print("\n".join("- " + item for item in failures))
        return 1
    print("evaluation gate passed for " + ", ".join(selected))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
