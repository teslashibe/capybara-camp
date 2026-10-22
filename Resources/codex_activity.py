"""Read-only Codex activity from its daemon and verified live rollout writers.

The desktop may use a private stdio server rather than the shared daemon. A
persisted turn marker is activity evidence only while a Codex process holds the
same rollout inode open for writing. A saved log or its modification time is
never sufficient. No monitoring path starts or resumes a provider session.
"""
import base64
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import queue
import signal
import socket
import sqlite3
import stat
import struct
import subprocess
import tempfile
import threading
import time

MAX_FRAME = 1024 * 1024
MAX_THREADS = 2048
MARKERS = (b'task_started', b'turn_started', b'task_complete', b'turn_complete', b'turn_aborted')


def state_of(status):
    if not isinstance(status, dict):
        return 'unknown'
    if status.get('type') == 'active':
        flags = status.get('activeFlags', [])
        if not isinstance(flags, list) or not all(isinstance(x, str) for x in flags): return 'unknown'
        return 'waiting' if set(flags) & {'waitingOnApproval', 'waitingOnUserInput'} else 'running'
    return {'idle': 'idle', 'notLoaded': None, 'systemError': 'unknown'}.get(status.get('type'), 'unknown')


class ReadOnlyDaemon:
    """Small bounded WebSocket client for the local Unix control socket."""
    def __init__(self, root):
        path = Path(root) / 'app-server-control/app-server-control.sock'
        info = path.stat()
        if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError('The Codex control socket is not private to this macOS user')
        self.socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.socket.settimeout(3)
        self.buffer = bytearray()
        self.fragment = bytearray()
        self.fragmented = False
        self.serial = 0
        self.events = []
        try:
            # Use the short rendezvous path when possible; the CLI also uses this socket.
            self.socket.connect(str(path))
            key = base64.b64encode(os.urandom(16)).decode('ascii')
            request = ('GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n'
                       'Connection: Upgrade\r\nSec-WebSocket-Key: ' + key + '\r\nSec-WebSocket-Version: 13\r\n\r\n')
            self.socket.sendall(request.encode('ascii'))
            while b'\r\n\r\n' not in self.buffer:
                chunk = self.socket.recv(4096)
                if not chunk: raise EOFError('Codex closed the handshake')
                self.buffer.extend(chunk)
                if len(self.buffer) > 16384: raise ValueError('Codex handshake is too large')
            header, rest = bytes(self.buffer).split(b'\r\n\r\n', 1)
            self.buffer = bytearray(rest)
            fields = dict(line.split(b':', 1) for line in header.split(b'\r\n')[1:] if b':' in line)
            expected = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest())
            if not header.startswith(b'HTTP/1.1 101 ') or next((v.strip() for k, v in fields.items() if k.lower() == b'sec-websocket-accept'), b'') != expected:
                raise ValueError('Codex refused the local observer connection')
            self.rpc('initialize', {'clientInfo': {'name': 'capybara_activity', 'version': '1.0'}, 'capabilities': {'experimentalApi': True}})
            self.send({'method': 'initialized', 'params': {}})
        except BaseException:
            self.close()
            raise

    def close(self):
        self.socket.close()

    def _send_frame(self, opcode, data):
        mask = os.urandom(4)
        n = len(data)
        if n > MAX_FRAME: raise ValueError('Observer request is too large')
        head = bytes([0x80 | opcode, 0x80 | n]) if n < 126 else bytes([0x80 | opcode, 0xFE]) + struct.pack('!H', n) if n < 65536 else bytes([0x80 | opcode, 0xFF]) + struct.pack('!Q', n)
        self.socket.sendall(head + mask + bytes(value ^ mask[index % 4] for index, value in enumerate(data)))

    def send(self, value):
        if value.get('method') not in {'initialize', 'initialized', 'thread/loaded/list', 'thread/read'}:
            raise ValueError('Only read-only Codex observation is permitted')
        self._send_frame(1, json.dumps(value, separators=(',', ':')).encode())

    def receive(self, timeout=1):
        deadline = time.monotonic() + timeout
        while True:
            if len(self.buffer) >= 2:
                first, second = self.buffer[:2]
                opcode, final = first & 15, bool(first & 128)
                if first & 0x70 or second & 128: raise ValueError('Unsupported Codex frame')
                n, offset = second & 127, 2
                if n == 126:
                    if len(self.buffer) < 4: n = None
                    else: n, offset = struct.unpack('!H', self.buffer[2:4])[0], 4
                elif n == 127:
                    if len(self.buffer) < 10: n = None
                    else: n, offset = struct.unpack('!Q', self.buffer[2:10])[0], 10
                if n is not None:
                    if n > MAX_FRAME or len(self.fragment) + n > MAX_FRAME: raise ValueError('Codex activity frame is too large')
                    if len(self.buffer) >= offset + n:
                        payload = bytes(self.buffer[offset:offset+n]); del self.buffer[:offset+n]
                        if opcode >= 8:
                            if not final or n > 125: raise ValueError('Invalid control frame')
                            if opcode == 8: raise EOFError('Codex observer closed')
                            if opcode == 9: self._send_frame(10, payload)
                            elif opcode != 10: raise ValueError('Unsupported control frame')
                            continue
                        if opcode == 1 and not self.fragmented:
                            self.fragment = bytearray(payload); self.fragmented = not final
                        elif opcode == 0 and self.fragmented:
                            self.fragment.extend(payload); self.fragmented = not final
                        else: raise ValueError('Unsupported Codex message')
                        if self.fragmented: continue
                        value = json.loads(self.fragment); self.fragment.clear()
                        if not isinstance(value, dict): raise ValueError('Invalid Codex event')
                        return value
            remaining = deadline - time.monotonic()
            if remaining <= 0: raise TimeoutError()
            self.socket.settimeout(remaining)
            try: chunk = self.socket.recv(65536)
            except socket.timeout: raise TimeoutError() from None
            if not chunk: raise EOFError('Codex observer closed')
            self.buffer.extend(chunk)

    def rpc(self, method, params, timeout=4):
        self.serial += 1; ident = self.serial
        self.send({'id': ident, 'method': method, 'params': params})
        deadline = time.monotonic() + timeout
        while True:
            if time.monotonic() >= deadline: raise TimeoutError()
            value = self.receive(max(0, deadline - time.monotonic()))
            if value.get('id') == ident and 'method' not in value:
                if 'error' in value: raise ValueError('Codex rejected read-only activity observation')
                result = value.get('result', {})
                if not isinstance(result, dict): raise ValueError('Invalid Codex response')
                return result
            # Never answer another client's approval or user-input request.
            if 'id' not in value and value.get('method') in {'thread/status/changed', 'thread/started', 'thread/closed'}:
                if len(self.events) >= MAX_THREADS: raise ValueError('Too many Codex status events')
                self.events.append(value)

    def snapshot(self):
        loaded, cursor, seen = [], None, set()
        while True:
            result = self.rpc('thread/loaded/list', {'limit': 200, 'cursor': cursor})
            ids = result.get('data')
            if not isinstance(ids, list) or any(not isinstance(x, str) for x in ids): raise ValueError('Invalid loaded thread list')
            loaded.extend(ids)
            if len(loaded) > MAX_THREADS or len(set(loaded)) != len(loaded): raise ValueError('Too many loaded Codex threads')
            cursor = result.get('nextCursor')
            if not cursor: break
            if not isinstance(cursor, str): raise ValueError('Invalid Codex page cursor')
            if cursor in seen: raise ValueError('Repeated Codex page cursor')
            seen.add(cursor)
        rows = {}
        for ident in loaded:
            value = self.rpc('thread/read', {'threadId': ident, 'includeTurns': False}).get('thread')
            if isinstance(value, dict) and value.get('id') == ident: rows[ident] = metadata_only(value)
        return rows


