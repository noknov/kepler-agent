#!/usr/bin/env python3
"""Serve a read-only, loopback AppWorld progress view without task contents."""
from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path


def load(path: Path) -> dict:
    return json.loads(path.read_text()) if path.is_file() else {}


def activity(path: Path) -> list[dict]:
    if not path.is_file():
        return []
    # Only inspect the active task's recent event tail. Never expose prompts,
    # tool arguments/results, credentials, or held-out grader details.
    with path.open('rb') as stream:
        stream.seek(0, 2)
        size = stream.tell()
        offset = max(0, size - 65536)
        stream.seek(offset)
        if offset:
            stream.readline()
        lines = stream.read().decode('utf-8', errors='replace').splitlines()
    events = []
    for line in lines:
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        kind = event.get('type', '')
        if kind in {'model_request_started', 'model_completed', 'tool_call_started',
                    'tool_call_completed', 'tool_call_failed', 'turn_completed', 'turn_failed'}:
            call = event.get('tool_call') or {}
            events.append({'type': kind, 'tool': call.get('name', ''), 'time': event.get('time', event.get('created_at', ''))})
    return events[-8:]


def snapshot(root: Path) -> dict:
    manifest = load(root / 'invocation.json')
    if manifest:
        progress = {'label': root.name, 'status': manifest['status'],
                    'model': manifest.get('gateway', {}).get('model'),
                    'total_tasks': len(manifest.get('task_ids', [])),
                    'max_steps': manifest.get('max_steps'),
                    'timeout_seconds': manifest.get('timeout_seconds'),
                    'error': manifest.get('error')}
        sources = [(manifest.get('split', ''), root, manifest)]
    else:
        progress = load(root / 'progress.json')
        sources = [(split, root / split, load(root / split / 'invocation.json'))
                   for split in progress.get('splits', [])]
    rows, splits = [], []
    for split, directory, manifest in sources:
        records = {item['task_id']: item for item in manifest.get('results', [])}
        splits.append({'name': split, 'status': manifest.get('status', 'pending'),
                       'total': len(manifest.get('task_ids', []))})
        for task_id in manifest.get('task_ids', []):
            item = records.get(task_id, {})
            state = item.get('status', 'pending')
            metrics = item.get('metrics', {})
            rows.append({'split': split, 'task': task_id, 'status': state,
                         'success': item.get('success'), 'started_at': item.get('started_at'),
                         'duration_seconds': item.get('duration_seconds'),
                         'model_requests': metrics.get('model_requests'),
                         'tool_calls': metrics.get('tool_calls'),
                         'usage': metrics.get('usage', {}),
                         'activity': activity(directory / task_id / 'agent.jsonl') if state == 'running' else []})
    settled = [item for item in rows if item['status'] not in {'running', 'pending'}]
    return {'name': progress.get('label', root.name), 'status': progress.get('status', 'starting'),
            'model': progress.get('model'), 'total': progress.get('total_tasks', 0),
            'max_steps': progress.get('max_steps'), 'timeout_seconds': progress.get('timeout_seconds'),
            'finished': len(settled), 'passed': sum(item['success'] is True for item in settled),
            'execution_errors': sum(item['status'] != 'completed' for item in settled),
            'splits': splits, 'tasks': rows, 'error': progress.get('error')}


STATIC_ROOT = Path(__file__).with_name('web') / 'appworld'
STATIC_ROUTES = {
    '/': ('index.html', 'text/html; charset=utf-8'),
    '/app.js': ('app.js', 'text/javascript; charset=utf-8'),
    '/style.css': ('style.css', 'text/css; charset=utf-8'),
}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run-dir', type=Path, required=True)
    parser.add_argument('--port', type=int, default=8084)
    args = parser.parse_args()
    root = args.run_dir.resolve()
    if not any((root / name).is_file() for name in ('invocation.json', 'progress.json')):
        parser.error('run-dir must contain invocation.json or grouped progress.json')

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path in STATIC_ROUTES:
                filename, mime = STATIC_ROUTES[self.path]
                body = (STATIC_ROOT / filename).read_bytes()
            elif self.path == '/api/progress':
                try:
                    body, mime = json.dumps(snapshot(root)).encode(), 'application/json; charset=utf-8'
                except (OSError, ValueError):
                    self.send_error(503, 'Progress temporarily unavailable')
                    return
            else:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header('Content-Type', mime)
            self.send_header('Content-Length', str(len(body)))
            self.send_header('Cache-Control', 'no-store')
            self.send_header('X-Content-Type-Options', 'nosniff')
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass

    server = ThreadingHTTPServer(('127.0.0.1', args.port), Handler)
    print(f'AppWorld progress: http://127.0.0.1:{args.port}/', flush=True)
    server.serve_forever()


if __name__ == '__main__':
    main()
