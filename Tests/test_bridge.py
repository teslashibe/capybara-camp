import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("bridge", Path(__file__).parents[1] / "Resources/bridge.py")
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)

class Imports(unittest.TestCase):
    def test_source_identity_does_not_merge_accounts(self):
        a = {"id": "Codex:/a", "provider": "Codex", "label": "A", "root": "/a"}
        c = {**a, "id": "Codex:/b", "root": "/b"}
        self.assertNotEqual(b.session(a, "same", "Title", "/tmp", 1)["id"], b.session(c, "same", "Title", "/tmp", 1)["id"])

    def test_paginated_codex_reads_history_without_rollout(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            c = sqlite3.connect(root / "state_5.sqlite")
            c.execute("create table threads (id,title,cwd,updated_at,rollout_path,source,creator_account_id,archived)")
            c.execute("insert into threads values (?,?,?,?,?,?,?,?)", ("one", "SQLite chat", temp, 1, "missing.jsonl", "cli", "acct", 0))
            c.commit(); c.close()
            c = sqlite3.connect(root / "thread_history_1.sqlite")
            c.execute("create table thread_items(thread_id,item_id,item_json,item_type,rollout_ordinal)")
            for i, (kind, item) in enumerate([("userMessage", {"type":"userMessage","content":[{"type":"text","text":"remember orange"}]}), ("reasoning", {"type":"reasoning","text":"private"}), ("agentMessage", {"type":"agentMessage","text":"Orange"})]):
                c.execute("insert into thread_items values (?,?,?,?,?)", ("one", str(i), json.dumps(item), kind, i))
            c.commit(); c.close()
            source = {"id":"Codex:"+temp,"provider":"Codex","label":"test","root":temp}
            records = b.codex_sessions(source)
            history = b.transcript(records[0], 1)
            self.assertEqual(records[0]["accountID"], "acct")
            self.assertEqual(history["total"], 2)
            self.assertEqual(history["messages"][0]["text"], "Orange")
            self.assertFalse(Path(temp, "state_5.sqlite-journal").exists())

    def test_claude_import_and_text_only_history(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp, "projects", "-test"); folder.mkdir(parents=True)
            path = folder / "one.jsonl"
            entries = [{"type":"user","sessionId":"one","cwd":temp,"uuid":"a","message":{"content":[{"type":"text","text":"Hello"}]}}, {"type":"assistant","uuid":"b","message":{"content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Hi"},{"type":"tool_use","name":"Bash"}]}}, {"type":"user","uuid":"c","message":{"content":[{"type":"tool_result","content":"not a user message"}]}}]
            path.write_text("\n".join(json.dumps(x) for x in entries) + "\n{partial")
            source = {"id":"Claude:"+temp,"provider":"Claude","label":"test","root":temp}
            records = b.claude_sessions(source, {})
            self.assertEqual(records[0]["title"], "Hello")
            self.assertEqual([x["text"] for x in b.transcript(records[0])["messages"]], ["Hello", "Hi"])

    def test_runtime_is_profile_bound_and_has_no_bypass(self):
        for provider, key in [("Codex", "CODEX_HOME"), ("Claude", "CLAUDE_CONFIG_DIR")]:
            s = {"provider":provider,"sessionID":"abc","root":"/test/profile"}
            self.assertEqual(b.runtime_env(s)[key], "/test/profile")
            self.assertNotIn("--dangerously-bypass-approvals-and-sandbox", b.runtime_command(s))
            self.assertNotIn("--dangerously-skip-permissions", b.runtime_command(s))
            self.assertIn("abc", b.runtime_command(s))

    def test_default_claude_preserves_default_keychain_namespace(self):
        env = b.runtime_env({"provider":"Claude", "root":str(Path.home()/".claude")})
        self.assertNotIn("CLAUDE_CONFIG_DIR", env)

if __name__ == "__main__": unittest.main()
