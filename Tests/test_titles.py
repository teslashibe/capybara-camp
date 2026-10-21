import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "Resources"))
from bridge import title_for


class TitleTests(unittest.TestCase):
    def test_large_titles_keep_the_same_displayed_prefix(self):
        for raw in ["word " * 500_000, "x" * 2_000_000, "  First\tsecond\nthird  " * 100_000,
                    " " * 100_000 + "First\u2003second", "first" + " " * 100_000 + "last"]:
            self.assertEqual(title_for(raw), " ".join(raw.split())[:110])

    def test_instruction_and_empty_titles_remain_generic(self):
        for raw in [" # AGENTS.md instructions", "You\nare an assistant", "<xml>instructions</xml>"]:
            self.assertEqual(title_for(raw), "Imported session")
        self.assertEqual(title_for(" \t\n"), "Untitled session")


if __name__ == "__main__":
    unittest.main()
