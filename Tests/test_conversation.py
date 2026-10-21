import io
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / 'Resources'))
import bridge
import conversation


class ConversationTests(unittest.TestCase):
    def test_output_backpressure_and_close_join_readers(self):
        with tempfile.TemporaryDirectory() as root:
            c = conversation.NativeConnection([sys.executable, '-u', '-c',
                "import json;[(print(json.dumps({'type':'notice','text':'x'*1000}),flush=True)) for _ in range(10000)]"], root, os.environ.copy())
            time.sleep(.1)
            self.assertLessEqual(c.events.qsize(), 16)
            c.close()
            self.assertIsNotNone(c.child.poll())
            self.assertFalse(any(reader.is_alive() for reader in c.readers))

    def test_oversized_native_event_fails_instead_of_growing(self):
        with tempfile.TemporaryDirectory() as root:
            c = conversation.NativeConnection([sys.executable, '-u', '-c',
                "print('x'*(2*1024*1024),flush=True)"], root, os.environ.copy())
            try:
                with self.assertRaisesRegex(RuntimeError, 'size limit'):
                    c.rpc('initialize', {})
            finally:
                c.close()

    def test_cancel_unblocks_an_unacknowledged_rpc(self):
        with tempfile.TemporaryDirectory() as root:
            c = conversation.NativeConnection([sys.executable, '-u', '-c',
                "import time;time.sleep(10)"], root, os.environ.copy())
            try:
                c.cancelled.set()
                started = time.monotonic()
                with self.assertRaises(InterruptedError):
                    c.rpc('initialize', {})
                self.assertLess(time.monotonic() - started, .5)
            finally:
                c.close()

    def run_fake(self, provider, code, controls=''):
        with tempfile.TemporaryDirectory() as root:
            old = bridge.SUPPORT
            bridge.SUPPORT = Path(root) / 'support'
            try:
                events = []
                s = {'provider': provider, 'id': 'test', 'sessionID': 'test', 'cwd': root, 'root': root}
                # Keep stdin open while the fake emits its terminal event.
                class Input:
                    def __iter__(self):
                        import time
                        for line in controls.splitlines(True):
                            time.sleep(.1)
                            yield line
                        time.sleep(1)
                conversation.run({'session': s, 'prompt': 'test'}, Input(), events.append,
                                 [sys.executable, '-u', '-c', code])
                return events
            finally:
                bridge.SUPPORT = old

    def test_codex_steer_checks_active_turn_and_keeps_streaming(self):
        code = '''import sys,json
def read():return json.loads(sys.stdin.readline())
def out(v):print(json.dumps(v),flush=True)
for _ in range(5):
 d=read()
 if 'id' not in d:continue
 result={}
 if d['method']=='config/read':result={'config':{'mcp_servers':{'server.with.dot':{}}}}
 if d['method']=='thread/resume':
  assert d['params']['config']['mcp_servers']['server.with.dot']['enabled'] is False
 if d['method']=='turn/start':result={'turn':{'id':'active'}}
 out({'id':d['id'],'result':result})
d=read();assert d['method']=='turn/steer';assert d['params']['expectedTurnId']=='active'
out({'id':d['id'],'result':{'turnId':'active'}})
out({'method':'item/agentMessage/delta','params':{'threadId':'test','turnId':'active','itemId':'reply','delta':'updated'}})
out({'method':'turn/completed','params':{'turn':{'id':'active','status':'completed'}}})
'''
        events = self.run_fake('Codex', code, json.dumps({'type':'steer','id':'s','text':'change'})+'\n')
        self.assertIn({'type':'steerAccepted','requestID':'s'}, events)
        self.assertEqual(events[-1]['type'], 'done')
        self.assertEqual(events[-2]['text'], 'updated')

    def test_claude_interrupt_waits_for_ack_and_turn_boundary(self):
        code = '''import sys,json
def read():return json.loads(sys.stdin.readline())
def out(v):print(json.dumps(v),flush=True)
assert read()['message']['content']=='test'
out({'type':'system','subtype':'init'})
d=read();assert d['request']['subtype']=='interrupt'
out({'type':'control_response','response':{'subtype':'success','request_id':d['request_id']}})
out({'type':'result','is_error':True})
d=read();assert d['message']['content']=='change'
out(d)
out({'type':'result','result':'redirected','is_error':False})
'''
        events = self.run_fake('Claude', code, json.dumps({'type':'steer','id':'s','text':'change'})+'\n')
        self.assertIn({'type':'boundary'}, events)
        self.assertIn({'type':'steerAccepted','requestID':'s'}, events)
        self.assertEqual(events[-1]['type'], 'done')

    def test_codex_rejects_stale_steering_without_claiming_delivery(self):
        code = '''import sys,json
def read():return json.loads(sys.stdin.readline())
def out(v):print(json.dumps(v),flush=True)
for _ in range(5):
 d=read()
 if 'id' not in d:continue
 out({'id':d['id'],'result':{'turn':{'id':'active'}} if d['method']=='turn/start' else {}})
d=read();out({'id':d['id'],'error':{'message':'turn already ended'}})
out({'method':'turn/completed','params':{'turn':{'id':'active','status':'completed'}}})
'''
        events = self.run_fake('Codex', code, json.dumps({'type':'steer','id':'s','text':'change'})+'\n')
        self.assertTrue(any(e['type']=='steerRejected' for e in events))
        self.assertFalse(any(e['type']=='steerAccepted' for e in events))


if __name__ == '__main__': unittest.main()
