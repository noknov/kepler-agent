#!/usr/bin/env python3
"""Repository architecture boundary checks."""

from __future__ import annotations

import re
import sys
from pathlib import Path

MODULE = "github.com/noknov/slack-copilot-agent/"
ROOT = Path(__file__).resolve().parent.parent


def go_files() -> list[Path]:
    ignored = {".git", ".cache", ".data", "bin", "dist"}
    files: list[Path] = []
    for path in ROOT.rglob("*.go"):
        if any(part in ignored for part in path.relative_to(ROOT).parts):
            continue
        files.append(path)
    return files


def imports(path: Path) -> list[str]:
    text = path.read_text()
    out: list[str] = []
    for block in re.findall(r'import\s*\((.*?)\)', text, flags=re.S):
        out.extend(re.findall(r'"([^"]+)"', block))
    for item in re.findall(r'import\s+(?:\w+\s+|\.\s+|_\s+)?"([^"]+)"', text):
        out.append(item)
    return out


def rel(path: Path) -> str:
    return path.relative_to(ROOT).as_posix()


def local(import_path: str) -> str | None:
    if not import_path.startswith(MODULE):
        return None
    return import_path[len(MODULE) :]


def violation(path: Path, import_path: str) -> str | None:
    source = rel(path)
    target = local(import_path)
    if target is None:
        return None

    if source.startswith("packages/agent/tool/"):
        if target.startswith("packages/prompts"):
            return None

    if source.startswith("packages/agent/"):
        if not target.startswith("packages/agent/"):
            return "packages/agent may only depend on canonical agent core packages"
        allowed = (
            "packages/agent/model",
            "packages/agent/prompt",
            "packages/agent/runtime",
            "packages/agent/tool",
            "packages/agent/transcript",
            "packages/agent/environment",
        )
        if not target.startswith(allowed):
            return "packages/agent contains only model/prompt/runtime/tool/transcript"

    if source.startswith("packages/providers/"):
        allowed = ("packages/agent/model", "packages/llm")
        if not target.startswith(allowed):
            return "packages/providers may only depend on agent/model and llm"

    if source.startswith("packages/profiles/"):
        if target.startswith("packages/llm"):
            return "profiles must use providers instead of importing llm directly"
        if target.startswith("packages/surfaces/"):
            return "profiles must not depend on surfaces"

    if source.startswith("packages/tools/"):
        if target.startswith("packages/surfaces/"):
            return "generic tools must not depend on surfaces"
        if target == "packages/tools/registry" or target.startswith("packages/tools/registry/"):
            return "packages/tools/registry was removed; use packages/agent/tool"

    if source.startswith("evals/"):
        if target.startswith("packages/"):
            return "evals must remain a black-box harness evaluator"

    return None


def main() -> int:
    failures: list[str] = []
    for path in go_files():
        for import_path in imports(path):
            reason = violation(path, import_path)
            if reason:
                failures.append(f"{rel(path)} imports {import_path}: {reason}")
    if failures:
        print("Architecture boundary violations:", file=sys.stderr)
        for failure in failures:
            print("  - " + failure, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
