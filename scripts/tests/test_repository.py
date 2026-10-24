import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("repository", Path(__file__).resolve().parents[1] / "repository.py")
repository = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(repository)


class DocumentationTests(unittest.TestCase):
    def test_links_ignore_code_and_accept_spaces_references_and_images(self):
        text = '''[guide](guide.md#heading) ![image](image.png)
[space](<a folder/guide.md>)
[ref]: target.md "title"
`[literal](missing.md)`
```sh
[example](also-missing.md)
```
'''
        self.assertEqual(list(repository.markdown_links(text)), ["guide.md#heading", "image.png", "a folder/guide.md", "target.md"])

    def test_missing_relative_links_fail_but_remote_links_are_not_fetched(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/a.md").write_text("[missing](gone.go) [url](https://example.test/no-fetch) [anchor](#x)")
            errors = repository.documentation_errors(root, ["docs/a.md"])
            self.assertEqual(len(errors), 1)
            self.assertIn("gone.go", errors[0])

    def test_historical_plans_are_not_current_architecture_claims(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "docs/plans/completed/old.md"
            path.parent.mkdir(parents=True)
            path.write_text("[old code](removed.go)")
            self.assertEqual(repository.documentation_errors(root, [str(path.relative_to(root))]), [])


class ReportTests(unittest.TestCase):
    def test_numstat_handles_tabs_newlines_and_binary_paths(self):
        records = list(repository.parse_numstat(b"2\t1\tpath with\ttab.go\0-\t-\tfile\nname.png\0"))
        self.assertEqual(records[0]["path"], "path with\ttab.go")
        self.assertIsNone(records[1]["added"])
        self.assertEqual(records[1]["path"], "file\nname.png")

    def test_generated_and_tests_have_separate_accounting(self):
        self.assertEqual(repository.category("docs/app-server.schema.json"), "generated")
        self.assertEqual(repository.category("scripts/tests/test_repository.py"), "tests")
        self.assertEqual(repository.category("packages/example/a_test.go"), "tests")

    def test_report_is_read_only_and_uses_explicit_git_baseline(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", "-C", str(root), *args]).decode().strip()
            git("init", "-b", "main")
            git("config", "user.name", "Test")
            git("config", "user.email", "test@example.test")
            (root / "go.mod").write_text("module example.test\n")
            git("add", ".")
            git("commit", "-m", "baseline")
            base = git("rev-parse", "HEAD")
            (root / "packages/new").mkdir(parents=True)
            (root / "packages/new/a.go").write_text("package example\n")
            git("add", ".")
            (root / "pending.md").write_text("untracked\n")
            before = git("status", "--porcelain")
            report = repository.change_report(root, base)
            self.assertEqual(report["base"], base)
            self.assertEqual(report["new_go_packages"], ["packages/new"])
            self.assertEqual(report["totals"]["product"]["added"], 1)
            self.assertEqual(report["untracked_files_not_in_line_totals"], ["pending.md"])
            self.assertEqual(before, git("status", "--porcelain"))
            git("commit", "-m", "add package")
            head = git("rev-parse", "HEAD")
            (root / "go.mod").write_text("module dirty.example\n")
            exact = repository.change_report(root, base, head)
            self.assertEqual(exact["scope"], "exact base/head commits")
            self.assertEqual(exact["head"], head)
            self.assertEqual(exact["dependency_manifests_changed"], [])
            self.assertEqual(exact["untracked_files_not_in_line_totals"], [])
            with self.assertRaises(ValueError):
                repository.change_report(root, "--not-a-ref")


class DoctorTests(unittest.TestCase):
    def test_missing_integration_configuration_is_explicit_failure(self):
        with patch.dict(os.environ, {"POSTGRES_TEST_DSN": "", "REDIS_TEST_URL": ""}):
            with patch("builtins.print") as output, patch.object(repository.shutil, "which", return_value=None):
                self.assertNotEqual(repository.doctor(repository.ROOT, False, True), 0)
            self.assertTrue(any("set POSTGRES_TEST_DSN" in str(call) for call in output.call_args_list))


if __name__ == "__main__":
    unittest.main()
