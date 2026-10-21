#!/usr/bin/env python3
"""Local CLI adapter. Provider stores are opened read-only; credentials stay with CLIs."""
import argparse
from contextlib import closing
import datetime as dt
import hashlib
import json
import os
import re
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import tempfile
import threading

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent))

SUPPORT = Path(os.environ.get("CAPY_SUPPORT", str(Path.home() / "Library/Application Support/Capybara Camp")))

def emit(value):
    print(json.dumps(value, ensure_ascii=False), flush=True)

def read_json(path, default):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return default

def save_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, name = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(value, output, ensure_ascii=False)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)

def text_blocks(value):
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        return "\n".join(x.get("text", "") for x in value if isinstance(x, dict) and x.get("type") in ("text", "input_text", "output_text") and isinstance(x.get("text", ""), str))
    return ""

def title_for(value):
    # Some CLI titles contain the entire original prompt. Only normalize the
    # displayed 110 characters, without splitting a multi-megabyte prompt.
    words, length = [], 0
    for match in re.finditer(r"\S{1,110}", value or ""):
        remaining = 110 - length - bool(words)
        if remaining == 0:
            words.append("")
            break
        if remaining < 0:
            break
        word = match.group()[:remaining]
        words.append(word)
        length += len(word) + (len(words) > 1)
        if length >= 110:
            break
    value = " ".join(words)
    if value.startswith(("<", "# AGENTS.md", "You are ")):
        return "Imported session"
    return value[:110] or "Untitled session"

def iso_seconds(value):
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except (ValueError, TypeError, AttributeError):
        return 0

def db_open(path):
    connection = sqlite3.connect(Path(path).as_uri() + "?mode=ro", uri=True, timeout=3)
    connection.execute("PRAGMA query_only=ON")
    connection.row_factory = sqlite3.Row
    return connection

def source_id(provider, root):
    return provider + ":" + str(root.resolve())

def session_lock_key(value, native_id=None):
    return source_id(value['provider'], Path(value['root'])) + ':' + (native_id or value['sessionID'])

def detect_provider(root):
    if not root.is_dir():
        return None
    if (root / "state_5.sqlite").is_file():
        return "Codex"
    if (root / "projects").is_dir():
        return "Claude"
    if (root / "sessions").is_dir() or (root / "archived_sessions").is_dir():
        return "Codex"
    return None

def add_source(value):
    root = Path(value).expanduser().resolve()
    if not detect_provider(root):
        raise ValueError("Choose a Codex home with sessions or a Claude folder with projects")
    paths = read_json(SUPPORT / "sources.json", [])
    if str(root) not in paths:
        paths.append(str(root))
    save_json(SUPPORT / "sources.json", paths)
    return {"ok": True}

def sources():
    roots = []
    homes = [Path.home()]
    try:
        homes += [p for p in Path("/Users").iterdir() if p.is_dir() and p != Path.home() and not p.name.startswith(".")]
    except OSError:
        pass
    for home in homes:
        try:
            roots.extend(p for p in home.iterdir() if p.is_dir() and p.name.startswith((".codex", ".claude")))
        except OSError:
            continue
    roots += [Path(p).expanduser() for p in read_json(SUPPORT / "sources.json", [])]
    for name in ("CODEX_HOME", "CLAUDE_CONFIG_DIR"):
        if os.environ.get(name):
            roots.append(Path(os.environ[name]))
    result = []
    seen = set()
    for root in roots:
        try:
            root = root.resolve()
            provider = detect_provider(root)
            if not provider or str(root) in seen:
                continue
            seen.add(str(root))
            label = root.name if root.parent == Path.home() else root.parent.name + "/" + root.name
            result.append({"id": source_id(provider, root), "provider": provider, "root": str(root), "label": label})
        except OSError:
            continue
    return result

def session(source, ident, title, cwd, updated, path="", **extra):
    return {"id": source["id"] + ":" + ident, "sessionID": ident, "provider": source["provider"],
            "sourceID": source["id"], "sourceLabel": source["label"], "root": source["root"],
            "title": title_for(title), "cwd": cwd or "", "updated": updated or 0, "path": path or "",
            "accountID": "", "archived": False, "model": "", **extra}

