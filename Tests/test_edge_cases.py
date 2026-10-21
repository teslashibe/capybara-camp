import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("bridge", Path(__file__).parents[1] / "Resources/bridge.py")
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)

class EdgeCases(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.support_patch = patch.object(b, "SUPPORT", self.root / "support")
        self.support_patch.start()

    def tearDown(self):
        self.support_patch.stop()
        self.temp.cleanup()

    def source(self, provider="Codex"):
        return {"id":provider+":"+str(self.root),"provider":provider,"root":str(self.root),"label":"fixture"}

    def test_legacy_codex_import_supplies_all_native_fields(self):
        folder = self.root / "sessions"; folder.mkdir()
        p = folder / "rollout.jsonl"
        p.write_text(json.dumps({"type":"session_meta","payload":{"id":"legacy","cwd":str(self.root)}})+"\n")
        entry = b.codex_sessions(self.source())[0]
        self.assertEqual(entry["accountID"], "")
        self.assertFalse(entry["archived"])
        self.assertEqual(b.detect_provider(self.root), "Codex")

    def test_legacy_archived_codex_retains_archive_state(self):
        folder = self.root / "archived_sessions"; folder.mkdir()
        (folder/"rollout.jsonl").write_text(json.dumps({"payload":{"id":"old","cwd":str(self.root)}}))
        self.assertEqual(b.detect_provider(self.root), "Codex")
        self.assertTrue(b.codex_sessions(self.source())[0]["archived"])

    def test_empty_history_database_falls_back_to_jsonl(self):
        c=sqlite3.connect(self.root/"thread_history_1.sqlite")
        c.execute("create table thread_items(thread_id,item_id,item_json,item_type,rollout_ordinal)");c.close()
        p=self.root/"rollout.jsonl"
        p.write_text(json.dumps({"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"legacy context"}]}})+"\n{partial")
        entry=b.session(self.source(),"old","Old",str(self.root),1,str(p))
        self.assertEqual(b.transcript(entry)["messages"][0]["text"],"legacy context")

    def test_invalid_source_is_rejected_without_saving(self):
        with self.assertRaisesRegex(ValueError,"Choose a Codex"):
            b.add_source(str(self.root))
        self.assertFalse((b.SUPPORT/"sources.json").exists())

    def test_source_can_have_any_directory_name_and_is_deduplicated(self):
        (self.root/"sessions").mkdir()
        b.add_source(str(self.root));b.add_source(str(self.root/"."))
        self.assertEqual(b.read_json(b.SUPPORT/"sources.json",[]),[str(self.root.resolve())])
        self.assertEqual(os.stat(b.SUPPORT/"sources.json").st_mode & 0o777,0o600)

    def test_disappeared_configured_source_is_reported(self):
        b.save_json(b.SUPPORT/"sources.json",[str((self.root/"missing").resolve())])
        with patch.object(b,"sources",return_value=[]):result=b.scan()
        self.assertIn("unavailable",result["errors"][0])
        self.assertEqual(result["failedRoots"], [str((self.root/"missing").resolve())])

    def test_failed_profile_scan_reports_root_for_ui_retention(self):
        source = self.source()
        with patch.object(b, "sources", return_value=[source]), patch.object(b, "codex_sessions", side_effect=sqlite3.OperationalError("busy")):
            result = b.scan()
        self.assertEqual(result["failedRoots"], [str(self.root)])
        self.assertEqual(result["sessions"], [])
        self.assertIn("busy", result["errors"][0])

    def test_duplicate_native_session_uses_newest_file(self):
        source=self.source("Claude")
        old=b.session(source,"same","old","/tmp",1)
        new=b.session(source,"same","new","/tmp",3)
        with patch.object(b,"sources",return_value=[source]),patch.object(b,"claude_sessions",return_value=[new,old]):result=b.scan()
        self.assertEqual(len(result["sessions"]),1)
        self.assertEqual(result["sessions"][0]["title"],"new")
        self.assertEqual(result["sources"][0]["count"],1)

    def test_claude_cache_refreshes_after_append(self):
        folder=self.root/"projects/test";folder.mkdir(parents=True)
        p=folder/"one.jsonl";p.write_text(json.dumps({"sessionId":"one","type":"user","cwd":str(self.root),"message":{"content":"hello"}}))
        cache={};b.claude_sessions(self.source("Claude"),cache)
        first=cache[str(p)]["signature"]
        with p.open("a") as f:f.write("\n{}")
        b.claude_sessions(self.source("Claude"),cache)
        self.assertNotEqual(first,cache[str(p)]["signature"])

    def test_subagent_claude_files_are_not_imported(self):
        folder=self.root/"projects/test";folder.mkdir(parents=True)
        (folder/"agent-one.jsonl").write_text('{}')
        (folder/"side.jsonl").write_text(json.dumps({"type":"user","isSidechain":True,"cwd":"/tmp","message":{"content":"subagent"}}))
        self.assertEqual(b.claude_sessions(self.source("Claude"),{}),[])

    def test_invalid_history_limit_is_rejected(self):
        with self.assertRaises(ValueError):b.transcript({},0)

    def test_inherited_credentials_do_not_override_profile(self):
        with patch.dict(os.environ,{"ANTHROPIC_API_KEY":"test-placeholder","OPENAI_API_KEY":"test-placeholder","CLAUDE_CODE_OAUTH_TOKEN":"test-placeholder"}):
            env=b.runtime_env({"provider":"Claude","root":str(self.root)})
        self.assertNotIn("ANTHROPIC_API_KEY",env)
        self.assertNotIn("OPENAI_API_KEY",env)
        self.assertNotIn("CLAUDE_CODE_OAUTH_TOKEN",env)

if __name__=="__main__":unittest.main()
