"""Synthetic native-UI fixture; copied only into a disposable development app."""
import json
import os
from pathlib import Path
import select
import sys
import time
import uuid

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
    for source in sessions():
        Path(source['root']).mkdir(exist_ok=True)
        Path(source['cwd']).mkdir(exist_ok=True)
    emit(dict(sessions=sessions(), sources=[dict(id=s['sourceID'], provider=s['provider'], root=s['root'], label=s['sourceLabel'], count=1) for s in sessions()], errors=[]))
elif action == 'activity':
    sys.stdin.read()
    emit(dict(activity=[], sessions=[], errors={}, models={s['sourceID']: [dict(id=model, label=model, efforts=['low', 'medium', 'high'], speeds=['standard', 'fast'], defaultEffort='medium', showInPicker=True) for model in [s['model'], 'gpt-6-luna' if s['provider']=='Codex' else 'claude-sonnet-5']] for s in sessions()}))
elif action == 'execution-context':
    request = json.load(sys.stdin)['session']
    emit(dict(fingerprint='a'*64, cwd=request['cwd'], profile=request['root'], provider=request['provider'], permissionSummary='Synthetic CLI rules'))
elif action == 'transcript':
    request = json.load(sys.stdin)
    messages = [dict(id='fixture-context', role='assistant', text='Synthetic context: preserve the same ticket and show both provider plans')] if request['session']['sessionID'] in ('Codex', 'Claude') else []
    emit(dict(messages=messages, total=len(messages)))
elif action == 'children':
    sys.stdin.read()
    emit(dict(children=[]))
elif action == 'conversation':
    request = json.loads(sys.stdin.readline())
    if request['mode'] == 'implementation':
        assert request['executionApproved'] and request['executionContext'] == 'a' * 64
        if request['newSession']:
            emit(dict(type='session', sessionID=request['session']['sessionID']))
        emit(dict(type='started'))
        ident = str(uuid.uuid4())
        emit(dict(type='approval', id=ident, kind='file', title='Write the implementation test result',
                  detail=str(root/'implementation-proof.txt')+'\nSynthetic implementation with '+request['model'],
                  questions=[], canApprove=True, acceptLabel='Allow once'))
        while True:
            control = json.loads(sys.stdin.readline())
            if control.get('type') == 'stop':
                emit(dict(type='cancelled')); sys.exit(0)
            if control.get('type') == 'approval' and control.get('id') == ident:
                if control.get('allow'):
                    (root/'implementation-proof.txt').write_text('Implemented with '+request['model'])
                    emit(dict(type='approvalResolved', requestID=ident))
                    emit(dict(type='text', text='Implementation completed with '+request['model']+'\n\nThe synthetic change was approved and the original planning history is retained'))
                    emit(dict(type='done')); sys.exit(0)
                emit(dict(type='cancelled')); sys.exit(0)
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
