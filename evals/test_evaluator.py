#!/usr/bin/env python3
"""Regression tests for the black-box evaluator contracts."""

from __future__ import annotations

import importlib.util
import asyncio
import io
import types
from unittest import mock
from urllib.error import HTTPError
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path


EVALS = Path(__file__).resolve().parent
# Match direct evaluator script imports when loading entrypoints by path.
sys.path.insert(0, str(EVALS))


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


runner = load_module("eval_runner", EVALS / "run.py")
report = load_module("eval_report", EVALS / "report.py")
harbor_launcher = load_module("harbor_launcher", EVALS / "run_harbor.py")
gateway = load_module("kepler_gateway", EVALS / "kepler_gateway.py")
gate = load_module("eval_gate", EVALS / "gate.py")


class GatewayPreflightTests(unittest.TestCase):
    def test_authentication_stays_in_environment_and_model_is_verified(self):
        with tempfile.TemporaryDirectory() as folder, mock.patch.dict(os.environ, {"EVAL_KEPLER_API_URL": "https://gateway.test", "KEPLER_TOKEN": "private-sentinel"}, clear=True), mock.patch.object(gateway.Path, "home", return_value=Path(folder)), mock.patch.object(gateway, "urlopen", return_value=io.BytesIO(b'{"model":"actual-model","protocol":"kepler"}')):
            info = gateway.prepare("actual-model")
            self.assertEqual(info["model"], "actual-model")
            self.assertNotIn("private-sentinel", json.dumps(info))
            self.assertEqual(os.environ["KEPLER_TOKEN"], "private-sentinel")

    def test_model_mismatch_is_rejected_before_running_cases(self):
        with tempfile.TemporaryDirectory() as folder, mock.patch.dict(os.environ, {"EVAL_KEPLER_API_URL": "https://gateway.test", "KEPLER_TOKEN": "private-sentinel"}, clear=True), mock.patch.object(gateway.Path, "home", return_value=Path(folder)), mock.patch.object(gateway, "urlopen", return_value=io.BytesIO(b'{"model":"actual-model","protocol":"kepler"}')):
            with self.assertRaisesRegex(ValueError, "differs from gateway model"):
                gateway.prepare("incorrect-model")

    def test_expired_login_has_actionable_error_without_credentials(self):
        error = HTTPError("https://gateway.test", 401, "unauthorized", {}, None)
        with tempfile.TemporaryDirectory() as folder, mock.patch.dict(os.environ, {"EVAL_KEPLER_API_URL": "https://gateway.test", "KEPLER_TOKEN": "private-sentinel"}, clear=True), mock.patch.object(gateway.Path, "home", return_value=Path(folder)), mock.patch.object(gateway, "urlopen", side_effect=error):
            with self.assertRaisesRegex(ValueError, "login") as caught:
                gateway.prepare()
            self.assertNotIn("private-sentinel", str(caught.exception))


class HarborCompletionTests(unittest.TestCase):
    def test_zero_cli_exit_does_not_hide_native_trial_errors(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder); job=root/"job"; job.mkdir()
            result={"finished_at":"now", "n_total_trials":1, "stats":{"n_completed_trials":1,"n_errored_trials":1}}
            (job/"result.json").write_text(json.dumps(result))
            self.assertEqual(harbor_launcher.execution_outcome(root,0)[0],1)
            result["stats"]["n_errored_trials"]=0
            (job/"result.json").write_text(json.dumps(result))
            self.assertEqual(harbor_launcher.execution_outcome(root,0)[0],0)

    def test_shared_viewer_directory_only_counts_current_job(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder)
            for name,errors in [("old",1),("new",0)]:
                job=root/name;job.mkdir()
                (job/"result.json").write_text(json.dumps({"finished_at":"now","n_total_trials":1,"stats":{"n_completed_trials":1,"n_errored_trials":errors}}))
            code,jobs=harbor_launcher.execution_outcome(root,0,"new")
            self.assertEqual(code,0)
            self.assertEqual(len(jobs),1)

    def test_missing_native_results_fail_closed(self):
        with tempfile.TemporaryDirectory() as folder:
            self.assertEqual(harbor_launcher.execution_outcome(Path(folder),0)[0],1)