def codex_sessions(source):
    root = Path(source["root"])
    if (root / "state_5.sqlite").exists():
        with closing(db_open(root / "state_5.sqlite")) as c:
            cols = {r[1] for r in c.execute("PRAGMA table_info(threads)")}
            if not {"id", "title", "cwd", "updated_at", "rollout_path", "source"}.issubset(cols):
                raise ValueError("This Codex database has an unsupported threads schema")
            selected = [x for x in ["id", "title", "cwd", "updated_at", "rollout_path", "source", "creator_account_id", "archived", "model"] if x in cols]
            rows = c.execute("SELECT " + ",".join(selected) + " FROM threads ORDER BY updated_at DESC")
            return [session(source, r["id"], r["title"], r["cwd"], r["updated_at"], r["rollout_path"],
                            accountID=r["creator_account_id"] or "" if "creator_account_id" in cols else "",
                            archived=bool(r["archived"]) if "archived" in cols else False,
                            model=r["model"] or "" if "model" in cols else "")
                    for r in rows if not (r["source"] or "").startswith('{"subagent"')]
    out = []
    for folder in [root / "sessions", root / "archived_sessions"]:
        for path in folder.rglob("*.jsonl"):
            try:
                with path.open() as f:
                    d = json.loads(f.readline()).get("payload", {})
                ident = d.get("id") or d.get("session_id")
                if ident:
                    out.append(session(source, ident, d.get("title", "Codex session"), d.get("cwd", ""), path.stat().st_mtime, str(path), archived=folder.name == "archived_sessions"))
            except (OSError, ValueError):
                continue
    return out

def claude_sessions(source, cache, warnings=None):
    root = Path(source["root"]) / "projects"
    out = []
    for project in root.iterdir():
        if not project.is_dir():
            continue
        entries = {x.get("sessionId"): x for x in read_json(project / "sessions-index.json", {}).get("entries", [])}
        for path in project.glob("*.jsonl"):
            if path.name.startswith("agent-"):
                continue
            try:
                st = path.stat()
            except OSError as error:
                if warnings is not None:
                    warnings.append(f"Could not read {path.name}: {error.strerror}")
                continue
            key = str(path)
            signature = [st.st_mtime_ns, st.st_size]
            if key in cache and cache[key].get("signature") == signature:
                out.append(cache[key]["session"])
                continue
            entry = entries.get(path.stem, {})
            cwd, title, ident = entry.get("projectPath", ""), entry.get("summary") or entry.get("firstPrompt", ""), path.stem
            sidechain = False
            try:
                f = path.open(errors="replace")
            except OSError as error:
                if warnings is not None:
                    warnings.append(f"Could not read {path.name}: {error.strerror}")
                continue
            with f:
                # Find initial metadata without reading multi-megabyte transcripts into memory.
                for _ in range(100):
                    line = f.readline()
                    if not line:
                        break
                    try:
                        d = json.loads(line)
                    except ValueError:
                        continue
                    if not isinstance(d, dict):
                        continue
                    cwd = d.get("cwd") or cwd
                    ident = d.get("sessionId") or ident
                    sidechain = sidechain or d.get("isSidechain", False)
                    if d.get("type") == "user":
                        value = text_blocks(d.get("message", {}).get("content"))
                        title = title or value
                        if cwd:
                            break
            if sidechain:
                continue
            s = session(source, ident, title, cwd, st.st_mtime, str(path), accountID="", archived=False)
            cache[key] = {"signature": signature, "session": s}
            out.append(s)
    return out

