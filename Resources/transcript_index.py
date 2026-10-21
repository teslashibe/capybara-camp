"""Bounded, local-only transcript export for explicitly shared sessions."""
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import sqlite3


def fingerprint(session):
    paths = [Path(session.get("path") or "/nonexistent")]
    if session["provider"] == "Codex":
        root = Path(session["root"])
        paths += [root / name for name in ("thread_history_1.sqlite", "thread_history_1.sqlite-wal", "state_5.sqlite", "state_5.sqlite-wal")]
    values = []
    for path in paths:
        try:
            stat = path.stat()
            values.append((str(path), stat.st_ino, stat.st_size, stat.st_mtime_ns))
        except FileNotFoundError:
            values.append((str(path), None))
    return hashlib.sha256(json.dumps(values).encode()).hexdigest()


def export_page(session, support, after=-1, limit=200):
    from bridge import codex_history, claude_records
    if session.get("provider") not in ("Codex", "Claude") or not session.get("sessionID"):
        raise ValueError("Choose a local Codex or Claude session")
    if not isinstance(after, int) or after < -1 or not isinstance(limit, int) or not 1 <= limit <= 200:
        raise ValueError("Invalid transcript page")
    key = hashlib.sha256((session["provider"] + "\n" + session["root"] + "\n" + session["sessionID"] + "\n" + session.get("path", "")).encode()).hexdigest()
    directory = Path(support) / "transcript-index"
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    database = directory / (key + ".sqlite")
    with closing(sqlite3.connect(database, timeout=5)) as db:
        os.chmod(database, 0o600)
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA journal_mode=WAL")
        db.executescript("""
            CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS messages(id TEXT PRIMARY KEY,ordinal INTEGER NOT NULL UNIQUE,body TEXT NOT NULL,seen INTEGER NOT NULL,removed INTEGER NOT NULL);
        """)
        stamp = fingerprint(session)
        cached = db.execute("SELECT value FROM metadata WHERE key='fingerprint'").fetchone()
        if cached is None or cached[0] != stamp:
            db.execute("BEGIN IMMEDIATE")
            try:
                db.execute("UPDATE messages SET seen=0")
                ordinal = db.execute("SELECT coalesce(max(ordinal)+1,0) FROM messages").fetchone()[0]
                records = codex_history(session) if session["provider"] == "Codex" else claude_records(session, strict=True)
                for item in records:
                    ident = item["id"]
                    if len(ident.encode()) > 256:
                        ident = "m:" + hashlib.sha256(ident.encode()).hexdigest()
                    existing = db.execute("SELECT ordinal FROM messages WHERE id=?", (ident,)).fetchone()
                    position = existing[0] if existing else ordinal
                    if existing is None:
                        ordinal += 1
                    body = {"id": ident, "ordinal": position, "role": item["role"], "text": item["text"], "removed": False}
                    if item.get("model"):
                        body["model"] = item["model"]
                    db.execute("INSERT INTO messages VALUES(?,?,?,1,0) ON CONFLICT(id) DO UPDATE SET body=excluded.body,seen=1,removed=0",
                               (ident, position, json.dumps(body, ensure_ascii=False, separators=(",", ":"))))
                db.execute("UPDATE messages SET removed=1 WHERE seen=0")
                # A concurrent append causes a fresh scan on the next page/cycle.
                stable = stamp if fingerprint(session) == stamp else ""
                db.execute("INSERT INTO metadata VALUES('fingerprint',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (stable,))
                db.commit()
            except BaseException:
                db.rollback()
                raise
        messages, size, has_more = [], 0, False
        for row in db.execute("SELECT body,removed FROM messages WHERE ordinal>? ORDER BY ordinal LIMIT ?", (after, limit + 1)):
            body = json.loads(row["body"])
            if row["removed"]:
                body.update(text="", removed=True)
            encoded_size = len(json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode())
            if encoded_size > 1024 * 1024:
                raise ValueError("This transcript contains a message larger than the 1 MiB sharing limit")
            if len(messages) >= limit or size + encoded_size > 1024 * 1024:
                has_more = True
                break
            messages.append(body)
            size += encoded_size
        return {"messages": messages, "after": messages[-1]["ordinal"] if messages else after, "hasMore": has_more, "fingerprint": stamp}
