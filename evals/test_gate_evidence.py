"""Release evidence must retain complete outcomes and measured performance."""
from __future__ import annotations

import copy
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import gate
import run as runner
from scoring import summarize


class EvidenceTests(unittest.TestCase):
    def write_evidence(self, root: Path) -> tuple[Path, dict]:
        root.mkdir(parents=True, exist_ok=True)
        records = [
            {"candidate": "a", "task": "one", "repetition": 1, "status": "passed", "agent_exit_code": 0, "test_exit_code": 0, "category": "repair", "tags": [], "duration_seconds": 12.0, "weight": 1},
            {"candidate": "a", "task": "two", "repetition": 1, "status": "failed", "agent_exit_code": 0, "test_exit_code": 1, "category": "repair", "tags": [], "duration_seconds": 100.0, "weight": 2},
        ]
        identity = {"schema_version": 2, "candidates": ["a"], "tasks": ["one", "two"], "repetitions": 1, "fixtures_sha256": {"one": "one", "two": "two"}}
        document = summarize(records, {})
        document["evaluation_identity"] = identity
        run = {"status": "completed", "ended_at": "2026-10-26", "inputs": {"dry_run": False}, "evaluation_identity": identity, "plan": {"cases": 2, "order": [{key: record[key] for key in ("candidate", "task", "repetition")} for record in records]}}
        (root / "run.json").write_text(json.dumps(run))
        (root / "records.jsonl").write_text("\n".join(json.dumps(record) for record in records) + "\n")
        path = root / "summary.json"
        path.write_text(json.dumps(document))
        return path, document

    def test_complete_evidence_passes_and_tampered_latency_fails(self):
        with tempfile.TemporaryDirectory() as folder:
            path, document = self.write_evidence(Path(folder))
            self.assertEqual(gate.validate_artifacts(path, document), [])
            document["candidates"]["a"]["p95_duration_seconds"] = 0.001
            self.assertTrue(gate.validate_artifacts(path, document))

    def test_missing_duplicate_and_unknown_cases_cannot_disappear_into_summary(self):
        for mutation in ("missing", "duplicate", "unknown", "dry_run", "negative_weight", "nan_duration", "failed_grader"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as folder:
                path, document = self.write_evidence(Path(folder))
                records = [json.loads(line) for line in path.with_name("records.jsonl").read_text().splitlines()]
                if mutation == "missing": records.pop()
                elif mutation == "duplicate": records[1] = copy.deepcopy(records[0])
                elif mutation == "unknown": records[0]["candidate"] = "unreported"
                elif mutation == "dry_run": records[0]["status"] = "dry_run"
                elif mutation == "negative_weight": records[0]["weight"] = -1
                elif mutation == "nan_duration": records[0]["duration_seconds"] = float("nan")
                elif mutation == "failed_grader": records[0]["test_exit_code"] = 1
                path.with_name("records.jsonl").write_text("\n".join(json.dumps(record) for record in records))
                forged = summarize(records, {})
                forged["evaluation_identity"] = document["evaluation_identity"]
                self.assertTrue(gate.validate_artifacts(path, forged))

    def test_baseline_is_validated_before_comparison(self):
        with tempfile.TemporaryDirectory() as folder:
            path, _ = self.write_evidence(Path(folder) / "current")
            baseline, document = self.write_evidence(Path(folder) / "baseline")
            document["candidates"]["a"]["weighted_pass_rate"] = 0
            baseline.write_text(json.dumps(document))
            result = subprocess.run([sys.executable, str(Path(gate.__file__)), str(path), "--min-pass-rate", "0", "--baseline", str(baseline)], text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("invalid baseline", result.stderr)

    def test_fixture_identity_tracks_content_modes_and_internal_links(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder) / "fixture"
            root.mkdir()
            file = root / "grader.py"
            file.write_text("first")
            first = runner.fixture_fingerprint(root)
            file.write_text("second")
            second = runner.fixture_fingerprint(root)
            self.assertNotEqual(first, second)
            file.chmod(0o755)
            self.assertNotEqual(second, runner.fixture_fingerprint(root))
            link = root / "link"
            link.symlink_to("grader.py")
            runner.fixture_fingerprint(root)
            link.unlink()
            link.symlink_to(Path(folder) / "outside")
            with self.assertRaisesRegex(ValueError, "escapes"):
                runner.fixture_fingerprint(root)