def scan():
    cache = read_json(SUPPORT / "import-cache.json", {})
    result, errors, failed_roots, homes = [], [], [], sources()
    discovered = {s["root"] for s in homes}
    for value in read_json(SUPPORT / "sources.json", []):
        if str(Path(value).expanduser().resolve()) not in discovered:
            errors.append("Profile folder is unavailable or has no session store: " + value)
            failed_roots.append(str(Path(value).expanduser().resolve()))
    for source in homes:
        try:
            entries = codex_sessions(source) if source["provider"] == "Codex" else claude_sessions(source, cache, errors)
            source["count"] = len(entries)
            result.extend(entries)
        except (OSError, sqlite3.Error, ValueError) as e:
            source["count"] = 0
            failed_roots.append(source["root"])
            errors.append(source["label"] + ": " + str(e))
    # Same native ID can occur in independent profiles; root is part of the identity.
    newest = {}
    for s in result:
        if s["id"] not in newest or s["updated"] > newest[s["id"]]["updated"]:
            newest[s["id"]] = s
    result = list(newest.values())
    from activity import claude_metadata
    metadata = claude_metadata()
    for s in result:
        if s["provider"] == "Claude":
            item = metadata.get(s["sessionID"], {})
            s["title"] = item.get("title") or s["title"]
            s["model"] = item.get("model") or s.get("model", "")
    result.sort(key=lambda s: s["updated"], reverse=True)
    for source in homes:
        source["count"] = sum(s["sourceID"] == source["id"] for s in result)
    save_json(SUPPORT / "import-cache.json", cache)
    return {"sessions": result, "sources": homes, "errors": errors, "failedRoots": failed_roots}

def codex_history(s):
    root = Path(s["root"])
    history = root / "thread_history_1.sqlite"
    if history.exists():
        found = False
        with closing(db_open(history)) as c:
            for row in c.execute("SELECT item_id,item_json FROM thread_items WHERE thread_id=? AND item_type IN ('userMessage','agentMessage') ORDER BY rollout_ordinal", (s["sessionID"],)):
                d = json.loads(row["item_json"])
                text = d.get("text", "") if d.get("type") == "agentMessage" else text_blocks(d.get("content"))
                if text:
                    found = True
                    yield {"id": row["item_id"], "role": "assistant" if d.get("type") == "agentMessage" else "user", "text": text}
        if found:
            return
    if not s.get("path"):
        return
    path = Path(s["path"])
    with path.open() as f:
        for n, line in enumerate(f):
            try:
                d = json.loads(line)
            except ValueError:
                continue
            p = d.get("payload", {})
            if d.get("type") == "response_item" and p.get("type") == "message" and p.get("role") in ("user", "assistant"):
                text = text_blocks(p.get("content"))
                if text:
                    yield {"id": p.get("id") or str(n), "role": p["role"], "text": text}

def claude_records(s, strict=False):
    with Path(s["path"]).open(errors="replace") as f:
        for n, line in enumerate(f):
            try:
                d = json.loads(line)
            except ValueError:
                if strict and line.strip():
                    raise ValueError("The transcript is incomplete; sharing will retry after it is written")
                continue
            if not isinstance(d, dict):
                if strict:
                    raise ValueError("The transcript contains an invalid record")
                continue
            if d.get("type") in ("user", "assistant") and (not d.get("isSidechain") or s.get("isSubagent")):
                message = d.get("message")
                if not isinstance(message, dict):
                    if strict:
                        raise ValueError("The transcript contains an invalid message")
                    continue
                text = text_blocks(message.get("content"))
                if text:
                    ident = d.get("uuid") or str(n)
                    value = {"id": ident, "role": d["type"], "text": text}
                    if isinstance(message.get("model"), str):
                        value["model"] = message["model"]
                    yield value

def claude_history(s):
    records = {value["id"]: value for value in claude_records(s)}
    yield from records.values()

def transcript(s, limit=120):
    if not isinstance(limit, int) or limit < 1:
        raise ValueError("History page size must be a positive integer")
    records = list(codex_history(s) if s["provider"] == "Codex" else claude_history(s))
    return {"messages": records[-limit:], "total": len(records)}

