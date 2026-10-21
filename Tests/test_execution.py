import json
from pathlib import Path
import queue
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / 'Resources'))
import bridge
import conversation
from execution import ApprovalRelay, execution_command


class Connection:
    def __init__(self): self.sent = []
    def write(self, value): self.sent.append(value)
    def reject_tool(self, value): self.sent.append({'id': value['id'], 'error': {'message': 'unsupported'}})


class ExecutionTests(unittest.TestCase):
    def relay(self, provider='Codex'):
        conn, events = Connection(), []
        return conn, events, ApprovalRelay(conn, events.append, provider, 'session', 'turn' if provider == 'Codex' else None)

    def request(self, method='item/commandExecution/requestApproval', ident=1, **params):
        return {'id': ident, 'method': method, 'params': {'threadId': 'session', 'turnId': 'turn', 'itemId': 'item', **params}}

    def test_command_requires_explicit_single_action_answer(self):
        conn, events, relay = self.relay()
        request = self.request(command='echo example', availableDecisions=['accept', 'decline'])
        relay.request(request)
        self.assertEqual(conn.sent, [])
        ident = events[0]['id']
        relay.resolve({'id': ident, 'allow': True})
        self.assertEqual(conn.sent, [{'id': 1, 'result': {'decision': 'accept'}}])
        relay.resolve({'id': ident, 'allow': True})
        self.assertEqual(len(conn.sent), 1)

    def test_cross_thread_and_cross_turn_are_rejected(self):
        conn, events, relay = self.relay()
        relay.request(self.request(threadId='other'))
        relay.request(self.request(turnId='other'))
        self.assertEqual(len(conn.sent), 2)
        self.assertEqual(events, [])

    def test_duplicate_request_and_server_cancellation(self):
        conn, events, relay = self.relay()
        request = self.request(command='echo example')
        relay.request(request); relay.request(request)
        self.assertEqual(len(events), 1)
        relay.cancelled(1)
        relay.resolve({'id': events[0]['id'], 'allow': True})
        self.assertEqual(conn.sent, [])

    def test_file_approval_needs_complete_diff(self):
        conn, events, relay = self.relay()
        relay.request(self.request('item/fileChange/requestApproval'))
        self.assertFalse(events[0]['canApprove'])
        relay.resolve({'id': events[0]['id'], 'allow': True})
        self.assertEqual(conn.sent, [])
        relay.resolve({'id': events[0]['id'], 'allow': False})
        relay.remember({'id': 'item', 'type': 'fileChange', 'changes': [{'path': '/tmp/example', 'diff': '+example'}]})
        relay.request(self.request('item/fileChange/requestApproval', ident=2))
        self.assertTrue(events[-1]['canApprove'])
        self.assertIn('+example', events[-1]['detail'])

    def test_permissions_are_exactly_requested_and_turn_scoped(self):
        conn, events, relay = self.relay()
        request = self.request('item/permissions/requestApproval', permissions={'network': {'enabled': True}})
        relay.request(request)
        request['params']['permissions']['fileSystem'] = {'write': ['/']}
        relay.resolve({'id': events[0]['id'], 'allow': True})
        self.assertEqual(conn.sent[-1]['result'], {'permissions': {'network': {'enabled': True}}, 'scope': 'turn'})

    def test_questions_validate_answers_and_preserve_question_ids(self):
        conn, events, relay = self.relay()
        relay.request(self.request('item/tool/requestUserInput', questions=[{'id': 'db', 'question': 'Database?', 'header': 'Database', 'options': [{'label': 'Postgres'}]}]))
        key = events[0]['id']
        relay.resolve({'id': key, 'allow': True, 'answers': {'db': ['arbitrary']}})
        self.assertEqual(conn.sent, [])
        relay.resolve({'id': key, 'allow': True, 'answers': {'db': ['Postgres']}})
        self.assertEqual(conn.sent[-1]['result'], {'answers': {'db': {'answers': ['Postgres']}}})

    def test_oversized_approval_is_declined_without_truncation(self):
        conn, events, relay = self.relay()
        relay.request(self.request(command='x' * 70000))
        self.assertEqual(conn.sent[-1]['result'], {'decision': 'decline'})
        self.assertFalse(relay.pending)

    def test_claude_response_preserves_input_and_does_not_save_rules(self):
        conn, events, relay = self.relay('Claude')
        request = {'type': 'control_request', 'request_id': 'claude', 'request': {'subtype': 'can_use_tool', 'tool_name': 'Write', 'input': {'file_path': '/tmp/example', 'content': 'hello'}}}
        relay.request(request)
        relay.resolve({'id': events[0]['id'], 'allow': True})
        response = conn.sent[-1]['response']['response']
        self.assertEqual(response, {'behavior': 'allow', 'updatedInput': request['request']['input']})
        self.assertNotIn('updatedPermissions', response)

    def test_commands_preserve_profile_configuration(self):
        source = {'provider': 'Claude', 'sessionID': '00000000-0000-0000-0000-000000000001'}
        command = bridge.runtime_command(source)
        result = execution_command(source, command, new_session=True)
        self.assertNotIn('--safe-mode', result)
        self.assertNotIn('--strict-mcp-config', result)
        self.assertNotIn('--resume', result)
        self.assertNotIn('--tools', result)
        self.assertNotIn('--dangerously-skip-permissions', result)
        self.assertIn('--session-id', result)
        self.assertIn('--permission-prompt-tool', result)
        self.assertEqual(execution_command({'provider': 'Codex'}, []), ['codex', 'app-server', '--stdio'])

    def test_implementation_requires_approval_before_process_creation(self):
        with self.assertRaisesRegex(ValueError, 'Approve the implementation'):
            conversation.run({'session': {}, 'prompt': 'change files', 'mode': 'implementation'})

    def run_process(self, provider, code):
        with tempfile.TemporaryDirectory() as root:
            old = bridge.SUPPORT; bridge.SUPPORT = Path(root) / 'support'
            controls, events = queue.Queue(), []
            class Input:
                def __iter__(self):
                    while True:
                        value = controls.get()
                        if value is None: return
                        yield value
            def emit(event):
                events.append(event)
                if event['type'] == 'approval':
                    controls.put(json.dumps({'type': 'approval', 'id': event['id'], 'allow': True}) + '\n')
            try:
                conversation.run({'session': {'provider': provider, 'id': 'logical', 'sessionID': '00000000-0000-0000-0000-000000000001', 'root': root, 'cwd': root},
                                  'prompt': 'implement', 'mode': 'implementation', 'executionApproved': True, 'newSession': True},
                                 Input(), emit, [sys.executable, '-u', '-c', code])
                self.assertEqual(events[-1]['type'], 'done')
                self.assertEqual(sum(e['type'] == 'approval' for e in events), 1)
                self.assertEqual(sum(e['type'] == 'session' for e in events), 1)
            finally:
                controls.put(None); bridge.SUPPORT = old

    def test_codex_buffers_approval_until_turn_ack(self):
        self.run_process('Codex', '''import json,sys
def read():return json.loads(sys.stdin.readline())
def out(v):print(json.dumps(v),flush=True)
r=read();out({'id':r['id'],'result':{}})
read()
r=read();out({'id':r['id'],'result':{'config':{'approval_policy':'on-request'}}})
r=read();assert r['method']=='thread/start';assert 'sandbox' not in r['params'];assert 'approvalPolicy' not in r['params'];assert not r['params'].get('ephemeral');out({'id':r['id'],'result':{'thread':{'id':'created'}}})
r=read();assert r['method']=='turn/start';assert 'approvalPolicy' not in r['params']
out({'id':50,'method':'item/commandExecution/requestApproval','params':{'threadId':'created','turnId':'turn','itemId':'command','command':'echo example'}})
out({'id':r['id'],'result':{'turn':{'id':'turn'}}})
r=read();assert r=={'id':50,'result':{'decision':'accept'}}
out({'method':'turn/completed','params':{'threadId':'created','turn':{'id':'turn','status':'completed'}}})
''')

    def test_claude_waits_for_initialized_permission_channel(self):
        self.run_process('Claude', '''import json,sys
def read():return json.loads(sys.stdin.readline())
def out(v):print(json.dumps(v),flush=True)
r=read();assert r['request']['subtype']=='initialize';out({'type':'control_response','response':{'request_id':r['request_id'],'subtype':'success','response':{}}})
r=read();assert r['type']=='user'
out({'type':'system','subtype':'init'})
out({'type':'control_request','request_id':'approval','request':{'subtype':'can_use_tool','tool_name':'Bash','input':{'command':'echo example'}}})
r=read();assert r['response']['response']=={'behavior':'allow','updatedInput':{'command':'echo example'}}
out({'type':'result','is_error':False,'result':'implemented'})
''')


if __name__ == '__main__': unittest.main()
