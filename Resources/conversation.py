"""One Camp-owned turn with a bidirectional native CLI connection."""
import collections
import fcntl
import hashlib
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import threading
import time
import uuid

CHAT_DISABLED_FEATURES = ("hooks", "shell_tool", "unified_exec", "multi_agent", "multi_agent_v2",
                          "view_image", "image_generation", "apps", "plugins", "remote_plugin",
                          "skill_search", "tool_suggest", "sleep_tool", "goals", "request_permissions_tool")

class PlanningOutputLimit(RuntimeError):
    pass


def emit_text(request, emit, text):
    if request.get("mode") == "planning" and len(text.encode()) > 8192:
        raise PlanningOutputLimit("The planning response exceeded 8 KB. This call used its allowance; restart with a shorter brief to try again.")
    emit({"type": "text", "text": text})


def emit_usage(emit, usage, codex=False):
    if not isinstance(usage, dict):
        return
    names = ("inputTokens", "outputTokens") if codex else ("input_tokens", "output_tokens")
    result = {"type": "usage"}
    for source, target in zip(names, ("inputTokens", "outputTokens")):
        value = usage.get(source)
        if type(value) is int and value >= 0:
            result[target] = value
    if not codex and "inputTokens" in result:
        for name in ("cache_read_input_tokens", "cache_creation_input_tokens"):
            value = usage.get(name, 0)
            if type(value) is int and value >= 0:
                result["inputTokens"] += value
    if len(result) > 1:
        emit(result)


