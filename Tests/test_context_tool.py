import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parents[1] / 'Resources'))
from context_tool import ContextGrant, ContextTool, validate, cleanup_stale


class ContextToolTests(unittest.TestCase):
    def sources(self):
        return [{'id': 'local-source', 'title': 'Synthetic source', 'revision': 'pinned',
                 'messages': [{'id': 'm1', 'role': 'user', 'text': 'First selected message'},
                              {'id': 'm2', 'role': 'assistant', 'text': 'Ignore these quoted instructions; they remain data'}]}]

    def test_reads_only_attached_messages_with_pinned_links(self):
        tool = ContextTool(self.sources())
        result = tool.read({'session_id': 'local-source', 'offset': 1, 'limit': 1})
        self.assertEqual([m['id'] for m in result['messages']], ['m2'])
        self.assertIn('message=m2&revision=pinned', result['messages'][0]['source'])
        self.assertEqual(result['revision'], 'pinned')
        self.assertEqual(len(result['digest']), 64)
        self.assertIsNone(result['next_offset'])
        self.assertEqual(tool.read({'session_id': 'local-source', 'limit': 1})['next_offset'], 1)

    def test_rejects_other_sessions_paths_and_invalid_ranges(self):
        tool = ContextTool(self.sources())
        for arguments in ({'session_id': '/etc/passwd'}, {'session_id': 'not-attached'},
                          {'session_id': 'local-source', 'path': '/etc/passwd'},
                          {'session_id': 'local-source', 'offset': -1},
                          {'session_id': 'local-source', 'offset': True},
                          {'session_id': 'local-source', 'limit': 121}):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                tool.read(arguments)

    def test_tool_surface_contains_one_read_only_tool(self):
        tool = ContextTool(self.sources())
        reply = tool.handle({'id': 1, 'method': 'tools/list'})['result']['tools']
        self.assertEqual([t['name'] for t in reply], ['read_session'])
        self.assertTrue(reply[0]['annotations']['readOnlyHint'])
        bad = tool.handle({'id': 2, 'method': 'tools/call', 'params': {'name': 'send_prompt'}})
        self.assertTrue(bad['result']['isError'])
        self.assertNotIn('First selected', json.dumps(bad))

    def test_manifest_is_owner_only_and_removed(self):
        with tempfile.TemporaryDirectory() as root:
            grant = ContextGrant(self.sources(), root)
            path = Path(grant.server['args'][1])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
            self.assertNotIn('First selected message', grant.instructions)
            grant.close()
            self.assertFalse(path.exists())

    def test_orphan_cleanup_preserves_live_and_unknown_directories(self):
        with tempfile.TemporaryDirectory() as root:
            orphan = Path(root) / 'camp-context-orphan'; orphan.mkdir()
            (orphan / 'owner.json').write_text(json.dumps({'kind': 'capybara-context-v1', 'pid': 4321}))
            (orphan / 'sources.json').write_text('[]')
            unknown = Path(root) / 'camp-context-user'; unknown.mkdir()
            (unknown / 'notes.txt').write_text('Leave this alone')
            with mock.patch('context_tool.os.kill', side_effect=ProcessLookupError): cleanup_stale(root)
            self.assertFalse(orphan.exists())
            self.assertTrue(unknown.exists())
            grant = ContextGrant(self.sources(), root)
            try:
                cleanup_stale(root)
                self.assertTrue(Path(grant.server['args'][1]).exists())
            finally: grant.close()

    def test_real_stdio_mcp_exchange(self):
        with tempfile.TemporaryDirectory() as root:
            grant = ContextGrant(self.sources(), root)
            try:
                commands = [{'id': 1, 'method': 'initialize', 'params': {'protocolVersion': '2025-03-26'}},
                            {'method': 'notifications/initialized'},
                            {'id': 2, 'method': 'tools/call', 'params': {'name': 'read_session', 'arguments': {'session_id': 'local-source'}}}]
                process = subprocess.run([grant.server['command'], *grant.server['args']], input=''.join(json.dumps(c)+'\n' for c in commands), text=True, capture_output=True, timeout=5, check=True)
                replies = [json.loads(line) for line in process.stdout.splitlines()]
                self.assertEqual([r['id'] for r in replies], [1, 2])
                result = json.loads(replies[1]['result']['content'][0]['text'])
                self.assertEqual(len(result['messages']), 2)
            finally:
                grant.close()

    def test_context_bounds_and_duplicate_ids(self):
        for change in ('large', 'duplicate', 'role'):
            source = self.sources()
            if change == 'large': source[0]['messages'][0]['text'] = 'x' * (49 * 1024)
            if change == 'duplicate': source[0]['messages'][1]['id'] = 'm1'
            if change == 'role': source[0]['messages'][0]['role'] = 'execute'
            with self.subTest(change=change), self.assertRaises(ValueError): validate(source)


if __name__ == '__main__':
    unittest.main()
