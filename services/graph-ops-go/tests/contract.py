"""Exercise the compiled Go API via Python's real HTTP client and synthetic data."""
import json
from pathlib import Path
import socket
import subprocess
import sys
import time
from urllib.request import Request, urlopen


def main(binary):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        addr = "127.0.0.1:" + str(sock.getsockname()[1])
    fixtures = Path(__file__).resolve().parents[1] / "testdata"

    def call(path, data=None, content_type="application/json"):
        req = Request("http://" + addr + path, data, {"Content-Type": content_type})
        with urlopen(req, timeout=2) as response:
            assert len(response.headers["X-Request-ID"]) == 32
            return response.read()

    process = subprocess.Popen([binary, "-listen", addr])
    try:
        for _ in range(50):
            if process.poll() is not None:
                raise RuntimeError("Go server exited before health check")
            try:
                health = json.loads(call("/health/ready"))
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("Go server did not become ready")
        assert health == {"status": "ok", "snapshot_source": "request", "database_connected": False}
        imported = json.loads(call("/api/v1/graph/import/validate", (fixtures / "import.csv").read_bytes(), "text/csv"))
        assert imported == {"valid": True, "courses": 2, "relations": 1, "errors": []}
        request = (fixtures / "preview.json").read_bytes()
        first = call("/api/v1/graph/sync/preview", request)
        assert first == call("/api/v1/graph/sync/preview", request)
        plan = json.loads(first)
        assert plan["executable"] and plan["statistics"] == {
            "courses": {"added": 1, "modified": 1, "delete_candidates": 0, "unchanged": 0},
            "relations": {"added": 1, "modified": 0, "delete_candidates": 0, "unchanged": 0},
            "errors": 0,
        }
        assert b"graphops_diff_duration_seconds_count 2" in call("/metrics")
        print("PASS: readiness, CSV import, deterministic JSON preview, metrics")
    finally:
        process.terminate()
        process.wait(timeout=10)


if __name__ == "__main__":
    main(sys.argv[1])
