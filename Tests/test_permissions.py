import json
from pathlib import Path
import queue
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / 'Resources'))
from permissions import normalize, check_codex, codex_overrides
from execution import execution_context, execution_command, ApprovalRelay
from conversation import codex_turn, run
from bridge import runtime_command


class PermissionTests(unittest.TestCase):
    def test_provider_modes_do_not_cross_or_silently_fall_back(self):
        for value in ({'sandbox': 'workspace-write', 'profile': ':workspace'}, {'approval': 'untrusted'},
                      {'claudeMode': 'bypassPermissions'}, {'reviewer': 'automatic'}, []):
            with self.assertRaises(ValueError): normalize('Codex', value)
        for value in ({'sandbox': 'read-only'}, {'claudeMode': 'all'}):
            with self.assertRaises(ValueError): normalize('Claude', value)
        self.assertEqual(normalize('Codex', {'sandbox': '', 'approval': ''}), {})

    def test_managed_profile_and_empty_allowlists_fail_closed(self):
        for value, requirements in [({'sandbox': 'read-only'}, {'allowedSandboxModes': []}),
                                     ({'approval': 'never'}, {'allowedApprovalPolicies': ['on-request']}),
                                     ({'reviewer': 'auto_review'}, {'allowedApprovalsReviewers': []}),
                                     ({'profile': ':workspace'}, {'allowedPermissionProfiles': {':read-only': True}}),
                                     ({'sandbox': 'workspace-write'}, {'allowedPermissionProfiles': {':workspace': True}})]:
            with self.assertRaises(ValueError): check_codex(value, {}, requirements)
        with self.assertRaises(ValueError): check_codex({'profile': 'missing'}, {}, {})
        self.assertEqual(check_codex({'profile': 'company'}, {'permissions': {'company': {}}},
                                    {'allowedPermissionProfiles': {'company': True}}), {'profile': 'company'})

    def test_permission_change_invalidates_saved_execution_approval(self):
        with tempfile.TemporaryDirectory() as directory:
            source = {'provider': 'Codex', 'root': directory, 'cwd': directory}
            original = execution_context(source)
            selected = {'sandbox': 'read-only', 'approval': 'never'}
            self.assertNotEqual(original['fingerprint'], execution_context(source, selected)['fingerprint'])
            with self.assertRaisesRegex(ValueError, 'settings changed'):
                run({'session': source, 'prompt': 'x', 'mode': 'implementation', 'executionApproved': True,
                     'executionContext': original['fingerprint'], 'permissions': selected})
            with self.assertRaisesRegex(ValueError, 'execution approval'):
                run({'session': source, 'prompt': 'x', 'permissions': selected})

    def test_claude_modes_reach_the_cli_without_changing_saved_settings(self):
        source = {'provider': 'Claude', 'sessionID': '00000000-0000-0000-0000-000000000001'}
        for mode in ['manual', 'acceptEdits', 'plan', 'auto', 'dontAsk', 'bypassPermissions']:
            command = execution_command(source, runtime_command(source), permissions={'claudeMode': mode})
            self.assertEqual(command[command.index('--permission-mode') + 1], mode)
            self.assertNotIn('--dangerously-skip-permissions', command)
            self.assertIn('--permission-prompt-tool', command)

    def test_codex_confirms_selected_settings_before_submitting_prompt(self):
        selected = {'sandbox': 'workspace-write', 'approval': 'on-request', 'reviewer': 'auto_review'}
        class Connection:
            def __init__(self, mismatch=False): self.calls = []; self.mismatch = mismatch
            def write(self, value): pass
            def rpc(self, method, params):
                self.calls.append((method, params))
                if method == 'config/read': return {'config': {}}
                if method == 'configRequirements/read': return {'requirements': {}}
                if method == 'thread/resume': return {'sandbox': {'type': 'workspaceWrite'}, 'approvalPolicy': 'on-request', 'approvalsReviewer': 'user' if self.mismatch else 'auto_review'}
                if method == 'turn/start': return {'turn': {'id': 'turn'}}
                return {}
            def next(self): return {'method': 'turn/completed', 'params': {'threadId': 'session', 'turn': {'id': 'turn', 'status': 'completed'}}}
        for mismatch in [False, True]:
            connection = Connection(mismatch); events = []
            args = (connection, {'sessionID': 'session', 'cwd': '/tmp'},
                    {'mode': 'implementation', 'permissions': selected, 'prompt': 'hello'}, queue.Queue(), events.append)
            if mismatch:
                with self.assertRaisesRegex(ValueError, 'did not confirm'): codex_turn(*args)
                self.assertNotIn('turn/start', [v[0] for v in connection.calls])
            else:
                codex_turn(*args)
                resume = dict(connection.calls)['thread/resume']
                self.assertEqual({k: resume[k] for k in codex_overrides(selected)}, codex_overrides(selected))
                self.assertIn('permissions', [e['type'] for e in events])

    def test_only_offered_decisions_can_be_sent_including_session_scope(self):
        class Connection:
            def __init__(self): self.sent = []
            def write(self, value): self.sent.append(value)
        c = Connection(); events = []; relay = ApprovalRelay(c, events.append, 'Codex', 'thread', 'turn')
        event = {'id': 1, 'method': 'item/commandExecution/requestApproval', 'params':
                 {'threadId': 'thread', 'turnId': 'turn', 'command': 'echo test', 'availableDecisions': ['acceptForSession', 'cancel']}}
        relay.request(event); ident = events[-1]['id']
        self.assertTrue(events[-1]['canApprove'])
        relay.resolve({'id': ident, 'allow': True})
        self.assertEqual(c.sent, [])
        relay.resolve({'id': ident, 'allow': True, 'decision': 'accept'})
        self.assertEqual(c.sent, [])
        relay.resolve({'id': ident, 'allow': True, 'decision': 'acceptForSession'})
        self.assertEqual(c.sent, [{'id': 1, 'result': {'decision': 'acceptForSession'}}])
        relay.resolve({'id': ident, 'allow': True, 'decision': 'acceptForSession'})
        self.assertEqual(len(c.sent), 1)


if __name__ == '__main__': unittest.main()
