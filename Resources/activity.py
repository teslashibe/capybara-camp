"""Read-only runtime observations; never infer activity from transcript age alone."""
import json
import os
from pathlib import Path
import re
import subprocess
import time


def read_json(path, default=None):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return {} if default is None else default


def json_lines(path, tail=None):
    try:
        with Path(path).open('rb') as f:
            if tail and f.seek(0, 2) > tail:
                f.seek(-tail, 2)
                f.readline()
            else:
                f.seek(0)
            for line in f:
                try:
                    value = json.loads(line)
                    if isinstance(value, dict):
                        yield value
                except ValueError:
                    continue
    except OSError:
        return


def claude_metadata(home=None):
    home = Path(home or Path.home())
    base = home / 'Library/Application Support/Claude/claude-code-sessions'
    result = {}
    for path in base.glob('*/*/local_*.json'):
        d = read_json(path)
        ident = d.get('cliSessionId')
        if ident and d.get('lastActivityAt', 0) >= result.get(ident, {}).get('updated', 0):
            result[ident] = {'title': d.get('title', ''), 'model': d.get('model', ''),
                             'updated': d.get('lastActivityAt', 0)}
    return result


def live_claude(source):
    from bridge import runtime_env
    root = Path(source['root'])
    if not any((root / 'sessions').glob('*.json')):
        return []
    command = ['claude', '--safe-mode', 'agents', '--json']
    p = subprocess.run(command, capture_output=True, text=True, timeout=8,
                       env=runtime_env({'provider': 'Claude', 'root': str(root)}))
    if p.returncode:
        raise ValueError('Claude live status is unavailable')
    try:
        rows = json.loads(p.stdout)
    except ValueError as e:
        raise ValueError('Claude returned invalid live status') from e
    if not isinstance(rows, list):
        raise ValueError('Claude returned unsupported live status')
    # The official CLI verifies live PIDs and process start identities. Stale
    # registry files, including reused PIDs, are not accepted as activity here.
    return [d for d in rows if isinstance(d, dict) and d.get('sessionId')]


def find_transcript(source, ident):
    return next((Path(source['root']) / 'projects').glob('*/' + ident + '.jsonl'), None)


def last_model(path):
    model = ''
    for d in json_lines(path, tail=256 * 1024):
        candidate = d.get('message', {}).get('model')
        if candidate and candidate != '<synthetic>':
            model = candidate
    return model


def codex_children(parent):
    from bridge import db_open
    from contextlib import closing
    import sqlite3
    path = Path(parent['root']) / 'state_5.sqlite'
    if not path.is_file(): return []
    try:
        with closing(db_open(path)) as db:
            cols = {r[1] for r in db.execute('PRAGMA table_info(threads)')}
            if not {'id','title','rollout_path','updated_at'}.issubset(cols): return []
            extra = [x for x in ('model','agent_nickname','agent_role') if x in cols]
            sql = """WITH RECURSIVE children(id) AS (
                SELECT child_thread_id FROM thread_spawn_edges WHERE parent_thread_id=?
                UNION SELECT e.child_thread_id FROM thread_spawn_edges e JOIN children c ON e.parent_thread_id=c.id)
                SELECT t.id,t.title,t.rollout_path,t.updated_at"""
            sql += ''.join(',t.'+x for x in extra)
            sql += ' FROM threads t JOIN children c ON t.id=c.id WHERE t.id<>?'
            return [{'id':r['id'], 'sessionID':r['id'], 'name': (r['agent_nickname'] if 'agent_nickname' in cols else '') or r['title'],
                     'phase': (r['agent_role'] if 'agent_role' in cols else '') or '', 'workflow':'Codex subagent',
                     'status':'unknown', 'model':(r['model'] if 'model' in cols else '') or '',
                     'path':r['rollout_path'] or '', 'updated':r['updated_at']} for r in db.execute(sql,(parent['sessionID'],parent['sessionID']))]
    except sqlite3.Error:
        return []