def runtime_command(s, model="", effort="", speed=""):
    if model and (not isinstance(model, str) or len(model) > 160 or any(c.isspace() for c in model) or model.startswith("-")):
        raise ValueError("Choose a valid model ID")
    from activity import capabilities_for
    capabilities = capabilities_for(s, model or s.get("model", ""))
    if effort and effort not in capabilities["efforts"]:
        raise ValueError("This model does not advertise the selected effort level")
    if speed and speed not in capabilities["speeds"]:
        raise ValueError("This model does not support the selected speed")
    selection = ["--model", model] if model else []
    tuning = []
    if s["provider"] == "Codex":
        if effort: tuning += ["-c", "model_reasoning_effort=" + json.dumps(effort)]
        if speed:
            tier = "default" if speed == "standard" else speed
            tuning += ["-c", "service_tier=" + json.dumps(tier)]
            if speed == "fast": tuning += ["-c", "features.fast_mode=true"]
            if speed == "ultrafast": tuning += ["-c", "features.ultrafast_mode=true"]
    else:
        if effort: tuning += ["--effort", effort]
        if speed: tuning += ["--settings", json.dumps({"fastMode": speed == "fast"})]
    if s["provider"] == "Codex":
        # Chat-only proof. Read-only sandbox; user config is not loaded, so no
        # external MCP servers or user-defined hooks are started by this adapter.
        return ["codex", "exec", *selection, *tuning, "--ignore-user-config", "-s", "read-only", "--json", "--skip-git-repo-check", "resume", s["sessionID"], "-"]
    return ["claude", *selection, *tuning, "--resume", s["sessionID"], "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--tools", "", "--strict-mcp-config", "--safe-mode"]

def runtime_env(s):
    env = os.environ.copy()
    # Prevent inherited credentials overriding the profile the user selected.
    for key in ("OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDECODE", "CODEX_THREAD_ID", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "CLAUDE_CODE_EFFORT_LEVEL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL"):
        env.pop(key, None)
    env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:" + env.get("PATH", "")
    if s["provider"] == "Codex":
        env["CODEX_HOME"] = s["root"]
    elif Path(s["root"]).resolve() == Path.home() / ".claude":
        # Claude's default Keychain service differs from the service selected
        # by explicitly setting the same directory. Preserve default auth.
        env.pop("CLAUDE_CONFIG_DIR", None)
    else:
        env["CLAUDE_CONFIG_DIR"] = s["root"]
    return env

