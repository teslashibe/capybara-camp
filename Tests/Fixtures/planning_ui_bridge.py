"""Synthetic native-UI fixture; copied only into a disposable development app."""
import json
import os
from pathlib import Path
import select
import sys
import time

root = Path(os.environ['CAPY_SUPPORT'])
root.mkdir(parents=True, exist_ok=True)


def emit(value):
    print(json.dumps(value), flush=True)


def sessions():
    return [dict(id=provider, sessionID=provider, provider=provider, sourceID=provider,
                 sourceLabel='Demo ' + provider, root=str(root / provider),
                 title='Synthetic ' + provider + ' planning source', cwd=str(root / 'demo-project'),
                 updated=time.time(), path='', accountID='', archived=False, model=model)
            for provider, model in [('Codex', 'gpt-6.1-sol'), ('Claude', 'claude-opus-5-5')]]


action = sys.argv[1]
if action == 'scan':
    emit(dict(sessions=sessions(), sources=[dict(id=s['sourceID'], provider=s['provider'], root=s['root'], label=s['sourceLabel'], count=1) for s in sessions()], errors=[]))
elif action == 'activity':
    sys.stdin.read()
    emit(dict(activity=[], sessions=[], errors={}, models={s['sourceID']: [dict(id=s['model'], label=s['model'], efforts=['low', 'medium', 'high'], speeds=['standard', 'fast'], defaultEffort='medium', showInPicker=True)] for s in sessions()}))
elif action == 'transcript':
    sys.stdin.read()
    emit(dict(messages=[dict(id='fixture-context', role='assistant', text='Synthetic context: preserve the same ticket and show both provider plans')], total=1))
elif action == 'children':
    sys.stdin.read()
    emit(dict(children=[]))
elif action == 'conversation':
    request = json.loads(sys.stdin.readline())
    assert request['mode'] == 'planning'
    provider = request['session']['provider']
    prompt = request['prompt']
    phase = 'Review' if 'Review the other proposal once' in prompt else 'Combined plan' if 'Produce one combined plan' in prompt else 'Independent plan'
    with (root / 'fixture-turns.jsonl').open('a') as output:
        output.write(json.dumps(dict(provider=provider, model=request['model'], phase=phase, context_count=len(request['context']))) + '\n')
    text = (phase + ' · ' + provider + '\n\n'
            '1. Keep one ticket with the complete planning history\n\n'
            '2. Let both providers draft independently, then review each other\n\n'
            '3. Preserve source links and any unresolved decisions\n\n'
            '4. Ask for approval before implementation\n\n'
            'Synthetic provider response for native UI testing')
    emit(dict(type='started'))
    for length in range(24, len(text) + 24, 24):
        if select.select([sys.stdin], [], [], .12)[0]:
            line = sys.stdin.readline()
            if not line or json.loads(line).get('type') == 'stop':
                emit(dict(type='cancelled', text='Stopped')); sys.exit(0)
        emit(dict(type='text', text=text[:length]))
    emit(dict(type='usage', inputTokens=123, outputTokens=45))
    emit(dict(type='done'))
else:
    emit({})