class HarborAdapterTests(unittest.TestCase):
    def test_current_cli_flags_and_secret_free_metadata(self):
        class BaseAgent:
            def __init__(self, *args, **kwargs):
                self.model_name = kwargs.get("model_name")
                self.extra_env = kwargs.get("extra_env", {})
        modules = {}
        for name in ("harbor.agents.base", "harbor.environments.base", "harbor.models.agent.context"):
            modules[name] = types.ModuleType(name)
        modules["harbor.agents.base"].BaseAgent = BaseAgent
        modules["harbor.environments.base"].BaseEnvironment = object
        modules["harbor.models.agent.context"].AgentContext = object
        with mock.patch.dict(sys.modules, modules):
            adapter = load_module("kepler_harbor_adapter_test", EVALS / "harbor_agents/kepler_agent.py")
        agent = adapter.KeplerAgent(source_ref="a"*40, model_name="actual-model", extra_env={"KEPLER_TOKEN":"private-sentinel", "EVAL_KEPLER_API_URL":"https://gateway.test"})
        for mirror in ("http://mirror.test/ubuntu", "https://user:secret@mirror.test/ubuntu", "https://mirror.test/ubuntu\ncommand"):
            with self.assertRaises(ValueError):
                adapter.KeplerAgent(source_ref="a"*40, ubuntu_mirror=mirror)
        calls = []
        class Environment:
            async def exec(self, **kwargs):
                calls.append(kwargs)
                event = {"type":"turn_completed","metadata":{"usage":{"input_tokens":12,"cache_read_tokens":4,"output_tokens":3}}}
                return types.SimpleNamespace(return_code=0, stdout=json.dumps(event), stderr="")
        context = types.SimpleNamespace(metadata={})
        asyncio.run(agent.run("Fix the task.", Environment(), context))
        command = calls[0]["command"]
        self.assertIn("--approval project", command)
        for retired in ("--provider", "--protocol", "--model", "--base-url", "--api-key-env"):
            self.assertNotIn(retired, command)
        self.assertEqual(context.n_input_tokens,12)
        self.assertEqual(context.n_cache_tokens,4)
        self.assertNotIn("private-sentinel", command)
        self.assertNotIn("private-sentinel", json.dumps(context.metadata))
        self.assertEqual(calls[0]["env"]["KEPLER_TOKEN"], "private-sentinel")
        class CancelledEnvironment:
            async def exec(self, **kwargs):
                if "env" in kwargs:
                    raise asyncio.CancelledError()
                calls.append(kwargs)
                if kwargs["command"].startswith("cat "):
                    event = {"type": "turn_canceled", "metadata": {"usage": {"input_tokens": 42, "output_tokens": 7}}}
                    return types.SimpleNamespace(return_code=0, stdout=json.dumps(event), stderr="")
                return types.SimpleNamespace(return_code=0, stdout="", stderr="")
        with self.assertRaises(asyncio.CancelledError):
            asyncio.run(agent.run("Fix the task.", CancelledEnvironment(), context))
        self.assertIn("kill -INT", calls[-2]["command"])
        self.assertEqual(calls[-1]["timeout_sec"],10)
        self.assertEqual(context.n_input_tokens, 42)
        self.assertEqual(context.n_output_tokens, 7)
        class FailedEnvironment:
            async def exec(self, **kwargs):
                event = {"type":"turn_failed", "error":"protocol failure"}
                return types.SimpleNamespace(return_code=1, stdout="private-transcript-sentinel\n"+json.dumps(event), stderr="")
        with self.assertRaisesRegex(RuntimeError, "protocol failure") as caught:
            asyncio.run(agent.run("Fix the task.", FailedEnvironment(), context))
        self.assertNotIn("private-transcript-sentinel", str(caught.exception))
        class DriftedEnvironment:
            async def exec(self, **kwargs):
                event = {"type":"model_request_started","metadata":{"model":"other-model"}}
                return types.SimpleNamespace(return_code=0, stdout=json.dumps(event), stderr="")
        with self.assertRaisesRegex(RuntimeError,"changed after preflight"):
            asyncio.run(agent.run("Fix the task.", DriftedEnvironment(), context))



