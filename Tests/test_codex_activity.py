import base64
import hashlib
import json
import os
from pathlib import Path
import select
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1] / 'Resources'))
import codex_activity as activity


def frame(value, opcode=1, final=True):
    data = json.dumps(value).encode() if isinstance(value, dict) else value
    n = len(data)
    head = bytes([(128 if final else 0) | opcode, n]) if n < 126 else bytes([(128 if final else 0) | opcode, 126]) + struct.pack('!H', n)
    return head + data


def exact(sock, count):
    data = b''
    while len(data) < count:
        part = sock.recv(count - len(data))
        if not part: raise EOFError()
        data += part
    return data


def client_frame(sock):
    first, second = exact(sock, 2)
    count = second & 127
    if count == 126: count = struct.unpack('!H', exact(sock, 2))[0]
    elif count == 127: count = struct.unpack('!Q', exact(sock, 8))[0]
    mask = exact(sock, 4) if second & 128 else b'\0' * 4
    payload = exact(sock, count)
    return first & 15, bytes(x ^ mask[i % 4] for i, x in enumerate(payload))


class DaemonFixture:
    def __init__(self, root):
        path = root / 'app-server-control/app-server-control.sock'
        path.parent.mkdir()
        self.server = socket.socket(socket.AF_UNIX)
        self.server.bind(str(path)); os.chmod(path, 0o600); self.server.listen()
        self.methods = []; self.client = None; self.ready = threading.Event(); self.error = None
        self.row = {'id': 'parent', 'title': 'Fixture plan', 'cwd': '/tmp', 'status': {'type': 'active', 'activeFlags': []},
                    'turns': [{'secret_transcript': 'must not leave the observer'}]}
        self.thread = threading.Thread(target=self.run, daemon=True); self.thread.start()

    def run(self):
        try:
            self.client, _ = self.server.accept(); self.client.settimeout(8)
            data = b''
            while b'\r\n\r\n' not in data: data += self.client.recv(4096)
            key = next(x.split(b':', 1)[1].strip() for x in data.split(b'\r\n') if x.lower().startswith(b'sec-websocket-key:'))
            accept = base64.b64encode(hashlib.sha1(key + b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest())
            self.client.sendall(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + b'\r\n\r\n')
            while True:
                opcode, raw = client_frame(self.client)
                if opcode != 1: continue
                value = json.loads(raw); method = value.get('method'); self.methods.append(method)
                if method == 'initialized': continue
                if method == 'initialize': result = {}
                elif method == 'thread/loaded/list': result = {'data': ['parent'], 'nextCursor': None}
                elif method == 'thread/read':
                    assert value['params']['includeTurns'] is False
                    result = {'thread': self.row}
                else: raise AssertionError('Mutation sent to read-only observer: ' + str(value))
                self.client.sendall(frame({'id': value['id'], 'result': result}))
                if method == 'thread/read': self.ready.set()
        except (EOFError, OSError): pass
        except BaseException as error: self.error = error

    def send(self, value):
        assert self.ready.wait(5)
        self.client.sendall(frame(value))

    def close(self):
        if self.client:
            try: self.client.shutdown(socket.SHUT_RDWR)
            except OSError: pass
            self.client.close()
        self.server.close(); self.thread.join(2)
        if self.error: raise self.error


class CodexActivityTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='camp-', dir='/tmp'); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = {'id': 'fixture', 'provider': 'Codex', 'root': str(self.root), 'label': 'Fixture'}

    def write(self, *values, path=None, append=False):
        path = path or self.root / 'rollout.jsonl'
        path.parent.mkdir(exist_ok=True, parents=True)
        with path.open('ab' if append else 'wb') as f:
            for value in values: f.write(json.dumps(value).encode() + b'\n')
        return path

    def event(self, kind, turn='one'):
        return {'type': 'event_msg', 'payload': {'type': kind, 'turn_id': turn}}

    def test_lifecycle_partial_lines_and_old_terminal_event(self):
        path = self.write({'type': 'session_meta', 'payload': {'id': 'parent'}}, self.event('task_started'))
        index = activity.RolloutState(path)
        self.assertEqual(index.update(), 'running'); self.assertEqual(index.session_id, 'parent')
        self.write(self.event('task_started', 'two'), self.event('task_complete'), path=path, append=True)
        self.assertEqual(index.update(), 'running')
        terminal = json.dumps(self.event('turn_aborted', 'two')).encode()
        with path.open('ab') as f: f.write(terminal[:15])
        self.assertEqual(index.update(), 'running')
        with path.open('ab') as f: f.write(terminal[15:] + b'\n')
        self.assertEqual(index.update(), 'idle')

    def test_text_markers_and_saved_log_age_are_not_live_evidence(self):
        path = self.write({'type': 'response_item', 'payload': {'text': 'task_started'}}, ['task_started'])
        self.assertEqual(activity.RolloutState(path).update(), 'unknown')
        index = {}; self.assertEqual(activity.snapshot([self.source], [], index), ([], []))

    def test_budget_replacement_and_expected_writer_inode(self):
        path = self.write(self.event('task_started'), {'text': 'x' * 500}, self.event('task_complete'))
        index = activity.RolloutState(path)
        self.assertEqual(index.update(budget=100), 'unknown')
        self.assertEqual(index.update(), 'idle')
        info = path.stat(); expected = (info.st_dev, info.st_ino)
        fresh = self.write(self.event('task_started', 'new'), path=self.root / 'new')
        fresh.replace(path)
        with self.assertRaises(OSError): index.update(expected=expected)
        self.assertEqual(index.update(), 'running')
        path.write_text('')
        self.assertEqual(index.update(), 'unknown')

    def test_oversized_line_is_skipped_without_retaining_it(self):
        path = self.write(self.event('task_started'))
        with path.open('ab') as f: f.write(b'x' * (activity.MAX_FRAME + 8) + b'\n')
        self.write(self.event('task_complete'), path=path, append=True)
        index = activity.RolloutState(path)
        self.assertEqual(index.update(), 'idle'); self.assertEqual(index.partial, b'')

    def test_writer_requires_real_writable_matching_inode_under_profile(self):
        path = self.write(self.event('task_started'), path=self.root / 'sessions/test.jsonl')
        info = path.stat()
        def fields(access='w', inode=info.st_ino, device=info.st_dev, file=path):
            return ('p123\0f7\0a' + access + '\0tREG\0D' + format(device, 'x') + '\0i' + str(inode) + '\0n' + str(file) + '\0\n').encode()
        with patch.object(activity, 'capture', side_effect=[(0, b'123 /usr/local/bin/codex\n555 /bin/other\n'), (0, fields())]) as capture:
            self.assertEqual(activity.writer_files([self.source]), [('fixture', str(path.resolve()), 123, info.st_dev, info.st_ino)])
            self.assertIn('123', capture.call_args.args[0])
        for output in (fields(access='r'), fields(inode=info.st_ino + 1), fields(device=0), fields(file=self.root / 'outside.jsonl')):
            with patch.object(activity, 'capture', side_effect=[(0, b'123 codex\n'), (0, output)]):
                self.assertEqual(activity.writer_files([self.source]), [])

    def test_snapshot_cleans_disappeared_files_and_excludes_child_cards(self):
        path = self.write({'type': 'session_meta', 'payload': {'id': 'child'}}, self.event('task_started'))
        info = path.stat(); writers = [('fixture', str(path), 123, info.st_dev, info.st_ino)]
        def resolve(source, ident, path):
            return {'id': source['id'] + ':' + ident, 'model': 'fixture'}, {'subAgent': {}}
        indexes = {}; live, cards = activity.snapshot([self.source], writers, indexes, resolve)
        self.assertEqual(live[0]['state'], 'running'); self.assertEqual(cards, [])
        activity.snapshot([self.source], [], indexes, resolve); self.assertEqual(indexes, {})

    def test_exact_states(self):
        self.assertEqual(activity.state_of({'type': 'active', 'activeFlags': ['waitingOnApproval']}), 'waiting')
        self.assertEqual(activity.state_of({'type': 'active', 'activeFlags': ['waitingOnUserInput']}), 'waiting')
        self.assertEqual(activity.state_of({'type': 'active', 'activeFlags': [{}]}), 'unknown')
        self.assertEqual(activity.state_of({'type': 'idle'}), 'idle')
        self.assertIsNone(activity.state_of({'type': 'notLoaded'}))
        row = activity.metadata_only({'id': 'child', 'source': {'subAgent': {}}, 'turns': [], 'parentThreadId': 'parent'})
        self.assertTrue(activity.is_child(row['source'])); self.assertNotIn('turns', row)

    def test_daemon_protocol_is_read_only_and_excludes_transcript(self):
        server = DaemonFixture(self.root); self.addCleanup(server.close)
        client = activity.ReadOnlyDaemon(self.root); self.addCleanup(client.close)
        self.assertNotIn('turns', client.snapshot()['parent'])
        with self.assertRaises(ValueError): client.send({'method': 'thread/resume'})
        server.send({'method': 'item/commandExecution/requestApproval', 'id': 90, 'params': {}})
        self.assertEqual(client.rpc('thread/loaded/list', {})['data'], ['parent'])
        self.assertTrue(set(server.methods) <= {'initialize', 'initialized', 'thread/read', 'thread/loaded/list'})

    def test_fragmented_frames_ping_timeout_and_oversize(self):
        receiver, sender = socket.socketpair(); self.addCleanup(receiver.close); self.addCleanup(sender.close)
        client = activity.ReadOnlyDaemon.__new__(activity.ReadOnlyDaemon)
        client.socket = receiver; client.buffer = bytearray(); client.fragment = bytearray(); client.fragmented = False
        wire = json.dumps({'method': 'thread/status/changed'}).encode()
        sender.sendall(frame(wire[:12], final=False) + frame(b'ping', opcode=9) + frame(wire[12:], opcode=0))
        self.assertEqual(client.receive(.1), {'method': 'thread/status/changed'})
        self.assertEqual(client_frame(sender), (10, b'ping'))
        with self.assertRaises(TimeoutError): client.receive(.01)
        sender.sendall(bytes([129, 127]) + struct.pack('!Q', activity.MAX_FRAME + 1))
        with self.assertRaises(ValueError): client.receive(.1)

    def test_private_socket_and_resume_ownership(self):
        server = DaemonFixture(self.root); self.addCleanup(server.close)
        path = self.root / 'app-server-control/app-server-control.sock'; os.chmod(path, 0o666)
        with self.assertRaisesRegex(ValueError, 'private'): activity.ReadOnlyDaemon(self.root)
        (self.root / 'sessions').mkdir()
        session = {**self.source, 'sourceID': self.source['id'], 'sessionID': 'parent', 'path': ''}
        log = self.write({'type': 'session_meta', 'payload': {'id': 'parent'}}, self.event('task_complete'), path=self.root / 'sessions/test.jsonl')
        info = log.stat()
        with patch.object(activity, 'writer_files', return_value=[('fixture', str(log), 123, info.st_dev, info.st_ino)]):
            with self.assertRaisesRegex(ValueError, 'open in Codex'): activity.assert_available(session)

    def test_capture_cancellation_reaps_child(self):
        stop = threading.Event(); stop.set()
        with self.assertRaises(InterruptedError): activity.capture([sys.executable, '-c', 'import time; time.sleep(30)'], 1000, 10, stop)
        with self.assertRaises(ValueError): activity.capture([sys.executable, '-c', 'print("x"*2000)'], 1000, 3)

    def test_bridge_streams_status_and_stops_its_observer(self):
        server = DaemonFixture(self.root); self.addCleanup(server.close)
        process = subprocess.Popen([sys.executable, str(Path(__file__).parents[1] / 'Resources/bridge.py'), 'codex-activity-watch'],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        def cleanup():
            if process.poll() is None: process.kill()
            process.wait(); process.stdin.close(); process.stdout.close(); process.stderr.close()
        self.addCleanup(cleanup)
        process.stdin.write(json.dumps([self.source]).encode() + b'\n'); process.stdin.flush()
        def await_state(wanted):
            deadline = time.monotonic() + 7
            while time.monotonic() < deadline:
                if not select.select([process.stdout], [], [], .1)[0]: continue
                line = process.stdout.readline(); self.assertTrue(line)
                value = json.loads(line)
                self.assertNotIn('secret_transcript', line.decode())
                if value.get('activity') and value['activity'][0]['state'] == wanted: return value
            self.fail('No live ' + wanted + ' update')
        await_state('running')
        server.send({'method': 'thread/status/changed', 'params': {'threadId': 'parent', 'status': {'type': 'active', 'activeFlags': ['waitingOnApproval']}}})
        await_state('waiting')
        server.send({'method': 'thread/status/changed', 'params': {'threadId': 'parent', 'status': {'type': 'idle'}}})
        await_state('idle')
        process.terminate(); self.assertEqual(process.wait(7), 0)


if __name__ == '__main__': unittest.main()