def metadata_only(value):
    return {key: value[key] for key in ('id', 'title', 'name', 'cwd', 'path', 'model', 'source', 'sourceKind', 'parentThreadId', 'agentNickname', 'agentRole', 'status', 'updatedAt', 'archived') if key in value}


def is_child(kind):
    return (isinstance(kind, str) and kind.startswith('{"subagent"')) or (isinstance(kind, dict) and any(key in kind for key in ('subAgent', 'subagent')))


def capture(command, limit, timeout, stopping=None):
    """Bound process output and reap probes, including during observer shutdown."""
    with tempfile.TemporaryFile() as output:
        process = subprocess.Popen(command, stdout=output, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + timeout
        try:
            while process.poll() is None:
                if stopping is not None and stopping.is_set(): raise InterruptedError('Observer stopped')
                if time.monotonic() >= deadline: raise subprocess.TimeoutExpired(command[0], timeout)
                if os.fstat(output.fileno()).st_size > limit: raise ValueError('Codex probe output is too large')
                if stopping is None: time.sleep(.02)
                else: stopping.wait(.02)
            if os.fstat(output.fileno()).st_size > limit: raise ValueError('Codex probe output is too large')
            output.seek(0)
            return process.returncode, output.read(limit + 1)
        finally:
            if process.poll() is None: process.kill()
            process.wait()


def writer_files(sources, stopping=None):
    """Return source, path, PID and inode identity from current writable handles."""
    code, output = capture(['/bin/ps', '-axo', 'pid=,comm='], 4 * MAX_FRAME, 3, stopping)
    if code: raise ValueError('Could not inspect Codex processes')
    pids = []
    for line in output.decode('utf-8', 'replace').splitlines():
        parts = line.strip().split(None, 1)
        if len(parts) == 2 and parts[0].isdigit() and Path(parts[1]).name == 'codex': pids.append(parts[0])
    if not pids: return []
    if len(pids) > 256: raise ValueError('Too many Codex processes to observe')
    code, output = capture(['/usr/sbin/lsof', '-nP', '-a', '-p', ','.join(pids), '-F0pfatDin'], 8 * MAX_FRAME, 5, stopping)
    if code not in (0, 1): raise ValueError('Could not inspect Codex session ownership')
    roots = [(s['id'], (Path(s['root']).resolve() / 'sessions'), (Path(s['root']).resolve() / 'archived_sessions')) for s in sources]
    files, pid, item = [], None, {}
    def finish():
        if not pid or item.get('a') not in ('w', 'u') or item.get('t') != 'REG' or not item.get('n'): return
        path = Path(item['n'])
        if path.suffix != '.jsonl': return
        try:
            path = path.resolve(); info = path.stat()
            if not item.get('i') or int(item['i']) != info.st_ino: return
            if not item.get('D') or int(item['D'], 16) != info.st_dev: return
        except (OSError, ValueError): return
        for source_id, active, archived in roots:
            if path.is_relative_to(active) or path.is_relative_to(archived):
                files.append((source_id, str(path), int(pid), info.st_dev, info.st_ino)); return
    for field in output.split(b'\0'):
        field = field.lstrip(b'\n')
        if not field: continue
        name, value = chr(field[0]), field[1:].decode('utf-8', 'surrogateescape')
        if name == 'p': finish(); item = {}; pid = value
        elif name == 'f': finish(); item = {}
        else: item[name] = value
    finish()
    return list(dict.fromkeys(files))


class RolloutState:
    """Incremental bounded parser; output/reasoning text is never retained."""
    def __init__(self, path):
        self.path = path; self.identity = None; self.offset = 0
        self.partial = b''; self.discard = False; self.state = 'unknown'; self.turn = None; self.session_id = None

    def consume(self, raw):
        if not self.session_id:
            try:
                value = json.loads(raw)
                if isinstance(value, dict) and value.get('type') == 'session_meta': self.session_id = value.get('payload', {}).get('id')
            except (ValueError, AttributeError): pass
        if not any(marker in raw for marker in MARKERS): return
        try: value = json.loads(raw)
        except ValueError: return
        if not isinstance(value, dict): return
        payload = value.get('payload', {})
        if value.get('type') != 'event_msg' or not isinstance(payload, dict): return
        kind, turn = payload.get('type'), payload.get('turn_id')
        if kind in ('task_started', 'turn_started'):
            self.turn, self.state = turn, 'running'
        elif kind in ('task_complete', 'turn_complete', 'turn_aborted') and (turn is None or self.turn is None or turn == self.turn):
            self.state = 'idle'

    def update(self, budget=8 * MAX_FRAME, expected=None):
        with open(self.path, 'rb') as f:
            info = os.fstat(f.fileno())
            identity = (info.st_dev, info.st_ino)
            if expected is not None and expected != identity: raise OSError('Codex rollout writer changed')
            if identity != self.identity or info.st_size < self.offset:
                self.identity = identity; self.offset = 0; self.partial = b''; self.discard = False; self.state = 'unknown'; self.turn = None; self.session_id = None
            f.seek(self.offset)
            while budget > 0:
                chunk = f.read(min(65536, budget))
                if not chunk: break
                self.offset += len(chunk); budget -= len(chunk)
                parts = chunk.split(b'\n')
                for index, part in enumerate(parts):
                    if not self.discard:
                        self.partial += part
                        if len(self.partial) > MAX_FRAME: self.partial = b''; self.discard = True
                    if index < len(parts) - 1:
                        if not self.discard: self.consume(self.partial)
                        self.partial = b''; self.discard = False
        # Historical intermediate state is not a current observation until caught up.
        return self.state if self.offset >= info.st_size else 'unknown'


def session_for(source, ident, path='', metadata=None):
    from bridge import db_open, session
    metadata = dict(metadata or {})
    db_path = Path(source['root']) / 'state_5.sqlite'
    if db_path.is_file():
        try:
            with closing(db_open(db_path)) as db:
                columns = {x[1] for x in db.execute('PRAGMA table_info(threads)')}
                names = [x for x in ('id', 'title', 'cwd', 'rollout_path', 'updated_at', 'model', 'source', 'archived') if x in columns]
                row = db.execute('SELECT ' + ','.join(names) + ' FROM threads WHERE id=?', (ident,)).fetchone()
                if row: metadata = {**metadata, **dict(row)}
        except (OSError, ValueError, sqlite3.Error): pass
    return session(source, ident, metadata.get('title') or metadata.get('name') or 'Codex session', metadata.get('cwd', ''),
                   metadata.get('updated_at', metadata.get('updatedAt', 0)), path or metadata.get('rollout_path') or metadata.get('path') or '',
                   model=metadata.get('model') or '', archived=bool(metadata.get('archived', False))), metadata.get('source', metadata.get('sourceKind'))


def snapshot(sources, writers, indexes, resolve=session_for):
    by_source = {s['id']: s for s in sources}
    live, sessions, paths = [], [], set()
    for source_id, path, pid, device, inode in writers:
        paths.add(path)
        try:
            index = indexes.setdefault(path, RolloutState(path)); state = index.update(expected=(device, inode))
            ident = index.session_id
            if not isinstance(ident, str) or not ident or len(ident) > 200: continue
            s, source_kind = resolve(by_source[source_id], ident, path)
            children = []
            if not is_child(source_kind): sessions.append(s)
            live.append({'id': s['id'], 'sourceID': source_id, 'state': state, 'model': s['model'], 'children': children, 'owner': 'Codex', 'pid': pid})
        except (OSError, ValueError): continue
    for path in list(indexes):
        if path not in paths: del indexes[path]
    return live, sessions


def watch(sources, emit):
    if not isinstance(sources, list) or any(not isinstance(s, dict) for s in sources): raise ValueError('Invalid Codex profiles')
    sources = [s for s in sources if s.get('provider') == 'Codex']
    if len(sources) > 32: raise ValueError('At most 32 Codex profiles can be observed at once')
    if any(not all(isinstance(s.get(k), str) and s[k] for k in ('id', 'root', 'label')) for s in sources): raise ValueError('Invalid Codex profile')
    if len({s['id'] for s in sources}) != len(sources): raise ValueError('Duplicate Codex profile')
    stopping = threading.Event(); changes = threading.Event(); lock = threading.Lock()
    daemon_rows = {}; connections = {}; indexes = {}; writer_state = {'files': [], 'error': ''}; metadata_cache = {}; child_cache = {}
    def daemon(source):
        delay = .5
        while not stopping.is_set():
            connection = None
            try:
                connection = ReadOnlyDaemon(source['root'])
                with lock: connections[source['id']] = connection
                if stopping.is_set(): break
                rows = connection.snapshot()
                with lock: daemon_rows[source['id']] = dict(rows)
                changes.set(); delay = .5
                while not stopping.is_set():
                    try: value = connection.events.pop(0) if connection.events else connection.receive(.5)
                    except TimeoutError: continue
                    if 'id' in value: continue
                    method, params = value.get('method'), value.get('params', {})
                    if not isinstance(params, dict): raise ValueError('Invalid Codex event parameters')
                    ident = params.get('threadId')
                    if method == 'thread/started':
                        thread = params.get('thread', {})
                        if not isinstance(thread, dict): raise ValueError('Invalid Codex thread metadata')
                        ident = thread.get('id')
                        if isinstance(ident, str): rows[ident] = metadata_only(thread)
                    elif method == 'thread/status/changed' and isinstance(ident, str):
                        if ident not in rows:
                            thread = connection.rpc('thread/read', {'threadId': ident, 'includeTurns': False}).get('thread')
                            if isinstance(thread, dict): rows[ident] = metadata_only(thread)
                        if ident in rows: rows[ident] = {**rows[ident], 'status': params.get('status')}
                    elif method == 'thread/closed' and isinstance(ident, str): rows.pop(ident, None)
                    else: continue
                    if len(rows) > MAX_THREADS: raise ValueError('Too many Codex threads')
                    with lock: daemon_rows[source['id']] = dict(rows)
                    changes.set()
            except (OSError, ValueError, EOFError, TimeoutError):
                with lock: daemon_rows.pop(source['id'], None)
                changes.set(); stopping.wait(delay); delay = min(10, delay * 2)
            finally:
                with lock: connections.pop(source['id'], None)
                if connection: connection.close()
    def discover():
        while not stopping.is_set():
            try: value = {'files': writer_files(sources, stopping), 'error': ''}
            except (OSError, ValueError, subprocess.TimeoutExpired): value = {'files': [], 'error': 'Codex desktop session ownership is unavailable'}
            with lock: writer_state.update(value)
            changes.set(); stopping.wait(2)
    def resolve(source, ident, path='', metadata=None):
        key = (source['id'], ident)
        cached = metadata_cache.get(key)
        if cached is None or time.monotonic() - cached[0] > 2:
            cached = (time.monotonic(), session_for(source, ident, path, metadata)); metadata_cache[key] = cached
        return cached[1]
    workers = [threading.Thread(target=daemon, args=(source,), daemon=True) for source in sources]
    workers.append(threading.Thread(target=discover, daemon=True))
    for worker in workers: worker.start()
    previous = None
    def stop(*_): stopping.set(); changes.set()
    prior = {sig: signal.signal(sig, stop) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        while not stopping.is_set():
            with lock: writers, writer_error = list(writer_state['files']), writer_state['error']
            live, sessions = snapshot(sources, writers, indexes, resolve)
            by_id = {x['id']: x for x in live}; by_session = {x['id']: x for x in sessions}
            with lock: current = {key: dict(value) for key, value in daemon_rows.items()}
            for source in sources:
                for ident, row in current.get(source['id'], {}).items():
                    state = state_of(row.get('status'))
                    if state is None: continue
                    s, kind = resolve(source, ident, metadata=row)
                    by_id[s['id']] = {'id': s['id'], 'sourceID': source['id'], 'state': state, 'model': s['model'], 'children': [], 'owner': 'Codex', 'pid': 0}
                    if not is_child(kind): by_session[s['id']] = s
            # Reuse the persisted child edges, but only these live observations supply child status.
            from activity import codex_children
            for value in by_id.values():
                parent = by_session.get(value['id'])
                if parent:
                    cached = child_cache.get(parent['id'])
                    if cached is None or time.monotonic() - cached[0] > 2:
                        cached = (time.monotonic(), codex_children(parent)); child_cache[parent['id']] = cached
                    value['children'] = [dict(child) for child in cached[1]]
                    for child in value['children']:
                        observed = by_id.get(parent['sourceID'] + ':' + child['sessionID'])
                        if observed: child['status'] = {'running':'running', 'idle':'done', 'waiting':'waiting'}.get(observed['state'], 'unknown')
            present = set(by_id)
            metadata_cache = {key: value for key, value in metadata_cache.items() if key[0] + ':' + key[1] in present}
            child_cache = {key: value for key, value in child_cache.items() if key in present}
            result = {'activity': list(by_id.values()), 'sessions': list(by_session.values()), 'errors': {s['id']: writer_error for s in sources if writer_error and s['id'] not in current}}
            wire = json.dumps(result, sort_keys=True, separators=(',', ':'))
            if len(wire.encode()) > 2 * MAX_FRAME or len(by_id) > 8192:
                result = {'activity': [], 'sessions': [], 'errors': {s['id']: 'Codex activity exceeded its size limit' for s in sources}}
                wire = json.dumps(result, sort_keys=True)
            if wire != previous: emit(result); previous = wire
            changes.wait(.1); changes.clear()
    finally:
        stopping.set()
        with lock: active_connections = list(connections.values())
        for connection in active_connections:
            try: connection.socket.shutdown(socket.SHUT_RDWR)
            except OSError: pass
        deadline = time.monotonic() + 5
        for worker in workers: worker.join(timeout=max(0, deadline - time.monotonic()))
        for sig, handler in prior.items(): signal.signal(sig, handler)


def assert_available(session):
    """A final observation before resume; never attach to an externally owned thread."""
    root = Path(session['root'])
    if not (root / 'sessions').is_dir() and not (root / 'state_5.sqlite').is_file(): return
    source = {'id': session['sourceID'], 'provider': 'Codex', 'root': str(root), 'label': session.get('sourceLabel', 'Codex')}
    try:
        for _, path, _, _, _ in writer_files([source]):
            if session.get('path') and Path(session['path']).resolve() == Path(path):
                raise ValueError('This session is open in Codex. Close it there before sending from Camp.')
            with open(path, 'rb') as f:
                raw = f.readline(MAX_FRAME + 1)
            if len(raw) <= MAX_FRAME:
                meta = json.loads(raw)
                if meta.get('type') == 'session_meta' and meta.get('payload', {}).get('id') == session['sessionID']:
                    raise ValueError('This session is open in Codex. Close it there before sending from Camp.')
        if (root / 'app-server-control/app-server-control.sock').exists():
            connection = ReadOnlyDaemon(root)
            try:
                cursor = None; seen = set(); total = 0
                while True:
                    result = connection.rpc('thread/loaded/list', {'limit': 200, 'cursor': cursor})
                    ids = result.get('data', [])
                    if not isinstance(ids, list) or any(not isinstance(x, str) for x in ids): raise ValueError('Invalid Codex loaded session list')
                    total += len(ids)
                    if total > MAX_THREADS: raise ValueError('Codex has too many loaded sessions to verify ownership')
                    if session['sessionID'] in ids: raise ValueError('This session is open in Codex. Close it there before sending from Camp.')
                    cursor = result.get('nextCursor')
                    if not cursor: break
                    if not isinstance(cursor, str): raise ValueError('Invalid Codex page cursor')
                    if cursor in seen: raise ValueError('Codex returned a repeated page')
                    seen.add(cursor)
            finally: connection.close()
    except (OSError, TimeoutError, EOFError, subprocess.TimeoutExpired) as error:
        raise ValueError('Codex session ownership could not be verified. Retry after its runtime is available.') from error