class HarborPreparationTests(unittest.TestCase):
    def test_running_job_stops_when_gateway_model_changes(self):
        process = mock.Mock()
        process.wait.side_effect = subprocess.TimeoutExpired(["harbor"], 5)
        with mock.patch.object(harbor_launcher.subprocess, "Popen", return_value=process), mock.patch.object(gateway, "prepare", side_effect=ValueError("model changed")), mock.patch.object(harbor_launcher, "stop_harbor") as stop:
            self.assertEqual(harbor_launcher.run_harbor(["harbor"], {}, expected_model="original-model"), 78)
            stop.assert_called_once_with(process)

    def test_package_mirror_preserves_repository_contract(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            apt = root / "apt"
            (apt / "sources.list.d").mkdir(parents=True)
            (apt / "apt.conf.d").mkdir()
            (root / "bin").mkdir()
            apt_get = root / "bin/apt-get"
            apt_get.write_text("#!/bin/sh\nexit 0\n")
            apt_get.chmod(0o755)
            legacy = apt / "sources.list"
            legacy.write_text("deb [signed-by=/keys/ubuntu.gpg] http://archive.ubuntu.com/ubuntu noble main universe\n"
                              "deb https://example.org/ubuntu noble main\n")
            deb822 = apt / "sources.list.d/ubuntu.sources"
            deb822.write_text("Types: deb\nURIs: http://security.ubuntu.com/ubuntu/\nSuites: noble-security\n"
                              "Components: main restricted\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n")
            script = (EVALS / "harbor_agents/package_sources.sh").read_text()
            script = script.replace("/etc/apt/", str(apt) + "/").replace("/logs/agent", str(root / "logs"))
            script = script.replace("/tmp/kepler-eval-ca.pem", str(root / "ca.pem"))
            (root / "ca.pem").write_text("trusted-test-roots")
            env = {**os.environ, "PATH": str(root / "bin") + os.pathsep + os.environ["PATH"]}
            subprocess.run(["sh", "-s", "--", "https://mirror.test/ubuntu"], input=script, text=True, check=True,
                           env=env)
            self.assertIn("[signed-by=/keys/ubuntu.gpg] https://mirror.test/ubuntu/ noble main universe", legacy.read_text())
            self.assertIn("deb https://example.org/ubuntu noble main", legacy.read_text())
            self.assertIn("URIs: https://mirror.test/ubuntu/ ", deb822.read_text())
            self.assertIn("Suites: noble-security", deb822.read_text())
            self.assertIn("Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg", deb822.read_text())
            self.assertFalse(list(apt.rglob("*.kepler-tmp")))
            self.assertIn("Acquire::https::mirror.test::CaInfo", (apt / "apt.conf.d/99-kepler-eval-ca").read_text())
            original = "deb http://archive.ubuntu.com/ubuntu noble main\n"
            legacy.write_text(original)
            subprocess.run(["sh", "-s", "--", ""], input=script, text=True, check=True, env=env)
            self.assertEqual(legacy.read_text(), original)
            subprocess.run(["sh", "-s", "--", "", "disabled"], input=script, text=True, check=True, env=env)
            self.assertIn('APT::Install-Recommends "false";', (apt / "apt.conf.d/99-kepler-eval-recommends").read_text())
            subprocess.run(["sh", "-s", "--", "", "enabled"], input=script, text=True, check=True, env=env)
            self.assertFalse((apt / "apt.conf.d/99-kepler-eval-recommends").exists())

    def test_image_preflight_matches_task_metadata_and_records_digests(self):
        preparation = load_module("harbor_preparation_test", EVALS / "prepare_harbor.py")
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            task = root / ".cache/harbor-datasets/terminal-bench-test/task"
            task.mkdir(parents=True)
            (task/"task.toml").write_text('[task]\nname="terminal-bench/example"\n[environment]\ndocker_image="upstream/example:version"\n')
            calls = []
            def run(command, **kwargs):
                calls.append(command)
                return types.SimpleNamespace(returncode=0)
            with mock.patch.object(preparation,"ROOT",root), mock.patch.object(preparation.subprocess,"run",side_effect=run), mock.patch.object(preparation.subprocess,"check_output",return_value='["upstream/example@sha256:verified"]'):
                images = preparation.prepare_images("terminal-bench/test",["terminal-bench/example"])
                self.assertEqual(images[0]["digests"],["upstream/example@sha256:verified"])
                self.assertFalse(any(c[:2]==["docker","pull"] for c in calls))
                with self.assertRaisesRegex(ValueError,"No task images"):
                    preparation.prepare_images("terminal-bench/test",["terminal-bench/other"])

    def test_unverified_dependency_archive_is_rejected(self):
        preparation = load_module("harbor_preparation_hash_test", EVALS / "prepare_harbor.py")
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder); (root/"bin").mkdir()
            (root/"bin"/(preparation.ARCHIVE+".tar.gz")).write_bytes(b"invalid")
            with mock.patch.object(preparation,"ROOT",root):
                with self.assertRaisesRegex(ValueError,"checksum mismatch"):
                    preparation.prepare()


