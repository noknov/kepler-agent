"""Progress reporting must distinguish execution failures and hide task bodies."""
import json
from pathlib import Path
import tempfile
import unittest

from view_appworld import snapshot


class ProgressTests(unittest.TestCase):
    def test_native_invocation_requires_no_local_launcher_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'invocation.json').write_text(json.dumps({
                'split': 'dev', 'status': 'completed', 'gateway': {'model': 'model'},
                'task_ids': ['a'], 'results': [{'task_id': 'a', 'status': 'completed', 'success': True}],
            }))
            result = snapshot(root)
            self.assertEqual((result['total'], result['finished'], result['passed']), (1, 1, 1))
            self.assertEqual(result['model'], 'model')

    def test_aggregate_results_without_exposing_task_or_tool_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'progress.json').write_text(json.dumps({'splits': ['test_normal', 'test_challenge'], 'total_tasks': 4, 'status': 'running'}))
            split = root / 'test_normal'
            split.mkdir()
            records = [
                {'task_id': 'a', 'status': 'completed', 'success': True},
                {'task_id': 'b', 'status': 'completed', 'success': False},
                {'task_id': 'c', 'status': 'agent_error', 'success': False},
                {'task_id': 'd', 'status': 'running'},
            ]
            (split / 'invocation.json').write_text(json.dumps({'status': 'running', 'task_ids': list('abcd'), 'results': records}))
            active = split / 'd'
            active.mkdir()
            (active / 'agent.jsonl').write_text(json.dumps({'type': 'tool_call_started', 'tool_call': {'name': 'lookup', 'arguments': 'PRIVATE_TASK_CONTENT'}}) + '\n')
            result = snapshot(root)
            self.assertEqual((result['finished'], result['passed'], result['execution_errors']), (3, 1, 1))
            self.assertEqual(result['splits'][1]['status'], 'pending')
            self.assertIn('lookup', json.dumps(result))
            self.assertNotIn('PRIVATE_TASK_CONTENT', json.dumps(result))


if __name__ == '__main__':
    unittest.main()
