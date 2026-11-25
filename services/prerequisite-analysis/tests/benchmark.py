"""Same service and requests with app-level snapshot cache disabled or enabled."""
from argparse import ArgumentParser
from concurrent.futures import ThreadPoolExecutor
from math import ceil
from pathlib import Path
from platform import python_version, system
from tempfile import TemporaryDirectory
from time import perf_counter
import json
import os

from fastapi.testclient import TestClient

from prereq.app import create_app
from prereq.repository import FileRepository, Neo4jRepository


def catalog(size):
    courses = [{"id": f"C{i:04}", "name": f"Course {i}", "semester": str(i // 20 + 1)} for i in range(size)]
    relations = [
        {"source": f"C{j:04}", "target": f"C{i:04}"}
        for i in range(20, size) for j in (i - 20, (i // 20 - 1) * 20 + (i - 1) % 20)
    ]
    return {"courses": courses, "relations": relations}


def run(make_source, course, concurrency, cache, count):
    app = create_app(make_source(), ttl=30 if cache else 0)
    with TestClient(app) as client:
        def request(_):
            start = perf_counter()
            response = client.get(f"/api/v1/prerequisites/{course}?path_limit=8")
            elapsed = (perf_counter() - start) * 1000
            payload = response.json()
            identity = (payload.get("snapshot_version"), payload.get("data"))
            return elapsed, response.status_code, identity

        with ThreadPoolExecutor(max_workers=concurrency) as workers:
            results = list(workers.map(request, range(count)))
    timings = sorted(round(row[0], 3) for row in results)
    errors = sum(row[1] != 200 for row in results)
    assert not errors and all(row[2] == results[0][2] for row in results)
    stats = app.state.cache
    return {
        "cache": cache, "concurrency": concurrency, "requests": count,
        "p50_ms": timings[ceil(count * .5) - 1], "p95_ms": timings[ceil(count * .95) - 1],
        "errors": errors, "source_reads": stats.reads, "hits": stats.hits,
        "misses": stats.misses, "timings_ms": timings,
    }, results[0][2]


def main():
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("--requests", type=int, default=24)
    args = parser.parse_args()
    if args.requests < 8:
        parser.error("at least eight requests per case")
    output = {"python": python_version(), "system": system(), "cases": []}
    with TemporaryDirectory() as directory:
        sources = []
        for size in (100, 500, 1500):
            path = Path(directory) / f"{size}.json"
            data = catalog(size)
            path.write_text(json.dumps(data), encoding="utf-8")
            sources.append(("file", size, len(data["relations"]), f"C{size-1:04}",
                            lambda path=path: FileRepository(path)))
        if os.getenv("PREREQ_LAB_BOLT"):
            sources.append(("bolt", 8, 9, "C401", lambda: Neo4jRepository(
                os.environ["PREREQ_LAB_BOLT"], os.environ["PREREQ_LAB_MARKER"])))
        for source, nodes, edges, course, make_source in sources:
            for concurrency in (1, 8):
                uncached, original = run(make_source, course, concurrency, False, args.requests)
                cached, optimized = run(make_source, course, concurrency, True, args.requests)
                assert original == optimized
                assert uncached["source_reads"] == args.requests and cached["source_reads"] == 1
                output["cases"].append({"source": source, "nodes": nodes, "edges": edges,
                                        "uncached": uncached, "cached": cached})
    args.output.write_text(json.dumps(output, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
