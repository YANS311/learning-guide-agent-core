import unittest
from pathlib import Path
from server import Search, read_corpus


class SearchContract(unittest.TestCase):
    def test_bm25_and_missing_evidence(self):
        root = Path(__file__).resolve().parents[1]
        engine = Search(read_corpus(root / "testdata" / "handbook.json"))
        result = engine.find("信号 嵌入式")
        self.assertEqual(result["mode"], "PYTHON_BM25")
        self.assertEqual(result["evidence"][0]["doc_id"], "SYNTHETIC-GUIDE")
        self.assertEqual(result["evidence"][0]["version"], "1")
        self.assertEqual(engine.find("zzzxvbnnn")["evidence"], [])


if __name__ == "__main__":
    unittest.main()
