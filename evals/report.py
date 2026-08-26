#!/usr/bin/env python3
"""Generate a static HTML report for an eval result directory."""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path
from typing import Any


def load_json(path: Path) -> dict[str, Any]:
    return json.loads(path.read_text())


def load_records(path: Path) -> list[dict[str, Any]]:
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def esc(value: Any) -> str:
    if value is None:
        return ""
    return html.escape(str(value), quote=True)


def rel_link(path: str | None, label: str) -> str:
    if not path:
        return ""
    return f'<a href="{esc(path)}">{esc(label)}</a>'


def status_class(status: str) -> str:
    if status == "passed":
        return "passed"
    if status == "dry_run":
        return "dry-run"
    return "failed"


def case_dir(record: dict[str, Any]) -> str:
    return f'{record["task"]}__{record["candidate"]}__{record["repetition"]}'


def candidate_summary(summary: dict[str, Any]) -> str:
    rows = []
    for name, item in sorted(summary.get("candidates", {}).items()):
        rows.append(
            "<tr>"
            f"<td>{esc(name)}</td>"
            f"<td>{esc(item.get('total', 0))}</td>"
            f"<td>{esc(item.get('passed', 0))}</td>"
            f"<td>{esc(item.get('failed', 0))}</td>"
            f"<td>{esc(item.get('timeout', 0))}</td>"
            f"<td>{esc(item.get('agent_error', 0))}</td>"
            f"<td>{esc(item.get('launch_error', 0))}</td>"
            f"<td>{esc(item.get('dry_run', 0))}</td>"
            f"<td>{esc(round(item.get('pass_rate', 0) * 100, 1))}%</td>"
            f"<td><code>{esc(json.dumps(item.get('failure_classes', {}), sort_keys=True))}</code></td>"
            "</tr>"
        )
    return "\n".join(rows)


def version_rows(versions: dict[str, Any]) -> str:
    rows = []
    for name, item in sorted(versions.items()):
        output = item.get("output") or item.get("error") or ""
        rows.append(
            "<tr>"
            f"<td>{esc(name)}</td>"
            f"<td>{esc(item.get('status'))}</td>"
            f"<td><code>{esc(' '.join(item.get('command', [])))}</code></td>"
            f"<td><pre>{esc(output)}</pre></td>"
            "</tr>"
        )
    return "\n".join(rows)


def existing_link(result_dir: Path, path: Path, label: str) -> str:
    if not (result_dir / path).exists():
        return ""
    return rel_link(str(path), label)


def record_rows(result_dir: Path, records: list[dict[str, Any]]) -> str:
    rows = []
    for record in records:
        directory = case_dir(record)
        case_path = Path(directory)
        diff_link = existing_link(result_dir, case_path / "workspace.diff", "diff")
        agent_link = existing_link(result_dir, case_path / "agent.log", "agent.log")
        test_link = existing_link(result_dir, case_path / "test.log", "test.log")
        rows.append(
            "<tr>"
            f'<td><span class="pill {status_class(record.get("status", ""))}">{esc(record.get("status"))}</span></td>'
            f"<td>{esc(record.get('failure_class'))}</td>"
            f"<td>{esc(record.get('task'))}</td>"
            f"<td>{esc(record.get('candidate'))}</td>"
            f"<td>{esc(record.get('category'))}</td>"
            f"<td>{esc(record.get('source'))}</td>"
            f"<td>{esc(record.get('repetition'))}</td>"
            f"<td>{esc(record.get('duration_seconds'))}</td>"
            f"<td>{agent_link} {test_link} {diff_link}</td>"
            "</tr>"
        )
    return "\n".join(rows)


