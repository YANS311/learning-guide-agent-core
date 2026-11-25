"""Run the compiled Go server against the isolated, bootstrapped lab."""
from contextlib import contextmanager
import json
import os
import socket
import subprocess
import sys
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen


@contextmanager
def server(binary, env):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        addr = "127.0.0.1:" + str(sock.getsockname()[1])
    process = subprocess.Popen([binary, "-listen", addr], env=env)
    try:
        def call(path, data=None, key=None):
            headers = {"Content-Type": "application/json"}
            if key is not None:
                headers["Idempotency-Key"] = key
            req = Request("http://" + addr + path, None if data is None else json.dumps(data).encode(), headers)
            try:
                response = urlopen(req, timeout=8)
            except HTTPError as error:
                response = error
            with response:
                assert len(response.headers["X-Request-ID"]) == 32
                result = response.read()
                return response.code, (result if path == "/metrics" else json.loads(result))

        for _ in range(50):
            if process.poll() is not None:
                raise RuntimeError("Go server exited early")
            try:
                assert call("/health/live")[0] == 200
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("Go server did not start")
        yield call
    finally:
        process.terminate()
        process.wait(timeout=10)


def main(binary):
    root = "/api/v1/graph/db/"
    target = {"courses": [{"id": "LAB-HTTP", "name": "Synthetic HTTP contract"}], "relations": []}
    key = "synthetic-http-contract"
    with server(binary, os.environ.copy()) as call:
        status, health = call("/health/ready")
        assert status == 200 and health["snapshot_source"] == "neo4j" and health["database_connected"]
        status, original = call("/api/v1/graph/sync/preview", {"current": {}, "target": {}})
        assert status == 200 and original["executable"]
        status, preview = call(root + "preview", {"scope": {"course_ids": ["LAB-HTTP"]}, "target": target})
        assert status == 200 and preview["plan"]["statistics"]["courses"]["added"] == 1
        command = {"plan_token": preview["plan_token"], "target": target}
        status, result = call(root + "execute", command, key)
        assert status == 200 and not result["replay"] and result["operation"]["status"] == "committed"
        assert result["operation"]["statistics"]["courses"]["added"] == 1
        status, replay = call(root + "execute", command, key)
        assert status == 200 and replay["replay"] and replay["operation"] == result["operation"]
        status, conflict = call(root + "execute", {"plan_token": preview["plan_token"], "target": {}}, key)
        assert status == 409 and conflict["errors"][0]["code"] == "idempotency_conflict"
        status, metrics = call("/metrics")
        assert status == 200 and b"graphops_execute_replays_total 1" in metrics and b"graphops_neo4j_ready 1" in metrics
    with server(binary, os.environ.copy()) as call:
        status, stored = call(root + "operation", key=key)
        assert status == 200 and stored == result["operation"]
    with socket.socket() as socket_with_no_bolt:
        socket_with_no_bolt.bind(("127.0.0.1", 0))
        env = os.environ.copy()
        env["GRAPHOPS_LAB_BOLT"] = "bolt://127.0.0.1:" + str(socket_with_no_bolt.getsockname()[1])
        with server(binary, env) as call:
            assert call("/health/live")[0] == 200
            assert call("/health/ready")[0] == 503
            assert call("/api/v1/graph/sync/preview", {"current": {}, "target": {}})[0] == 200
    print("PASS: server snapshot, commit, replay, conflict, restart lookup, dependency failure, Phase 1 compatibility")


if __name__ == "__main__":
    main(sys.argv[1])