def chat(s, prompt, model="", effort="", speed=""):
    if s.get("provider") not in ("Codex", "Claude"):
        raise ValueError("Unknown session provider")
    if not isinstance(prompt, str) or not prompt.strip():
        raise ValueError("Write a message before sending")
    cwd = Path(s.get("cwd") or "")
    if not s.get("cwd") or not cwd.is_dir():
        raise ValueError("The original project folder is missing. Restore it before resuming this session: " + str(cwd))
    # Serialize sends across multiple app windows/processes for this session.
    import fcntl
    SUPPORT.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock_path = SUPPORT / ("run-" + hashlib.sha256(session_lock_key(s).encode()).hexdigest() + ".lock")
    lock = os.fdopen(os.open(lock_path, os.O_RDWR | os.O_CREAT, 0o600), "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock.close()
        raise ValueError("This session is already running in Capybara Camp.")
    from activity import assert_available
    try:
        assert_available(s)
        command = runtime_command(s, model, effort, speed)
    except Exception:
        lock.close()
        raise
    try:
        child = subprocess.Popen(command, cwd=cwd, env=runtime_env(s), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
    except OSError as error:
        lock.close()
        raise ValueError(f"Could not start {s['provider']}. Check that its CLI is installed: {error.strerror}") from error
    cancelled = False
    stop_timers = []
    def stop(sig, frame):
        nonlocal cancelled
        cancelled = True
        try:
            os.killpg(child.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        def force_stop():
            # The CLI may have exited while a descendant still owns stdout.
            # Kill the remaining process group, not only a living direct child.
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        timer = threading.Timer(4, force_stop)
        timer.daemon = True
        stop_timers.append(timer)
        timer.start()
    previous_signals = {sig: signal.signal(sig, stop) for sig in (signal.SIGTERM, signal.SIGINT)}
    errors = []
    def stderr_reader():
        for line in child.stderr:
            errors.append(line.rstrip())
            if len(errors) > 30:
                errors.pop(0)
    thread = threading.Thread(target=stderr_reader, daemon=True)
    thread.start()
    emit({"type": "started"})
    try:
        child.stdin.write(prompt)
        child.stdin.close()
    except BrokenPipeError:
        pass  # Read the provider's actual error even if it rejects before stdin.
    text = ""
    got_result = False
    failure = ""
    reported_speed = ""
    for line in child.stdout:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if not isinstance(d, dict):
            continue
        kind = d.get("type")
        if s["provider"] == "Codex":
            item = d.get("item", {})
            if kind in ("item.completed", "item.updated") and item.get("type") == "agent_message":
                text = item.get("text", "")
                emit({"type": "text", "text": text})
            if kind in ("error", "turn.failed"):
                e = d.get("error", {})
                failure = d.get("message") or (e.get("message") if isinstance(e, dict) else str(e)) or "Codex could not finish the turn"
            if kind == "turn.completed":
                got_result = True
        else:
            if kind == "system" and d.get("subtype") == "init" and speed == "fast" and d.get("fast_mode_state") == "off":
                emit({"type": "notice", "message": "Claude did not enable Fast mode. Check usage credits and account availability."})
            if kind == "system" and d.get("subtype") == "notification":
                message = d.get("message") or text_blocks(d.get("content"))
                if isinstance(message, str) and message:
                    emit({"type": "notice", "message": message})
            if d.get("parent_tool_use_id"):
                continue
            if kind == "stream_event":
                event = d.get("event", {})
                delta = event.get("delta", {})
                if delta.get("type") == "text_delta":
                    text += delta.get("text", "")
                    emit({"type": "text", "text": text})
            elif kind == "assistant":
                actual_speed = d.get("message", {}).get("usage", {}).get("speed")
                if actual_speed in ("standard", "fast") and actual_speed != reported_speed:
                    reported_speed = actual_speed
                    if speed and actual_speed != speed:
                        emit({"type": "notice", "message": f"Claude returned this reply at {actual_speed.capitalize()} speed ({speed.capitalize()} was requested). Check usage credits and account availability."})
                complete = text_blocks(d.get("message", {}).get("content"))
                if complete:
                    text = complete
                    emit({"type": "text", "text": text})
            elif kind == "result":
                got_result = not d.get("is_error", False)
                if d.get("is_error"):
                    failure = d.get("result") or "; ".join(d.get("errors", [])) or "Claude could not finish the turn"
                elif d.get("result"):
                    text = d["result"]
                    emit({"type": "text", "text": text})
    code = child.wait()
    for timer in stop_timers:
        timer.cancel()
    thread.join(timeout=1)
    for sig, handler in previous_signals.items():
        signal.signal(sig, handler)
    lock.close()
    if cancelled:
        emit({"type": "cancelled", "text": "Stopped. Any partial turn remains in the original session."})
    elif code != 0 or failure or not got_result:
        # No retry and no account switching after uncertain outcomes.
        message = failure or "\n".join(errors[-5:]) or f"The CLI exited with code {code} before completing the turn"
        emit({"type": "error", "message": message})
    else:
        emit({"type": "done"})

def main():
    p = argparse.ArgumentParser()
    p.add_argument("action", choices=["scan", "transcript", "export-page", "chat", "conversation", "execution-context", "add-source", "activity", "children"])
    args = p.parse_args()
    if args.action == "scan":
        emit(scan())
    elif args.action == "activity":
        from activity import collect
        emit(collect(json.load(sys.stdin)))
    elif args.action == "children":
        from activity import children_for
        value = json.load(sys.stdin)
        emit({"children": children_for(value["session"], value.get("live", False))})
    elif args.action == "add-source":
        emit(add_source(sys.stdin.read().strip()))
    elif args.action == "conversation":
        from conversation import run
        run(json.loads(sys.stdin.readline()))
    elif args.action == "execution-context":
        from execution import execution_context
        emit(execution_context(json.load(sys.stdin)['session']))
    else:
        request = json.load(sys.stdin)
        if args.action == "export-page":
            from transcript_index import export_page
            emit(export_page(request["session"], SUPPORT, request.get("after", -1), request.get("limit", 200)))
        elif args.action == "transcript":
            emit(transcript(request["session"], request.get("limit", 120)))
        else:
            chat(request["session"], request["prompt"], request.get("model", ""), request.get("effort", ""), request.get("speed", ""))

if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        emit({"type": "error", "message": str(e)})
        sys.exit(1)
