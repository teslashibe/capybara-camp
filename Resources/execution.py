"""Bounded, turn-scoped approval relay for the installed provider CLIs."""
import json
import uuid
import copy


class ApprovalRelay:
    def __init__(self, connection, emit, provider, session_id, turn_id=None):
        self.connection = connection
        self.emit = emit
        self.provider = provider
        self.session_id = session_id
        self.turn_id = turn_id
        self.pending = {}
        self.items = {}

    def remember(self, item):
        if isinstance(item, dict) and item.get('type') == 'fileChange':
            if len(self.items) >= 16:
                self.items.pop(next(iter(self.items)))
            self.items[item.get('id')] = item if len(json.dumps(item).encode()) <= 65536 else None

    def cancelled(self, request_id):
        for key, value in list(self.pending.items()):
            if value['request_id'] == request_id:
                self.pending.pop(key)
                self.emit({'type': 'approvalResolved', 'requestID': key})

    def request(self, event):
        if self.provider == 'Codex':
            method, params, request_id = event.get('method'), event.get('params', {}), event.get('id')
            if params.get('threadId') != self.session_id or (self.turn_id is not None and params.get('turnId') != self.turn_id):
                self.connection.reject_tool(event)
                return
            kinds = {'item/commandExecution/requestApproval': 'command',
                     'item/fileChange/requestApproval': 'file',
                     'item/permissions/requestApproval': 'permissions',
                     'item/tool/requestUserInput': 'question'}
            kind = kinds.get(method)
            if kind is None:
                self.connection.reject_tool(event)
                self.emit({'type': 'notice', 'message': 'This provider request is not supported in Camp yet'})
                return
            detail = dict(params)
            if kind == 'file':
                detail['changes'] = self.items.get(params.get('itemId'))
            questions = params.get('questions', []) if kind == 'question' else []
            title = {'command': 'Approve command', 'file': 'Approve file changes',
                     'permissions': 'Approve access for this turn', 'question': 'Input requested'}[kind]
            can_approve = kind != 'file' or detail.get('changes') is not None
            if kind == 'command' and params.get('availableDecisions') is not None:
                can_approve = 'accept' in params['availableDecisions']
            request = {'request_id': request_id, 'kind': kind, 'params': params}
        else:
            params = event.get('request', {})
            request_id = event.get('request_id')
            if params.get('subtype') != 'can_use_tool':
                self.connection.write({'type': 'control_response', 'response': {'subtype': 'error', 'request_id': request_id, 'error': 'Unsupported Camp control request'}})
                return
            tool = params.get('tool_name', '')
            kind = 'question' if tool == 'AskUserQuestion' else 'tool'
            detail = {'tool': tool, 'input': params.get('input', {}), 'reason': params.get('decision_reason')}
            questions = params.get('input', {}).get('questions', []) if kind == 'question' else []
            title = 'Input requested' if kind == 'question' else 'Approve ' + tool
            can_approve = True
            request = {'request_id': request_id, 'kind': kind, 'params': params}
        if any(value['request_id'] == request_id for value in self.pending.values()):
            return
        detail_text = json.dumps(detail, indent=2, ensure_ascii=False)
        if len(self.pending) >= 16 or len(detail_text.encode()) > 65536 or len(questions) > 8:
            self.deny(request)
            self.emit({'type': 'notice', 'message': 'A provider approval exceeded the size or pending-request limit and was declined'})
            return
        normalized = []
        for index, question in enumerate(questions):
            normalized.append({'id': question.get('id', str(index)), 'header': question.get('header', ''),
                               'prompt': question.get('question', ''),
                               'options': [option['label'] for option in question.get('options') or []],
                               'multiple': bool(question.get('multiSelect', False)),
                               'secret': bool(question.get('isSecret', False)),
                               'allowText': bool(question.get('isOther', self.provider == 'Claude'))})
        request = copy.deepcopy(request)
        request['questions'] = normalized
        request['can_approve'] = can_approve
        key = str(uuid.uuid4())
        self.pending[key] = request
        self.emit({'type': 'approval', 'id': key, 'kind': kind, 'title': title,
                   'detail': detail_text, 'questions': normalized, 'canApprove': can_approve,
                   'acceptLabel': 'Allow for this turn' if kind == 'permissions' else 'Submit answers' if kind == 'question' else 'Allow once'})

    def deny(self, value):
        kind, params = value['kind'], value['params']
        if self.provider == 'Codex':
            result = {'answers': {}} if kind == 'question' else {'permissions': {}, 'scope': 'turn'} if kind == 'permissions' else {'decision': 'decline'}
            self.connection.write({'id': value['request_id'], 'result': result})
        else:
            self.connection.write({'type': 'control_response', 'response': {'subtype': 'success', 'request_id': value['request_id'],
                'response': {'behavior': 'deny', 'message': 'Declined in Capybara Camp'}}})

    def resolve(self, control):
        key = control.get('id')
        value = self.pending.get(key)
        if not value:
            self.emit({'type': 'approvalRejected', 'requestID': key, 'message': 'This approval is no longer pending'})
            return
        allow = control.get('allow') is True
        answers = control.get('answers', {})
        if allow and not value['can_approve']:
            self.emit({'type': 'approvalRejected', 'requestID': key, 'message': 'The full action cannot be approved from this preview'})
            return
        if allow and value['kind'] == 'question':
            if not isinstance(answers, dict) or set(answers) != {q['id'] for q in value['questions']}:
                self.emit({'type': 'approvalRejected', 'requestID': key, 'message': 'Answer every question before submitting'})
                return
            for q in value['questions']:
                selected = answers[q['id']]
                if (not isinstance(selected, list) or not selected or len(selected) > 8 or
                    (not q['multiple'] and len(selected) != 1) or
                    any(not isinstance(x, str) or not x.strip() or len(x.encode()) > 4096 or
                        (q['options'] and not q['allowText'] and x not in q['options']) for x in selected)):
                    self.emit({'type': 'approvalRejected', 'requestID': key, 'message': 'Choose a valid answer for each question'})
                    return
        if not allow:
            self.deny(value)
        elif self.provider == 'Codex':
            kind, params = value['kind'], value['params']
            result = {'answers': {qid: {'answers': selected} for qid, selected in answers.items()}} if kind == 'question' else (
                {'permissions': params['permissions'], 'scope': 'turn'} if kind == 'permissions' else {'decision': 'accept'})
            self.connection.write({'id': value['request_id'], 'result': result})
        else:
            original = dict(value['params'].get('input', {}))
            if value['kind'] == 'question':
                original['answers'] = {q['prompt']: ', '.join(answers[q['id']]) for q in value['questions']}
            self.connection.write({'type': 'control_response', 'response': {'subtype': 'success', 'request_id': value['request_id'],
                'response': {'behavior': 'allow', 'updatedInput': original}}})
        self.pending.pop(key)
        self.emit({'type': 'approvalResolved', 'requestID': key})


def execution_command(session, validated_command, new_session=False):
    if session['provider'] == 'Codex':
        return ['codex', 'app-server', '--stdio']
    command = list(validated_command)
    for flag in ['--tools', '--resume'] if new_session else ['--tools']:
        index = command.index(flag)
        del command[index:index + 2]
    command.remove('--safe-mode')
    command.remove('--strict-mcp-config')
    if new_session:
        uuid.UUID(session['sessionID'])
        command += ['--session-id', session['sessionID']]
    return command + ['--permission-prompt-tool', 'stdio', '--input-format', 'stream-json', '--replay-user-messages']