def children_for(parent, parent_busy=False):
    if parent.get('provider') == 'Codex': return codex_children(parent)
    if parent.get('provider') != 'Claude' or not parent.get('path'):
        return []
    folder = Path(parent['path']).with_suffix('')
    agents_dir = folder / 'subagents'
    journal_state = {}
    journals = list(agents_dir.glob('workflows/*/journal.jsonl'))
    # Only a live parent's newest unfinished workflow can supply a running
    # claim. Old interrupted journals remain unknown, never perpetually busy.
    unfinished = [p for p in journals if not (folder / 'workflows' / (p.parent.name + '.json')).exists()]
    latest = max(unfinished, key=lambda p: p.stat().st_mtime, default=None)
    for journal in journals:
        terminal_workflow = read_json(folder / 'workflows' / (journal.parent.name + '.json'))
        for d in json_lines(journal):
            ident = d.get('agentId')
            if not ident:
                continue
            key = (journal.parent.name, ident)
            row = journal_state.setdefault(key, {'name': ident, 'phase': '', 'status': 'unknown'})
            if d.get('type') == 'started':
                row.update(name=d.get('label') or ident, phase=d.get('phase', ''), status='running' if parent_busy and journal == latest else 'unknown')
            elif d.get('type') in ('result', 'completed'):
                row['status'] = 'done'
            elif d.get('type') in ('error', 'failed'):
                row['status'] = 'failed'
            elif d.get('type') in ('cancelled', 'stopped'):
                row['status'] = 'stopped'
            if terminal_workflow.get('status') in ('cancelled', 'stopped', 'failed') and row['status'] == 'running':
                row['status'] = terminal_workflow['status']
    result = []
    for path in agents_dir.rglob('agent-*.jsonl'):
        ident = path.stem.removeprefix('agent-')
        workflow = path.parent.name if path.parent != agents_dir else ''
        meta = read_json(path.with_suffix('.meta.json'))
        state = journal_state.get((workflow, ident), {})
        name = state.get('name') or meta.get('description') or ident
        status = state.get('status', 'unknown')
        if not state:
            for d in json_lines(path, tail=128 * 1024):
                if d.get('type') == 'system' and d.get('subtype') == 'stop_hook_summary':
                    status = 'done'
        # Workflow names come from script filenames, never from evaluating code.
        scripts = list((folder / 'workflows/scripts').glob('*' + workflow + '.js')) if workflow else []
        label = scripts[0].stem.removesuffix('-' + workflow) if scripts else workflow
        result.append({'id': workflow + ':' + ident, 'sessionID': ident, 'name': name, 'phase': state.get('phase') or meta.get('workflowPhase', ''),
                       'workflow': label, 'status': status, 'model': last_model(path), 'path': str(path),
                       'updated': path.stat().st_mtime})
    return sorted(result, key=lambda d: (d['status'] != 'running', -d['updated']))


def claude_capabilities(model):
    aliases = {'opus': 'claude-opus-5-5', 'sonnet': 'claude-sonnet-5-5',
               'haiku': 'claude-haiku-5-5', 'fable': 'claude-fable-5-1'}
    resolved = aliases.get(model, model)
    modern = re.match(r'^claude-(?:opus-(?:5-5|5|4-8|4-7)|sonnet-(?:5-5|5)|haiku-5-5|fable-(?:5-1|5))(?:-|$)', resolved)
    older = resolved in ('claude-opus-4-6', 'claude-sonnet-4-6')
    efforts = ['low', 'medium', 'high', 'xhigh', 'max'] if modern else ['low', 'medium', 'high', 'max'] if older else []
    fast = bool(re.match(r'^claude-opus-(?:5-5|5|4-8)(?:-|$)', resolved))
    return {'id': model, 'label': model.capitalize() if model in aliases else model,
            'efforts': efforts, 'speeds': ['standard', 'fast'] if fast else ['standard'],
            'defaultEffort': 'medium' if resolved.endswith('5-5') else 'high', 'showInPicker': True}


