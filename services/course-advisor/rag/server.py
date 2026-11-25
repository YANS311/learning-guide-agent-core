"""Optional loopback-only evidence adapter; never decides course eligibility."""
from argparse import ArgumentParser
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from threading import Lock
import json
import os
import re

import jieba
from rank_bm25 import BM25Plus

MAX_SOURCE_BYTES = 1024 * 1024
FIELDS = {"doc_id": 64, "version": 80, "section": 100, "source": 140, "excerpt": 600}


def tokens(text):
    return [word for word in jieba.cut_for_search(text.lower()) if word.strip() and re.search(r"\w", word)]


def read_corpus(path):
    with path.open("rb") as file:
        raw = file.read(MAX_SOURCE_BYTES + 1)
    if len(raw) > MAX_SOURCE_BYTES:
        raise ValueError("evidence corpus limit")
    rows = json.loads(raw)
    if not isinstance(rows, list) or len(rows) > 200:
        raise ValueError("evidence corpus limit")
    result = []
    for row in rows:
        if not isinstance(row, dict) or not all(isinstance(row.get(k), str) and
                0 < len(row[k]) <= limit and "\x00" not in row[k] for k, limit in FIELDS.items()):
            raise ValueError("invalid evidence metadata")
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}", row["doc_id"]):
            raise ValueError("invalid document ID")
        result.append({k: row[k] for k in FIELDS})
    return result


class Search:
    def __init__(self, rows, embedding_dir=None):
        self.rows = rows
        self.lock = Lock()
        texts = [row["section"] + " " + row["excerpt"] for row in rows]
        self.words = [tokens(text) for text in texts]
        self.word_sets = [set(row) for row in self.words]
        self.bm25 = BM25Plus(self.words) if self.words else None
        self.encoder = self.index = None
        if embedding_dir is not None and texts:
            if not embedding_dir.is_dir():
                raise ValueError("embedding directory must already be present")
            os.environ["HF_HUB_OFFLINE"] = "1"
            os.environ["TRANSFORMERS_OFFLINE"] = "1"
            import faiss
            from sentence_transformers import SentenceTransformer
            self.encoder = SentenceTransformer(str(embedding_dir), local_files_only=True, trust_remote_code=False,
                                               device="cpu", model_kwargs={"use_safetensors": True})
            matrix = self.encoder.encode(texts, normalize_embeddings=True, convert_to_numpy=True).astype("float32")
            self.index = faiss.IndexFlatIP(matrix.shape[1])
            self.index.add(matrix)

    def find(self, query):
        query_tokens = tokens(query)
        if not query_tokens or not self.bm25:
            return {"mode": "PYTHON_BM25", "evidence": []}
        with self.lock:
            scores = self.bm25.get_scores(query_tokens)
            terms = set(query_tokens)
            lexical = sorted((i for i, row in enumerate(self.word_sets) if row & terms),
                             key=lambda i: (-scores[i], i))[:12]
            if self.index is None:
                result = [(i, float(scores[i])) for i in lexical[:4]]
                mode = "PYTHON_BM25"
            else:
                vector = self.encoder.encode([query], normalize_embeddings=True, convert_to_numpy=True).astype("float32")
                distances, indexes = self.index.search(vector, min(12, len(self.rows)))
                semantic = [int(i) for s, i in zip(distances[0], indexes[0]) if i >= 0 and s >= 0.50]
                fused = {}
                for ranking in (lexical, semantic):
                    for rank, i in enumerate(ranking, 1):
                        fused[i] = fused.get(i, 0.0) + 1 / (60 + rank)
                result = sorted(fused.items(), key=lambda entry: (-entry[1], entry[0]))[:4]
                mode = "PYTHON_FAISS_BM25_RRF"
        return {"mode": mode, "evidence": [{**self.rows[i], "score": score} for i, score in result]}


def handler(search):
    class EvidenceHandler(BaseHTTPRequestHandler):
        timeout = 3

        def log_message(self, fmt, *args):
            pass

        def do_POST(self):
            if self.path != "/evidence":
                self.send_error(404)
                return
            try:
                count = int(self.headers.get("Content-Length", "0"))
                if not 0 < count <= 4096 or self.headers.get_content_type() != "application/json":
                    raise ValueError("invalid request")
                doc = json.loads(self.rfile.read(count))
                if (not isinstance(doc, dict) or set(doc) != {"query"} or not isinstance(doc["query"], str)
                        or not 0 < len(doc["query"]) <= 480):
                    raise ValueError("invalid query")
            except (ValueError, OSError):
                self.send_error(400)
                return
            payload = json.dumps(search.find(doc["query"]), ensure_ascii=False).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            try:
                self.wfile.write(payload)
            except (ConnectionError, TimeoutError):
                pass

    return EvidenceHandler


def main():
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("--port", default=8195, type=int)
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("--embedding-dir", type=Path)
    args = parser.parse_args()
    if not 1024 <= args.port <= 65535:
        parser.error("invalid port")
    search = Search(read_corpus(args.evidence), args.embedding_dir)
    with HTTPServer(("127.0.0.1", args.port), handler(search)) as server:
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass


if __name__ == "__main__":
    main()
