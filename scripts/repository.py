#!/usr/bin/env python3
"""Read-only repository diagnostics, documentation checks, and change evidence."""
from __future__ import annotations

import argparse
from collections import defaultdict
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parent.parent


def git(root: Path, *args: str) -> bytes:
    result = subprocess.run(["git", "-C", str(root), *args], capture_output=True)
    if result.returncode:
        raise ValueError(result.stderr.decode(errors="replace").strip())
    return result.stdout


def tracked_and_new(root: Path) -> list[str]:
    return sorted(set(git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").decode().strip("\0").split("\0")) - {""})


def doctor(root: Path, ui: bool, integration: bool) -> int:
    failures = []
    if sys.version_info < (3, 11):
        failures.append("Python 3.11+ required by evaluators; set make PYTHON=/path/to/python3.11")
    print(f"Python: {sys.version.split()[0]} ({sys.executable})")
    for command in ("git", "rg", "go"):
        path = shutil.which(command)
        print(f"{command}: {path or 'MISSING'}")
        if not path:
            failures.append(f"install {command} before running repository checks")
    if shutil.which("go"):
        result = subprocess.run(["go", "version"], cwd=root, text=True, capture_output=True)
        required = re.search(r"^go (\d+)\.(\d+)", (root / "go.mod").read_text(), re.M)
        actual = re.search(r"go(\d+)\.(\d+)", result.stdout)
        if result.returncode or not actual or tuple(map(int, actual.groups())) < tuple(map(int, required.groups())):
            failures.append("use the Go toolchain declared by go.mod (GOTOOLCHAIN=auto can select it)")
        else:
            print(result.stdout.strip())
    for command, minimum in (("node", 20), ("pnpm", 10)):
        path = shutil.which(command)
        version = "unavailable"
        if path:
            result = subprocess.run([command, "--version"], text=True, capture_output=True, timeout=10)
            version = result.stdout.strip() if result.returncode == 0 else "unavailable"
        print(f"{command}: {version} ({'required' if ui else 'optional for core checks'})")
        match = re.match(r"v?(\d+)", version)
        if ui and (not match or (int(match.group(1)) != 10 if command == "pnpm" else int(match.group(1)) < minimum)):
            failures.append(f"UI checks require {command} " + ("10, matching CI" if command == "pnpm" else f"{minimum}+"))
    for variable in ("POSTGRES_TEST_DSN", "REDIS_TEST_URL"):
        configured = bool(os.environ.get(variable))
        print(f"{variable}: {'configured' if configured else 'not configured; dependent tests can skip'}")
        if integration and not configured:
            failures.append(f"set {variable} to an isolated test store before integration checks")
    print("Integration configuration is not a connectivity check. No service was started or modified.")
    for failure in failures:
        print("ERROR: " + failure, file=sys.stderr)
    return bool(failures)


def markdown_links(text: str):
    # Validate destinations, not a full Markdown rendering or heading slugger.
    # Ignore literal examples in fenced blocks and inline code.
    text = re.sub(r"(?ms)^\s*(`{3,}|~{3,})[^\n]*\n.*?^\s*\1\s*$", "", text)
    text = re.sub(r"`+[^`\n]*`+", "", text)
    for match in re.finditer(r"!?\[[^\]\n]*\]\(\s*(<[^>]+>|[^\s)]+)(?:\s+['\"][^\n]*?['\"])?\s*\)", text):
        yield match.group(1).strip("<>")
    for match in re.finditer(r"(?m)^\s*\[[^\]]+\]:\s*(<[^>]+>|\S+)", text):
        yield match.group(1).strip("<>")


def documentation_errors(root: Path, paths: list[str]) -> list[str]:
    errors = []
    root = root.resolve()
    for name in paths:
        path = root / name
        if path.suffix != ".md" or not path.exists():
            continue
        for destination in markdown_links(path.read_text()):
            parsed = urlsplit(destination)
            if parsed.scheme or parsed.netloc or not parsed.path:
                continue
            target = unquote(parsed.path)
            resolved = (root / target.lstrip("/")) if target.startswith("/") else (path.parent / target)
            if not resolved.resolve().is_relative_to(root):
                errors.append(f"{name}: local destination {destination!r} escapes the repository; use its canonical repository URL")
            elif not resolved.exists():
                errors.append(f"{name}: broken local destination {destination!r}; update the owning link with the source change")
    return errors


def docs(root: Path) -> int:
    paths = [name for name in tracked_and_new(root) if name.startswith("docs/") or "/" not in name or name.endswith("/AGENTS.md")]
    errors = documentation_errors(root, paths)
    for name in paths:
        if name.startswith("docs/plans/active/") and name.endswith(".md") and (root / name).exists():
            text = (root / name).read_text()
            for heading in ("Outcome and acceptance", "Decisions", "Progress and evidence", "Handoff"):
                if not re.search(r"(?m)^## " + re.escape(heading) + r"\s*$", text):
                    errors.append(f"{name}: active plan needs section {heading!r}; follow docs/plans/README.md")
            if not re.search(r"(?m)^Status: active\b", text):
                errors.append(f"{name}: declare Status: active or move the completed record")
    for error in errors:
        print(error, file=sys.stderr)
    if not errors:
        print("Live documentation destinations passed (external URLs and heading anchors are not checked)")
    return bool(errors)


def category(path: str) -> str:
    if path.startswith("apps/cli/src/generated/") or path == "docs/app-server.schema.json":
        return "generated"
    if path.endswith("_test.go") or "/tests/" in path or Path(path).name.startswith("test_"):
        return "tests"
    if path.startswith("docs/") or path.endswith(".md"):
        return "documentation"
    if path.startswith("scripts/") or path.startswith(".github/") or path == "Makefile":
        return "maintenance"
    return "product"


def parse_numstat(data: bytes):
    # --no-renames makes every NUL-delimited record an unambiguous single path.
    for record in data.decode().strip("\0").split("\0"):
        if record:
            added, removed, path = record.split("\t", 2)
            yield {"path": path, "category": category(path), "added": int(added) if added != "-" else None, "removed": int(removed) if removed != "-" else None}


def go_package_dirs(root: Path, revision: str | None) -> set[str]:
    names = (git(root, "ls-tree", "-r", "-z", "--name-only", revision).decode().strip("\0").split("\0") if revision else tracked_and_new(root))
    return {str(Path(name).parent) for name in names if name.endswith(".go") and not name.endswith("_test.go") and (revision or (root / name).exists())}


def change_report(root: Path, base: str, head: str | None = None) -> dict:
    # Resolve first, then pass only the verified object ID to subsequent calls.
    revision = git(root, "rev-parse", "--verify", "--end-of-options", base + "^{commit}").decode().strip()
    target = git(root, "rev-parse", "--verify", "--end-of-options", head + "^{commit}").decode().strip() if head else None
    comparison = [revision, target] if target else [revision]
    files = list(parse_numstat(git(root, "diff", "--no-ext-diff", "--no-renames", "--numstat", "-z", *comparison, "--")))
    totals = defaultdict(lambda: {"added": 0, "removed": 0})
    for file in files:
        for key in ("added", "removed"):
            totals[file["category"]][key] += file[key] or 0
    new_packages = sorted(go_package_dirs(root, target) - go_package_dirs(root, revision))
    untracked = git(root, "ls-files", "--others", "--exclude-standard", "-z").decode().strip("\0").split("\0")
    return {
        "available": True, "base": revision,
        "head": target or git(root, "rev-parse", "HEAD").decode().strip(),
        "scope": "exact base/head commits" if target else "base commit versus tracked working tree; stage new files to include their line counts",
        "totals": dict(sorted(totals.items())), "files": files,
        "new_go_packages": new_packages,
        "dependency_manifests_changed": [f["path"] for f in files if Path(f["path"]).name in ("go.mod", "go.sum", "package.json", "pnpm-lock.yaml")],
        "untracked_files_not_in_line_totals": [p for p in untracked if p] if not target else [],
        "interpretation": "Growth is review evidence, not a quality score. Explain new ownership, dependencies, alternatives removed, and behavior verified.",
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    commands = parser.add_subparsers(dest="command", required=True)
    diagnostics = commands.add_parser("doctor")
    diagnostics.add_argument("--ui", action="store_true")
    diagnostics.add_argument("--integration", action="store_true")
    commands.add_parser("docs")
    report = commands.add_parser("report")
    report.add_argument("--base", required=True, help="explicit existing comparison commit/ref; never inferred")
    report.add_argument("--head", help="optional exact head commit/ref for CI; otherwise inspect tracked working tree")
    report.add_argument("--output", type=Path)
    args = parser.parse_args()
    try:
        if args.command == "doctor":
            return doctor(args.root, args.ui, args.integration)
        if args.command == "docs":
            return docs(args.root)
        evidence = json.dumps(change_report(args.root, args.base, args.head), indent=2, ensure_ascii=False) + "\n"
        if args.output:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(evidence)
            print(f"Change report: {args.output}")
        else:
            print(evidence, end="")
        return 0
    except (ValueError, OSError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