class NativeConnection:
    def __init__(self, command, cwd, env, allow_requests=False):
        self.allow_requests = allow_requests
        self.events = queue.Queue(maxsize=16)
        self.backlog = collections.deque()
        self.errors = collections.deque(maxlen=12)
        self.stopping = threading.Event()
        self.cancelled = threading.Event()
        self.child = subprocess.Popen(command, cwd=cwd, env=env, text=True,
                                      stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                      stderr=subprocess.PIPE, start_new_session=True)
        self.serial = 0
        self.readers = [threading.Thread(target=self._read, daemon=True),
                        threading.Thread(target=self._stderr, daemon=True)]
        for reader in self.readers:
            reader.start()

    def _publish(self, event):
        while not self.stopping.is_set():
            try:
                self.events.put(event, timeout=.1)
                return
            except queue.Full:
                pass

    def _read(self):
        try:
            while not self.stopping.is_set():
                line = self.child.stdout.readline(1024 * 1024 + 1)
                if not line:
                    break
                if len(line) > 1024 * 1024:
                    self._publish({'camp_error': 'The native CLI exceeded the event size limit'})
                    break
                try:
                    event = json.loads(line)
                    if isinstance(event, dict):
                        self._publish(event)
                except ValueError:
                    pass
        finally:
            self._publish(None)

    def _stderr(self):
        while not self.stopping.is_set():
            line = self.child.stderr.readline(4096)
            if not line:
                break
            self.errors.append(line.rstrip())

    def write(self, event):
        self.child.stdin.write(json.dumps(event, ensure_ascii=False) + "\n")
        self.child.stdin.flush()

    def rpc(self, method, params, timeout=20):
        self.serial += 1
        ident = self.serial
        self.write({"id": ident, "method": method, "params": params})
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.cancelled.is_set():
                raise InterruptedError('Stopped while waiting for the native CLI')
            try:
                event = self.events.get(timeout=min(.1, max(.01, deadline - time.monotonic())))
            except queue.Empty:
                continue
            if event is None:
                raise RuntimeError("The native CLI closed before acknowledging " + method + (": " + " ".join(self.errors) if self.errors else ""))
            if event.get('camp_error'):
                raise RuntimeError(event['camp_error'])
            if event.get("id") == ident:
                if "error" in event:
                    raise RuntimeError(event["error"].get("message", "Native request rejected"))
                return event.get("result", {})
            if "method" in event and "id" in event:
                if self.allow_requests:
                    if len(self.backlog) >= 32:
                        raise RuntimeError('Too many pending provider requests')
                    self.backlog.append(event)
                else:
                    self.reject_tool(event)
            else:
                if len(self.backlog) >= 32:
                    raise RuntimeError('The native CLI sent too many events before acknowledging ' + method)
                self.backlog.append(event)
        raise RuntimeError("The native CLI did not acknowledge " + method)

    def reject_tool(self, event):
        self.write({"id": event["id"], "error": {"code": -32000, "message": "Tools are disabled in Camp's chat preview"}})

    def next(self):
        if self.backlog:
            return self.backlog.popleft()
        try:
            event = self.events.get(timeout=.1)
            if event and event.get('camp_error'):
                raise RuntimeError(event['camp_error'])
            return event
        except queue.Empty:
            return {}

    def close(self):
        self.stopping.set()
        try:
            self.child.stdin.close()
        except (OSError, BrokenPipeError):
            pass
        try:
            os.killpg(self.child.pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        try:
            self.child.wait(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(self.child.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
            self.child.wait(timeout=2)
        for reader in self.readers:
            reader.join(timeout=2)
        self.child.stdout.close()
        self.child.stderr.close()


def codex_command(s, context_server=None):
    # Disable configured MCP transports before the server parses their URLs.
    # Desktop-only placeholder URLs are not understood by the standalone CLI.
    import tomllib
    configs = [Path(s["root"]) / "config.toml"]
    cwd = Path(s["cwd"])
    configs += [path / ".codex/config.toml" for path in (*reversed(cwd.parents), cwd)]
    servers = {}
    for path in configs:
        if path.exists():
            with path.open("rb") as file:
                servers.update(tomllib.load(file).get("mcp_servers", {}))
    command = ["codex", "app-server", "--stdio"]
    for feature in CHAT_DISABLED_FEATURES:
        command += ["-c", "features." + feature + "=false"]
    disabled = []
    for name, config in servers.items():
        fields = ["enabled=false"]
        if "url" in config:
            fields.append('url="http://127.0.0.1:9"')
        disabled.append(json.dumps(name) + "={" + ",".join(fields) + "}")
    command += ["-c", "mcp_servers={" + ",".join(disabled) + "}"]
    if context_server:
        command += ["-c", "mcp_servers.camp_context=" + "{command=" + json.dumps(context_server["command"]) + ",args=" + json.dumps(context_server["args"]) + ",enabled=true,required=true}"]
    return command


def codex_config(config, context_server=None):
    """Disable every merged MCP server before resuming, including project entries."""
    overrides = {"mcp_servers": {name: {"enabled": False} for name in config.get("mcp_servers", {})}}
    overrides.update({"features." + feature: False for feature in CHAT_DISABLED_FEATURES})
    overrides["web_search"] = "disabled"
    if context_server:
        overrides["mcp_servers"]["camp_context"] = {**context_server, "enabled": True, "required": True}
    return overrides


def run(request, input_stream=None, emit=None, command=None):
    from bridge import SUPPORT, runtime_command, runtime_env
    from bridge import emit as default_emit
    from activity import assert_available
    emit = emit or default_emit
    input_stream = input_stream or sys.stdin
    request = dict(request)
    request.pop("_camp_mcp", None)
    request.pop("_camp_context_instructions", None)
    s, prompt = dict(request["session"]), request["prompt"]
    planning = request.get("mode") == "planning"
    implementing = request.get("mode") == "implementation"
    if request.get("mode", "chat") not in ("chat", "planning", "implementation"):
        raise ValueError("Unknown conversation mode")
    if implementing and request.get('executionApproved') is not True:
        raise ValueError('Approve the implementation before enabling project tools')
    if type(request.get('newSession', False)) is not bool:
        raise ValueError('Invalid session creation choice')
    if planning and s.get("provider") == "Claude":
        s["sessionID"] = str(uuid.uuid4())
    if s.get("provider") not in ("Codex", "Claude") or not isinstance(prompt, str) or not prompt.strip():
        raise ValueError("Choose a session and write a message")
    if not s.get("cwd") or not Path(s["cwd"]).is_dir():
        raise ValueError("Restore the original project folder before resuming this session")
    # Validate model and tuning with the existing adapter before starting a process.
    native_command = runtime_command(s, request.get("model", ""), request.get("effort", ""), request.get("speed", ""))
    SUPPORT.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock_key = "planning:" + s["provider"] if planning else s["id"]
    lock_path = SUPPORT / ("run-" + hashlib.sha256(lock_key.encode()).hexdigest() + ".lock")
    lock = os.fdopen(os.open(lock_path, os.O_RDWR | os.O_CREAT, 0o600), "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock.close()
        raise ValueError("This session is already running in Capybara Camp")
    connection = None
    grant = None
    controls = queue.Queue()
    previous = {}
    try:
        if not planning and not (implementing and request.get('newSession')):
            assert_available(s)
        if request.get("context"):
            from context_tool import ContextGrant
            grant = ContextGrant(request["context"], SUPPORT)
            request = {**request, "prompt": prompt + grant.attachment_links, "_camp_mcp": grant.server, "_camp_context_instructions": grant.instructions}
        if command is None:
            if implementing:
                from execution import execution_command
                command = execution_command(s, native_command, request.get('newSession', False))
                if grant and s['provider'] == 'Claude':
                    command += ['--mcp-config', json.dumps({'mcpServers': {'camp_context': grant.server}}),
                                '--append-system-prompt-file', str(grant.instructions_path), '--system-prompt-snapshot', 'off']
            elif s["provider"] == "Codex":
                command = codex_command(s, grant.server if grant else None)
            else:
                command = [*native_command, "--input-format", "stream-json", "--replay-user-messages"]
                if planning:
                    index = command.index("--resume"); del command[index:index + 2]
                    command += ["--session-id", s["sessionID"], "--no-session-persistence"]
                if grant:
                    command.remove("--safe-mode")
                    settings = {"disableAllHooks": True, "autoMemoryEnabled": False}
                    if "--settings" in command:
                        index = command.index("--settings")
                        settings.update(json.loads(command[index + 1])); del command[index:index + 2]
                    command += ["--restricted", "--setting-sources", "", "--disable-slash-commands", "--settings", json.dumps(settings), "--mcp-config", json.dumps({"mcpServers": {"camp_context": grant.server}}), "--allowedTools", "mcp__camp_context__read_session", "--append-system-prompt-file", str(grant.instructions_path), "--system-prompt-snapshot", "off"]
        env = runtime_env(s)
        if grant and s["provider"] == "Claude" and not implementing:
            env.pop("CLAUDE_CODE_SAFE_MODE", None)
        connection = NativeConnection(command, s["cwd"], env, allow_requests=implementing)

        def read_controls():
            for line in input_stream:
                try:
                    control = json.loads(line)
                    if isinstance(control, dict):
                        controls.put(control)
                        if control.get('type') == 'stop':
                            connection.cancelled.set()
                except ValueError:
                    pass
            controls.put({"type": "stop"})
            connection.cancelled.set()

        threading.Thread(target=read_controls, daemon=True).start()
        def stop_request(*_):
            controls.put({'type': 'stop'})
            connection.cancelled.set()

        previous = {sig: signal.signal(sig, stop_request)
                    for sig in (signal.SIGTERM, signal.SIGINT)}
        if s["provider"] == "Codex":
            codex_turn(connection, s, request, controls, emit)
        else:
            claude_turn(connection, s, request, controls, emit)
    except InterruptedError:
        emit({'type': 'cancelled', 'text': 'Stopped. Queued prompts are paused.'})
    except PlanningOutputLimit as error:
        emit({'type': 'error', 'message': str(error)})
    finally:
        if connection:
            connection.close()
        if grant:
            grant.close()
        for sig, handler in previous.items():
            signal.signal(sig, handler)
        lock.close()


def codex_turn(c, s, request, controls, emit):
    c.rpc("initialize", {"clientInfo": {"name": "capybara_camp", "title": "Capybara Camp", "version": "0.0.1"}, "capabilities": {"experimentalApi": True}})
    c.write({"method": "initialized", "params": {}})
    merged = c.rpc("config/read", {"includeLayers": False, "cwd": s["cwd"]}).get("config", {})
    params = {"threadId": s["sessionID"], "cwd": s["cwd"], "sandbox": "read-only", "approvalPolicy": "untrusted",
              "config": codex_config(merged, request.get("_camp_mcp")), "developerInstructions": "This is a chat-only preview. Answer directly. Only the Camp context read tool, when configured, is permitted; do not run project tools or delegate work." + request.get("_camp_context_instructions", "")}
    implementing = request.get('mode') == 'implementation'
    if implementing:
        params = {'threadId': s['sessionID'], 'cwd': s['cwd']}
        if request.get('_camp_mcp'):
            params['config'] = {'mcp_servers': {**merged.get('mcp_servers', {}), 'camp_context': {**request['_camp_mcp'], 'enabled': True, 'required': True}}}
            params['developerInstructions'] = (merged.get('developer_instructions') or '') + request.get('_camp_context_instructions', '')
    if request.get("model"):
        params["model"] = request["model"]
    if implementing and request.get('newSession'):
        params.pop('threadId')
        s['sessionID'] = c.rpc('thread/start', params)['thread']['id']
        emit({'type': 'session', 'sessionID': s['sessionID']})
    elif request.get("mode") == "planning":
        params.pop("threadId")
        params["ephemeral"] = True
        params["developerInstructions"] += " This is one bounded planning step. Do not execute the plan or change project files."
        s["sessionID"] = c.rpc("thread/start", params)["thread"]["id"]
    else:
        c.rpc("thread/resume", params)
    turn_params = {"threadId": s["sessionID"], "input": [{"type": "text", "text": request["prompt"]}], "approvalPolicy": "untrusted"}
    if implementing:
        turn_params.pop('approvalPolicy')
    for key in ("model", "effort"):
        if request.get(key):
            turn_params[key] = request[key]
    if request.get("speed"):
        turn_params["serviceTier"] = "default" if request["speed"] == "standard" else request["speed"]
    turn_id = c.rpc("turn/start", turn_params)["turn"]["id"]
    relay = None
    if implementing:
        from execution import ApprovalRelay
        relay = ApprovalRelay(c, emit, 'Codex', s['sessionID'], turn_id)
    emit({"type": "started", "turnID": turn_id})
    texts = collections.OrderedDict()
    while True:
        try:
            control = controls.get_nowait()
        except queue.Empty:
            control = None
        if control:
            if control.get('type') == 'approval' and relay:
                relay.resolve(control)
                continue
            if control.get("type") == "stop":
                try:
                    c.rpc("turn/interrupt", {"threadId": s["sessionID"], "turnId": turn_id}, timeout=3)
                except Exception:
                    pass
                emit({"type": "cancelled", "text": "Stopped. Queued prompts are paused."})
                return
            if control.get("type") == "steer":
                try:
                    if request.get("mode") == "planning":
                        raise ValueError("Restart the plan to change its frozen brief")
                    if not control.get("text", "").strip():
                        raise ValueError("Write a steering message")
                    c.rpc("turn/steer", {"threadId": s["sessionID"], "expectedTurnId": turn_id,
                                        "clientUserMessageId": control["id"],
                                        "input": [{"type": "text", "text": control["text"]}]})
                    emit({"type": "steerAccepted", "requestID": control["id"]})
                except Exception as error:
                    emit({"type": "steerRejected", "requestID": control.get("id"), "message": str(error)})
        event = c.next()
        if event is None:
            raise RuntimeError("Codex closed before completing the turn")
        if "method" in event and "id" in event:
            if relay:
                relay.request(event)
            else:
                c.reject_tool(event)
            continue
        method, params = event.get("method"), event.get("params", {})
        if params.get("threadId") not in (None, s["sessionID"]) or params.get("turnId") not in (None, turn_id):
            continue
        if relay and method == 'serverRequest/resolved':
            relay.cancelled(params.get('requestId'))
        if relay and method == 'item/started':
            item = params.get('item', {})
            relay.remember(item)
            if item.get('type') in ('commandExecution', 'fileChange', 'mcpToolCall'):
                emit({'type': 'activity', 'text': item.get('command') or item.get('tool') or 'Updating project files'})
        if method == "thread/tokenUsage/updated":
            emit_usage(emit, params.get("tokenUsage", {}).get("total"), codex=True)
        if method == "item/agentMessage/delta":
            item_id = params["itemId"]
            texts[item_id] = texts.get(item_id, "") + params.get("delta", "")
            emit_text(request, emit, "\n\n".join(texts.values()))
        if method == "item/completed" and params.get("item", {}).get("type") == "agentMessage":
            item = params["item"]
            texts[item["id"]] = item.get("text", "")
            emit_text(request, emit, "\n\n".join(texts.values()))
        if method == "turn/completed" and params.get("turn", {}).get("id") == turn_id:
            turn = params["turn"]
            if turn["status"] != "completed":
                emit({"type": "error", "message": turn.get("error", {}).get("message", "Codex did not complete the turn")})
            else:
                emit({"type": "done"})
            return


def claude_turn(c, s, request, controls, emit):
    from bridge import text_blocks
    def user(text, ident):
        c.write({"type": "user", "uuid": ident, "session_id": s["sessionID"],
                 "parent_tool_use_id": None, "message": {"role": "user", "content": text}})
    implementing = request.get('mode') == 'implementation'
    relay, initialization = None, None
    if implementing:
        from execution import ApprovalRelay
        relay = ApprovalRelay(c, emit, 'Claude', s['sessionID'])
        initialization = str(uuid.uuid4())
        c.write({'type': 'control_request', 'request_id': initialization, 'request': {'subtype': 'initialize', 'hooks': None}})
        initialize_deadline = time.monotonic() + 30
    else:
        user(request["prompt"], str(uuid.uuid4()))
    pending = None
    interrupt_ack = False
    interrupted_result = False
    sent = False
    text = ""
    ready = False
    reported_speed = ""
    while True:
        try:
            control = controls.get_nowait()
        except queue.Empty:
            control = None
        if control:
            if control.get('type') == 'approval' and relay:
                relay.resolve(control)
                continue
            if control.get("type") == "stop":
                emit({"type": "cancelled", "text": "Stopped. Queued prompts are paused."})
                return
            if control.get("type") == "steer":
                if request.get("mode") == "planning":
                    emit({"type": "steerRejected", "requestID": control.get("id"), "message": "Restart the plan to change its frozen brief"})
                elif pending or not ready or not control.get("text", "").strip():
                    emit({"type": "steerRejected", "requestID": control.get("id"), "message": "Wait until the current steering request finishes"})
                else:
                    pending = control
                    pending["deadline"] = time.monotonic() + 15
                    interrupt_ack = interrupted_result = sent = False
                    c.write({"type": "control_request", "request_id": control["id"], "request": {"subtype": "interrupt"}})
        event = c.next()
        if event is None:
            raise RuntimeError("Claude closed before completing the turn")
        if initialization and time.monotonic() > initialize_deadline:
            raise RuntimeError('Claude did not initialize its approval channel')
        kind = event.get("type")
        if relay and kind == 'control_request':
            relay.request(event)
            continue
        if relay and kind == 'control_cancel_request':
            relay.cancelled(event.get('request_id'))
            continue
        if initialization and kind == 'control_response' and event.get('response', {}).get('request_id') == initialization:
            response = event['response']
            if response.get('subtype') != 'success':
                raise RuntimeError('Claude rejected its approval channel')
            initialization = None
            if request.get('newSession'):
                emit({'type': 'session', 'sessionID': s['sessionID']})
            user(request['prompt'], str(uuid.uuid4()))
            continue
        if kind == "system" and event.get("subtype") == "init":
            if request.get("_camp_mcp") and not any(server.get("name") == "camp_context" and server.get("status") == "connected" for server in event.get("mcp_servers", [])):
                emit({"type": "error", "message": "The selected context tool did not connect. No context was supplied."})
                return
            if not ready:
                ready = True
                emit({"type": "started"})
            if request.get("speed") == "fast" and event.get("fast_mode_state") == "off":
                emit({"type": "notice", "message": "Claude did not enable Fast mode. Check usage credits and account availability."})
        if kind == "control_response" and pending and event.get("response", {}).get("request_id") == pending["id"]:
            response = event["response"]
            if response.get("subtype") == "error":
                emit({"type": "steerRejected", "requestID": pending["id"], "message": response.get("error", "Claude rejected the interruption")})
                pending = None
                if interrupted_result:
                    emit({"type": "done"})
                    return
            else:
                interrupt_ack = True
        if kind == "user" and pending and sent and event.get("uuid") == pending["id"]:
            emit({"type": "steerAccepted", "requestID": pending["id"]})
            pending = None
        if not event.get("parent_tool_use_id"):
            if kind == "stream_event":
                delta = event.get("event", {}).get("delta", {})
                if delta.get("type") == "text_delta":
                    text += delta.get("text", "")
                    emit_text(request, emit, text)
            elif kind == "assistant":
                actual_speed = event.get("message", {}).get("usage", {}).get("speed")
                if actual_speed in ("standard", "fast") and actual_speed != reported_speed:
                    reported_speed = actual_speed
                    if request.get("speed") and actual_speed != request["speed"]:
                        emit({"type": "notice", "message": f"Claude returned this reply at {actual_speed.capitalize()} speed ({request['speed'].capitalize()} was requested). Check usage credits and account availability."})
                complete = text_blocks(event.get("message", {}).get("content"))
                if complete:
                    text = complete
                    emit_text(request, emit, text)
            elif kind == "result":
                emit_usage(emit, event.get("usage"))
                if pending and not sent:
                    interrupted_result = True
                else:
                    if pending:
                        emit({"type": "steerRejected", "requestID": pending["id"], "message": "Claude did not acknowledge the steering message; check the transcript before retrying"})
                    if event.get("is_error"):
                        emit({"type": "error", "message": event.get("result") or "; ".join(event.get("errors", [])) or "Claude did not complete the turn"})
                    else:
                        if event.get("result"):
                            emit({"type": "text", "text": event["result"]})
                        emit({"type": "done"})
                    return
        if kind == "system" and event.get("subtype") == "notification" and isinstance(event.get("message"), str):
            emit({"type": "notice", "message": event["message"]})
        if pending and not sent and interrupt_ack and interrupted_result:
            emit({"type": "boundary"})
            text = ""
            user(pending["text"], pending["id"])
            sent = True
        if pending and time.monotonic() > pending["deadline"]:
            emit({"type": "steerRejected", "requestID": pending["id"], "message": "Steering acknowledgment timed out; check the transcript before retrying"})
            raise RuntimeError("Claude's steering response was uncertain. The queue is paused.")
