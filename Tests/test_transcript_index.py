import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / "Resources"))
from transcript_index import export_page


class ExportTests(unittest.TestCase):
    def test_pages_updates_deletions_and_reappearance_keep_ordinals(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = root / "session.jsonl"
            session = {"provider": "Claude", "root": temp, "sessionID": "fixture", "path": str(path)}
            def record(ident, text):
                return json.dumps({"type": "assistant", "uuid": ident, "message": {"model": "fixture-model", "content": text}}) + "\n"
            path.write_text(record("a", "First") + record("b", "Second") + record("a", "First amended"))
            first = export_page(session, root / "support", limit=1)
            self.assertEqual(first["messages"][0]["text"], "First amended")
            self.assertEqual(first["messages"][0]["model"], "fixture-model")
            self.assertTrue(first["hasMore"])
            second = export_page(session, root / "support", after=first["after"], limit=1)
            self.assertEqual(second["messages"][0]["id"], "b")
            self.assertFalse(second["hasMore"])
            path.write_text(record("b", "Second amended"))
            changed = export_page(session, root / "support")
            self.assertEqual([(x["id"], x["ordinal"], x["removed"]) for x in changed["messages"]], [("a", 0, True), ("b", 1, False)])
            path.write_text(record("b", "Second amended") + record("a", "First returns") + record("c", "Third"))
            changed = export_page(session, root / "support")
            self.assertEqual([(x["id"], x["ordinal"]) for x in changed["messages"]], [("a", 0), ("b", 1), ("c", 2)])
            self.assertFalse(changed["messages"][0]["removed"])
            database = next((root / "support/transcript-index").glob("*.sqlite"))
            self.assertEqual(database.stat().st_mode & 0o777, 0o600)

    def test_size_bound_and_profile_isolation(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = root / "session.jsonl"
            records = [{"type": "assistant", "uuid": str(i), "message": {"content": "x" * 600_000}} for i in range(3)]
            path.write_text("\n".join(json.dumps(x) for x in records))
            s = {"provider": "Claude", "root": temp, "sessionID": "fixture", "path": str(path)}
            page = export_page(s, root / "support")
            self.assertEqual(len(page["messages"]), 1)
            self.assertTrue(page["hasMore"])
            export_page({**s, "root": temp + "/another"}, root / "support")
            self.assertEqual(len(list((root / "support/transcript-index").glob("*.sqlite"))), 2)
            path.write_text(json.dumps({"type": "assistant", "uuid": "large", "message": {"content": "x" * 1_100_000}}))
            with self.assertRaisesRegex(ValueError, "1 MiB"):
                export_page(s, root / "support")

    def test_partial_write_does_not_commit_a_deletion(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); path = root / "session.jsonl"
            complete = json.dumps({"type": "assistant", "uuid": "a", "message": {"content": "Complete answer"}})
            s = {"provider": "Claude", "root": temp, "sessionID": "fixture", "path": str(path)}
            path.write_text(complete + "\n")
            self.assertEqual(export_page(s, root / "support")["messages"][0]["text"], "Complete answer")
            path.write_text('{"type":"assistant"')
            with self.assertRaisesRegex(ValueError, "incomplete"):
                export_page(s, root / "support")
            path.write_text(complete + "\n")
            restored = export_page(s, root / "support")["messages"][0]
            self.assertFalse(restored["removed"])
            self.assertEqual(restored["ordinal"], 0)


if __name__ == "__main__":
    unittest.main()