class DockerDiskBudgetTests(unittest.TestCase):
    def test_probe_uses_target_daemon_space(self):
        output="Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda1 100000 60000 40000 60% /\n"
        with mock.patch.object(harbor_launcher.subprocess,"run",return_value=types.SimpleNamespace(stdout=output)) as run:
            self.assertEqual(harbor_launcher.docker_available_bytes("probe"),40000*1024)
            self.assertEqual(run.call_args.args[0],["docker","exec","probe","df","-Pk","/"])

    def test_low_capacity_prevents_model_job_start(self):
        with mock.patch.object(harbor_launcher,"docker_available_bytes",return_value=9), mock.patch.object(harbor_launcher.subprocess,"Popen") as launch:
            with self.assertRaisesRegex(RuntimeError,"reserve reached"):
                harbor_launcher.run_harbor(["harbor"],{},"probe",10)
            launch.assert_not_called()

    def test_running_job_stops_on_reserve_breach(self):
        process=mock.Mock()
        process.wait.side_effect=subprocess.TimeoutExpired(["harbor"],5)
        with mock.patch.object(harbor_launcher,"check_disk_budget",side_effect=[None,RuntimeError("reserve reached")]), mock.patch.object(harbor_launcher.subprocess,"Popen",return_value=process), mock.patch.object(harbor_launcher,"stop_harbor") as stop:
            self.assertEqual(harbor_launcher.run_harbor(["harbor"],{},"probe",10),75)
            stop.assert_called_once_with(process)