def render_report(result_dir: Path) -> str:
    run = load_json(result_dir / "run.json")
    summary = load_json(result_dir / "summary.json")
    versions = load_json(result_dir / "candidate_versions.json")
    records = load_records(result_dir / "records.jsonl")
    title = f"Eval report · {result_dir.name}"
    return f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{esc(title)}</title>
  <style>
    :root {{ color-scheme: light dark; --border: #d8dee4; --muted: #57606a; --bg: #f6f8fa; }}
    body {{ font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 32px; line-height: 1.45; }}
    h1, h2 {{ margin-bottom: 8px; }}
    .meta {{ color: var(--muted); margin-top: 0; }}
    .cards {{ display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin: 20px 0; }}
    .card {{ border: 1px solid var(--border); border-radius: 12px; padding: 14px; background: var(--bg); }}
    .card .value {{ font-size: 24px; font-weight: 700; }}
    table {{ border-collapse: collapse; width: 100%; margin: 12px 0 28px; }}
    th, td {{ border: 1px solid var(--border); padding: 8px 10px; text-align: left; vertical-align: top; }}
    th {{ background: var(--bg); }}
    code, pre {{ font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }}
    pre {{ white-space: pre-wrap; max-width: 720px; margin: 0; }}
    .pill {{ border-radius: 999px; padding: 2px 8px; font-size: 12px; font-weight: 700; }}
    .passed {{ background: #dafbe1; color: #116329; }}
    .failed {{ background: #ffebe9; color: #82071e; }}
    .dry-run {{ background: #ddf4ff; color: #0969da; }}
  </style>
</head>
<body>
  <h1>{esc(title)}</h1>
  <p class="meta">Started {esc(run.get("started_at"))} · ended {esc(run.get("ended_at"))} · status {esc(run.get("status"))}</p>
  <div class="cards">
    <div class="card"><div>Records</div><div class="value">{esc(summary.get("records", 0))}</div></div>
    <div class="card"><div>Model</div><div class="value">{esc(run.get("inputs", {}).get("model"))}</div></div>
    <div class="card"><div>Seed</div><div class="value">{esc(run.get("inputs", {}).get("seed"))}</div></div>
    <div class="card"><div>Git dirty</div><div class="value">{esc(run.get("git", {}).get("dirty"))}</div></div>
  </div>

  <h2>Run manifest</h2>
  <table>
    <tr><th>Suite</th><td>{esc(run.get("inputs", {}).get("suite"))}</td></tr>
    <tr><th>Candidates</th><td>{esc(", ".join(run.get("selection", {}).get("candidates", [])))}</td></tr>
    <tr><th>Tasks</th><td>{esc(", ".join(run.get("selection", {}).get("tasks", [])))}</td></tr>
    <tr><th>Git</th><td><code>{esc(run.get("git", {}).get("commit"))}</code> {esc(run.get("git", {}).get("branch"))}</td></tr>
    <tr><th>Filters</th><td><code>{esc(json.dumps(run.get("inputs", {}).get("filters", {}), sort_keys=True))}</code></td></tr>
  </table>

  <h2>Candidate summary</h2>
  <table>
    <thead><tr><th>Candidate</th><th>Total</th><th>Passed</th><th>Failed</th><th>Timeout</th><th>Agent error</th><th>Launch error</th><th>Dry-run</th><th>Pass rate</th><th>Failure classes</th></tr></thead>
    <tbody>{candidate_summary(summary)}</tbody>
  </table>

  <h2>Candidate versions</h2>
  <table>
    <thead><tr><th>Candidate</th><th>Status</th><th>Command</th><th>Output</th></tr></thead>
    <tbody>{version_rows(versions)}</tbody>
  </table>

  <h2>Case records</h2>
  <table>
    <thead><tr><th>Status</th><th>Failure class</th><th>Task</th><th>Candidate</th><th>Category</th><th>Source</th><th>Rep</th><th>Seconds</th><th>Artifacts</th></tr></thead>
    <tbody>{record_rows(result_dir, records)}</tbody>
  </table>
</body>
</html>
"""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("result_dir", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    output = args.output or args.result_dir / "report.html"
    output.write_text(render_report(args.result_dir))
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
