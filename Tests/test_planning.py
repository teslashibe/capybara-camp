import io
import json
import queue
import sys
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1] / 'Resources'))
import bridge
import conversation


class PlanningTests(unittest.TestCase):
    def test_codex_planning_uses_an_ephemeral_thread(self):
        class Connection:
            def __init__(self): self.calls = []
            def rpc(self, method, params):
                self.calls.append((method, params))
                if method == 'config/read': return {'config': {}}
                if method == 'thread/start': return {'thread': {'id': 'isolated'}}
                if method == 'turn/start': return {'turn': {'id': 'turn'}}
                return {}
            def write(self, value): pass
            def next(self): return {'method': 'turn/completed', 'params': {'turn': {'id': 'turn', 'status': 'completed'}}}
        c = Connection()
        events = []
        conversation.codex_turn(c, {'sessionID': 'original', 'cwd': '/tmp'}, {'mode': 'planning', 'prompt': 'Draft a plan'}, queue.Queue(), events.append)
        self.assertNotIn('thread/resume', [method for method, _ in c.calls])
        params = next(params for method, params in c.calls if method == 'thread/start')
        self.assertTrue(params['ephemeral'])
        self.assertNotIn('threadId', params)
        turn = next(params for method, params in c.calls if method == 'turn/start')
        self.assertEqual(turn['threadId'], 'isolated')
        self.assertEqual(events[-1]['type'], 'done')

    def test_claude_planning_does_not_resume_or_persist(self):
        with tempfile.TemporaryDirectory() as root:
            s = {'provider': 'Claude', 'id': 'source', 'sessionID': 'original', 'cwd': root, 'root': root}
            with patch.object(bridge, 'SUPPORT', Path(root)/'support'), \
                 patch.object(bridge, 'runtime_command', return_value=['claude', '--resume', 'original', '-p', '--safe-mode']), \
                 patch('activity.assert_available') as available, \
                 patch.object(conversation, 'NativeConnection') as connection, \
                 patch.object(conversation, 'claude_turn'):
                conversation.run({'session': s, 'prompt': 'Draft a plan', 'mode': 'planning'}, io.StringIO(''), lambda e: None)
                command = connection.call_args.args[0]
                self.assertNotIn('--resume', command)
                self.assertIn('--no-session-persistence', command)
                self.assertNotEqual(command[command.index('--session-id')+1], 'original')
                self.assertEqual(s['sessionID'], 'original')
                available.assert_not_called()

    def test_visible_output_is_bounded_in_utf8_bytes(self):
        events = []
        conversation.emit_text({'mode': 'planning'}, events.append, 'A short plan')
        with self.assertRaises(conversation.PlanningOutputLimit):
            conversation.emit_text({'mode': 'planning'}, events.append, '🌱' * 3000)
        self.assertEqual(len(events), 1)

    def test_usage_preserves_unavailable_values(self):
        events = []
        conversation.emit_usage(events.append, {'inputTokens': 34, 'outputTokens': 12}, codex=True)
        self.assertEqual(events[-1], {'type': 'usage', 'inputTokens': 34, 'outputTokens': 12})
        conversation.emit_usage(events.append, {'output_tokens': 8, 'input_tokens': 10, 'cache_read_input_tokens': 3})
        self.assertEqual(events[-1]['inputTokens'], 13)
        conversation.emit_usage(events.append, {'inputTokens': None, 'outputTokens': -1}, codex=True)
        self.assertEqual(len(events), 2)


if __name__ == '__main__': unittest.main()