class EvaluatorTests(unittest.TestCase):
    def test_interrupted_log_retains_completed_usage_without_double_counting(self):
        completed = {"type": "model_completed", "metadata": {"usage": {"input_tokens": 10, "output_tokens": 4, "reported": True}}}
        log = json.dumps(completed) + "\n" + json.dumps(completed) + "\n{partial"
        metrics = runner.kepler_metrics(log)
        self.assertEqual(metrics["usage"], {"input_tokens": 20, "output_tokens": 8})
        self.assertTrue(metrics["usage_partial"])
        terminal = {"type": "turn_canceled", "metadata": {"usage": {"input_tokens": 21, "output_tokens": 9}}}
        metrics = runner.kepler_metrics(log + "\n" + json.dumps(terminal))
        self.assertEqual(metrics["usage"]["input_tokens"], 21)
        self.assertNotIn("usage_partial", metrics)

    @unittest.skipUnless(os.name == "posix", "process-group lifecycle")
    def test_timeout_stops_candidate_descendants(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); marker = root / "survived"
            child = f"import time,pathlib; time.sleep(1); pathlib.Path({str(marker)!r}).write_text('survived')"
            parent = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c',sys.argv[1]]); print('started',flush=True); time.sleep(10)"
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.run_process([sys.executable,"-c",parent,child],cwd=root,env=runner.probe_environment(root),timeout=0.3)
            time.sleep(1)
            self.assertFalse(marker.exists())

    def test_metrics_use_events_instead_of_model_prose(self):
        events = [
            {"type":"model_request_started","timestamp":"2026-10-13T00:00:00Z","metadata":{"model":"actual"}},
            {"type":"model_streamed","timestamp":"2026-10-13T00:00:00.250Z","model_event":{"type":"first_token"}},
            {"type":"tool_call_started"}, {"type":"tool_call_failed"},
            {"type":"turn_completed","metadata":{"usage":{"input_tokens":12,"output_tokens":3}}},
        ]
        value = runner.kepler_metrics("unstructured output\n" + "\n".join(json.dumps(event) for event in events))
        self.assertEqual(value["first_token_ms"],250)
        self.assertEqual(value["tool_failures"],1)
        self.assertEqual(value["usage"]["output_tokens"],3)
        self.assertEqual(value["observed_models"],["actual"])

    def test_release_gate_checks_quality_timeout_and_latency(self) -> None:
        summary = {"total": 10, "eligible": 10, "passed": 8, "weighted_pass_rate": 0.8, "timeout": 1, "p95_duration_seconds": 12}
        self.assertEqual(gate.violation("kepler", summary, 0.8, 0.1, 12), [])
        failures = gate.violation("kepler", summary, 0.9, 0.05, 10)
        self.assertEqual(len(failures), 3)

    def test_release_gate_detects_baseline_regression(self) -> None:
        current = {"total": 10, "eligible": 10, "passed": 8, "weighted_pass_rate": 0.8, "timeout": 0, "p95_duration_seconds": 1}
        baseline = {"weighted_pass_rate": 0.9}
        failures = gate.violation("kepler", current, 0.7, 0.1, None, baseline, 0.05)
        self.assertEqual(len(failures), 1)

    def test_release_gate_rejects_summary_that_disagrees_with_records(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            summary_path = root / "summary.json"
            document = {"records": 1, "candidates": {"a": {"total": 1, "eligible": 1, "passed": 1, "timeout": 0, "weighted_pass_rate": 1.0}}}
            summary_path.write_text(json.dumps(document))
            (root / "run.json").write_text(json.dumps({"status": "completed", "ended_at": "now", "inputs": {"dry_run": False}}))
            (root / "records.jsonl").write_text(json.dumps({"candidate": "a", "status": "failed", "weight": 1}) + "\n")
            self.assertTrue(gate.validate_artifacts(summary_path, document))
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
            task = runner.Task("write-task", "bugfix", "test", "do work", fixture, ["false"], 1, ["write"], {}, frozenset({"workspace_write"}), 1, ())
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
                    "kepler-agent",
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
            self.assertIn("evals.harbor_agents.kepler_agent:KeplerAgent", result.stdout)

    def test_protected_grader_file_cannot_be_rewritten_to_pass(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "fixture"; fixture.mkdir()
            (fixture / "product.py").write_text("VALUE = 0\n")
            (fixture / "test_product.py").write_text("from product import VALUE\nassert VALUE == 42\n")
            candidate = runner.Candidate("tamper", [sys.executable, "-c", "from pathlib import Path; Path('test_product.py').write_text('')"], {}, {}, "model", [], frozenset())
            task = runner.Task("tamper", "security", "test", "fix", fixture, [sys.executable, "test_product.py"], 5, [], {}, frozenset(), 1, ("test_product.py",))
            record = runner.run_case(candidate, task, "model", root / "results", 1, False, {})
            self.assertEqual(record["status"], "failed")

    def test_grader_does_not_inherit_unrelated_environment(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "fixture"; fixture.mkdir()
            candidate = runner.Candidate("noop", [sys.executable, "-c", "pass"], {}, {}, "model", [], frozenset())
            task = runner.Task("env", "security", "test", "noop", fixture, [sys.executable, "-c", "import os,sys; sys.exit('KEPLER_SYNTHETIC_SECRET' in os.environ)"], 5, [], {}, frozenset(), 1, ())
            os.environ["KEPLER_SYNTHETIC_SECRET"] = "synthetic"
            try: record = runner.run_case(candidate, task, "model", root / "results", 1, False, {})
            finally: os.environ.pop("KEPLER_SYNTHETIC_SECRET", None)
            self.assertEqual(record["status"], "passed")

    def test_candidate_environment_passes_explicit_kepler_credentials(self) -> None:
        candidate = runner.Candidate("kepler", ["true"], {}, {}, "model", [], frozenset())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old_url = os.environ.get("EVAL_KEPLER_API_URL")
            old_token = os.environ.get("KEPLER_TOKEN")
            os.environ["EVAL_KEPLER_API_URL"] = "https://gateway.example"
            os.environ["KEPLER_TOKEN"] = "test-token"
            try:
                env = runner.clean_environment(candidate, "model", root, root / "home", {})
            finally:
                if old_url is None: os.environ.pop("EVAL_KEPLER_API_URL", None)
                else: os.environ["EVAL_KEPLER_API_URL"] = old_url
                if old_token is None: os.environ.pop("KEPLER_TOKEN", None)
                else: os.environ["KEPLER_TOKEN"] = old_token
            self.assertEqual(env["KEPLER_API_URL"], "https://gateway.example")
            self.assertEqual(env["KEPLER_TOKEN"], "test-token")

    def test_gate_rejects_non_finite_metrics_and_low_coverage(self) -> None:
        invalid = {"total": 1, "eligible": 1, "passed": 1, "timeout": 0, "weighted_pass_rate": float("nan"), "p95_duration_seconds": 1}
        self.assertTrue(gate.violation("a", invalid, 0, 1, None))
        low_coverage = {"total": 100, "eligible": 1, "passed": 1, "timeout": 0, "weighted_pass_rate": 1, "p95_duration_seconds": 1}
        self.assertTrue(gate.violation("a", low_coverage, 1, 0, 1))


if __name__ == "__main__":
    unittest.main()
