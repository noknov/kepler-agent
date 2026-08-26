#!/usr/bin/env python3
"""Regression tests for the black-box evaluator contracts."""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


EVALS = Path(__file__).resolve().parent


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


runner = load_module("eval_runner", EVALS / "run.py")
report = load_module("eval_report", EVALS / "report.py")


class EvaluatorTests(unittest.TestCase):
    def test_summary_excludes_incompatible_cases_from_pass_rate(self) -> None:
        records = [
            {"candidate": "a", "status": "passed", "category": "bugfix", "tags": ["go"], "weight": 2, "duration_seconds": 2.0, "candidate_capabilities": ["shell"]},
            {"candidate": "a", "status": "failed", "category": "bugfix", "tags": ["go"], "weight": 1, "duration_seconds": 4.0, "candidate_capabilities": ["shell"], "failure_class": "test_failure"},
            {"candidate": "a", "status": "skipped", "category": "ops", "tags": ["k8s"], "weight": 4, "candidate_capabilities": ["shell"]},
        ]
        summary = runner.summarize(records, {})
        candidate = summary["candidates"]["a"]
        self.assertEqual(candidate["total"], 3)
        self.assertEqual(candidate["eligible"], 2)
        self.assertEqual(candidate["skipped"], 1)
        self.assertEqual(candidate["pass_rate"], 0.5)
        self.assertEqual(candidate["weighted_pass_rate"], 2 / 3)
        self.assertEqual(candidate["median_duration_seconds"], 3.0)

    def test_report_renders_capability_and_coverage_sections(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            result_dir = Path(directory)
            (result_dir / "run.json").write_text(json.dumps({"started_at": "now", "ended_at": "now", "status": "passed", "inputs": {}, "selection": {}, "git": {}}))
            summary = runner.summarize([{"candidate": "a", "status": "passed", "category": "bugfix", "tags": ["go"], "weight": 1, "duration_seconds": 1, "candidate_capabilities": ["shell"]}], {})
            (result_dir / "summary.json").write_text(json.dumps(summary))
            (result_dir / "candidate_versions.json").write_text("{}")
            (result_dir / "records.jsonl").write_text(json.dumps({"task": "t", "candidate": "a", "repetition": 1, "status": "passed", "category": "bugfix", "source": "test", "tags": ["go"], "required_capabilities": ["shell"]}) + "\n")
            html = report.render_report(result_dir)
            self.assertIn("Declared capability profiles", html)
            self.assertIn("Category coverage", html)
            self.assertIn("shell", html)

    def test_incompatible_case_is_skipped_without_launching_agent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "fixture"
            fixture.mkdir()
            (fixture / "input.txt").write_text("unchanged")
            candidate = runner.Candidate("read-only", ["definitely-not-a-command"], {}, {}, "model", [], frozenset({"workspace_read"}))
            task = runner.Task("write-task", "bugfix", "test", "do work", fixture, ["false"], 1, ["write"], {}, frozenset({"workspace_write"}), 1)
            record = runner.run_case(candidate, task, "model", root / "results", 1, False, {})
            self.assertEqual(record["status"], "skipped")
            self.assertEqual(record["missing_capabilities"], ["workspace_write"])

    def test_public_harbor_dry_run_records_a_pinned_invocation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "jobs"
            result = subprocess.run(
                [
                    sys.executable,
                    str(EVALS / "run_harbor.py"),
                    "--benchmark",
                    "terminal-bench-2.1",
                    "--candidate",
                    "copilot-agent",
                    "--source-ref",
                    "2f9f18001bfd9e0f51bb92b026e853f65974ed6a",
                    "--model",
                    "controlled-model",
                    "--output",
                    str(output),
                    "--dry-run",
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            invocation = json.loads((output / "invocation.json").read_text())
            self.assertEqual(invocation["dataset"], "terminal-bench/terminal-bench-2-1")
            self.assertEqual(invocation["source_ref"], "2f9f18001bfd9e0f51bb92b026e853f65974ed6a")
            self.assertIn("evals.harbor_agents.slack_copilot:CopilotAgent", result.stdout)


if __name__ == "__main__":
    unittest.main()