def models_for(source):
    if source['provider'] == 'Claude':
        aliases = [claude_capabilities(x) for x in ('opus', 'sonnet', 'haiku', 'fable')]
        versions = ('claude-opus-5-5', 'claude-opus-5', 'claude-opus-4-8', 'claude-opus-4-7', 'claude-opus-4-6',
                    'claude-sonnet-5-5', 'claude-sonnet-5', 'claude-sonnet-4-6', 'claude-haiku-5-5', 'claude-fable-5-1', 'claude-fable-5')
        return aliases + [{**claude_capabilities(x), 'showInPicker': False} for x in versions]
    data = read_json(Path(source['root']) / 'models_cache.json')
    result = []
    for m in data.get('models', []):
        if not m.get('slug') or m.get('visibility') != 'list': continue
        advertised = set(m.get('additional_speed_tiers', []))
        advertised.update('fast' if x.get('id') == 'priority' else x.get('id') for x in m.get('service_tiers', []))
        result.append({'id': m['slug'], 'label': m.get('display_name') or m['slug'],
                       'efforts': [x['effort'] for x in m.get('supported_reasoning_levels', []) if x.get('effort')],
                       'speeds': ['standard'] + [x for x in ('fast','ultrafast') if x in advertised],
                       'defaultEffort': m.get('default_reasoning_level', ''), 'showInPicker': True})
    return result


def capabilities_for(source, model):
    if source['provider'] == 'Claude': return claude_capabilities(model)
    return next((x for x in models_for(source) if x['id'] == model),
                {'id': model, 'label': model, 'efforts': [], 'speeds': ['standard'], 'defaultEffort': '', 'showInPicker': False})


def collect(request):
    sources = request if isinstance(request, list) else request["sources"]
    focus = None if isinstance(request, list) else request.get("selectedID")
    from bridge import session
    live, sessions, errors, catalogs = [], [], {}, {}
    metadata_by_home = {}
    for source in sources:
        catalogs[source['id']] = models_for(source)
        if source['provider'] != 'Claude':
            continue
        try:
            observations = live_claude(source)
        except (OSError, ValueError, subprocess.TimeoutExpired) as error:
            errors[source['id']] = str(error)
            continue
        home = Path(source['root']).parent
        metadata = metadata_by_home.setdefault(str(home), claude_metadata(home))
        for row in observations:
            ident = row['sessionId']
            path = find_transcript(source, ident)
            if path is None:
                continue
            native_state = row.get('status', 'unknown')
            state = {'busy': 'running', 'idle': 'idle', 'waiting': 'waiting', 'waiting_for_input': 'waiting'}.get(native_state, 'unknown')
            meta = metadata.get(ident, {})
            s = session(source, ident, meta.get('title') or row.get('name'), row.get('cwd', ''), path.stat().st_mtime,
                        str(path), model=meta.get('model') or last_model(path))
            children = children_for(s, state == 'running') if state == "running" or s["id"] == focus else []
            live.append({'id': s['id'], 'sourceID': source['id'], 'state': state, 'model': s['model'],
                         'children': children, 'owner': 'Claude Code', 'pid': row.get('pid', 0)})
            sessions.append(s)
    return {'activity': live, 'sessions': sessions, 'models': catalogs, 'errors': errors, 'observedAt': time.time()}


def assert_available(s):
    if s.get('isSubagent'):
        raise ValueError('Subagents are read-only in Camp. Continue their parent session.')
    if s.get('provider') == 'Claude':
        # Check immediately before spawning as well as in the UI. Never resume
        # into an independently owned live session and accidentally fork it.
        for row in live_claude({'root': s['root'], 'provider': 'Claude'}):
            if row['sessionId'] == s['sessionID']:
                raise ValueError('This session is open in Claude Code. Close it there before sending from Camp.')
