#!/usr/bin/env python3
"""Import Harbor/Terminal-Bench task directories into the neutral suite format."""

import argparse
import json
from pathlib import Path

def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=900)
    args = parser.parse_args()
    tasks = []
    for directory in sorted(path for path in args.root.iterdir() if path.is_dir()):
        instruction = next((path for path in (directory / "instruction.md", directory / "task.md", directory / "README.md") if path.exists()), None)
        if instruction is None: continue
        test = ["bash", "tests/test.sh"] if (directory / "tests" / "test.sh").exists() else ["bash", "test.sh"]
        tasks.append({
            "id": directory.name,
            "category": "benchmark",
            "source": "harbor",
            "fixture": str(directory.resolve()),
            "prompt": instruction.read_text(),
            "test": test,
            "timeout_seconds": args.timeout,
            "tags": ["harbor"],
            "metadata": {},
        })
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps({
        "schema_version": 1,
        "name": args.root.name,
        "description": "Imported Harbor/Terminal-Bench-style tasks.",
        "tasks": tasks,
    }, indent=2) + "\n")
    print(f"imported {len(tasks)} tasks")
    return 0

if __name__ == "__main__": raise SystemExit(main())
