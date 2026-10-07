"""AppWorld adapter boundaries; these tests require no benchmark installation."""
import argparse
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import run_appworld as runner


class AppWorldTests(unittest.TestCase):
    def test_explicit_unlimited_steps_reaches_cli_configuration(self):
        limit = runner.parse_step_limit("-1")
        self.assertIn("max_steps = -1\n", runner.agent_config("http://mcp.test", limit))
        self.assertEqual(runner.parse_step_limit("100"), 100)
        for invalid in ("0", "-2"):
            with self.assertRaises(argparse.ArgumentTypeError):
                runner.parse_step_limit(invalid)

    def test_failure_reason_comes_from_structured_lifecycle_event(self):
        transcript = 'latest turn exceeds token budget\n{"type":"tool_call_failed","error":"tool error"}\n'
        self.assertEqual(runner.terminal_event(transcript), {})
        transcript += '{"type":"turn_failed","error":"latest turn exceeds token budget"}\n'
        self.assertEqual(runner.terminal_event(transcript), {
            "terminal_event": "turn_failed", "error": "latest turn exceeds token budget"})

    def test_agent_receives_public_task_fields_only(self):
        class Task:
            instruction = "Do the requested work"
            supervisor = {"email": "person@example.test"}
            datetime = "2025-01-01"

            @property
            def ground_truth(self):
                raise AssertionError("reference data must never be read")

        prompt = runner.task_prompt(Task())
        self.assertIn(Task.instruction, prompt)
        self.assertNotIn("ground_truth", prompt)

    def test_agent_does_not_inherit_operator_secrets_or_data_paths(self):
        with mock.patch.dict(os.environ, {"APPWORLD_ROOT": "/private/grader", "QODER_PERSONAL_ACCESS_TOKEN": "private", "KEPLER_TOKEN": "gateway-only"}, clear=True):
            env = runner.agent_environment({"api_url": "http://gateway.test"})
        self.assertEqual(env, {"KEPLER_TOKEN": "gateway-only", "KEPLER_API_URL": "http://gateway.test"})

    def test_timeout_stops_owned_agent_and_retains_lifecycle_metrics(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            script = folder / "agent.py"
            script.write_text('import time,json\nprint(json.dumps({"type":"tool_call_started"}),flush=True)\ntime.sleep(60)\n')
            with mock.patch.object(runner, "prepare"):
                result = runner.execute_agent([sys.executable, str(script)], folder, os.environ.copy(),
                                              folder / "agent.jsonl", 0.3, "model")
            self.assertEqual(result["status"], "agent_timeout")
            self.assertNotEqual(result["exit_code"], 0)
            self.assertEqual(result["metrics"]["tool_calls"], 1)


if __name__ == "__main__":
    unittest.main()
