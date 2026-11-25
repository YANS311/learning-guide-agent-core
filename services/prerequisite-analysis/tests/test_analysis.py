from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from time import monotonic, sleep

import pytest
from fastapi.testclient import TestClient

from prereq.app import create_app
from prereq.domain import Analysis, AnalysisError, Course, Dataset, Relation, Snapshot
from prereq.repository import FileRepository, SnapshotCache

ROOT = Path(__file__).resolve().parents[1]


def synthetic():
    return FileRepository(ROOT / "testdata" / "catalog.json")


def relation(a, b, strength="strong"):
    return Relation(source=a, target=b, strength=strength)


def snap(*edges, courses=("A", "B", "C", "D")):
    return Snapshot.build(Dataset(courses=tuple(Course(id=c, name=c) for c in courses), relations=edges))


def test_multilevel_paths_and_order():
    s = snap(relation("B", "D"), relation("A", "B"), relation("C", "D"), relation("A", "C"), relation("A", "B"))
    a = Analysis(s, "all", monotonic() + 5)
    result = a.prerequisites("D", max_depth=1)
    assert s.duplicates == 1
    assert [x.id for x in result["direct"]] == ["B", "C"]
    assert [x.id for x in result["all_prerequisites"]] == ["B", "C", "A"]
    assert result["depth"] == 2 and result["paths_truncated"]
    assert [x.id for x in a.learning_order("D")["sequence"]] == ["A", "B", "C", "D"]
    assert a.prerequisites("D")["paths"][-1]["path"] == ["A", "B", "D"]
    assert a.dependents("A")["paths"][-1]["path"] == ["A", "B", "D"]
    assert a.compare("B", "C")["common"][0].id == "A"
    assert a.missing("D", ["B"]) == {"course_id": "D", "satisfied_direct": ["B"],
                                    "missing_direct": ["C"], "missing_indirect": ["A"]}


def test_strength_policy_scope_and_isolated():
    s = snap(relation("A", "B", "weak"), relation("B", "C"))
    required = Analysis(s, "required", monotonic() + 5)
    assert len(required.prerequisites("C")["all_prerequisites"]) == 1
    assert required.prerequisites("D")["paths"] == []
    assert required.dependents("D")["impact_count"] == 0
    assert len(Analysis(s, "all", monotonic() + 5).prerequisites("C")["all_prerequisites"]) == 2


def test_relevant_cycles_and_bad_edges():
    s = snap(relation("A", "B"), relation("B", "A"), relation("A", "C"))
    a = Analysis(s, "all", monotonic() + 5)
    with pytest.raises(AnalysisError, match="dependency_cycle"):
        a.learning_order("C")
    with pytest.raises(AnalysisError, match="dependency_cycle"):
        a.dependents("A")
    assert len(a.learning_order("D")["sequence"]) == 1
    with pytest.raises(AnalysisError, match="course_not_found"):
        a.prerequisites("Z")
    with pytest.raises(AnalysisError, match="dangling_relation"):
        snap(relation("A", "Z"))
    with pytest.raises(AnalysisError, match="ambiguous_relation"):
        snap(relation("A", "B"), relation("A", "B", "weak"))
    with pytest.raises(AnalysisError, match="duplicate_course"):
        snap(courses=("A", "A"))


def test_hash_is_independent_of_input_order():
    edges = (relation("A", "B"), relation("C", "D"))
    assert snap(*edges).version == snap(*reversed(edges), courses=("D", "A", "C", "B")).version
    assert snap(*edges).version != snap(*edges[:-1]).version


def test_http_contract():
    with TestClient(create_app(synthetic())) as c:
        assert c.get("/").status_code == 200
        assert c.get("/openapi.json").status_code == 200
        assert c.get("/api/v1/courses", params={"q":"信号"}).json()["data"]["total"] == 3
        route = "/api/v1/prerequisites"
        detail = c.get(route + "/C401")
        assert detail.status_code == 200 and len(detail.headers["X-Request-ID"]) == 32
        assert detail.json()["data"]["depth"] == 3
        assert detail.json()["cache"] == "hit"
        assert c.post(route + "/compare", json={"left":"C301", "right":"C302"}).json()["data"]["common"]
        assert c.post(route + "/missing", json={"course_id":"C401", "completed":["C301"]}).json()["data"]["missing_direct"] == ["C302"]
        assert c.post(route + "/learning-order", json={"course_id":"C401"}).json()["data"]["sequence"][-1]["id"] == "C401"
        assert c.get(route + "/C101/dependents").json()["data"]["impact_count"] > 0
        absent = c.get(route + "/absent")
        assert absent.status_code == 404 and absent.json()["error"]["code"] == "course_not_found"
        assert c.post(route + "/compare", json={"left":"unsafe id", "right":"A"}).status_code == 422
        assert c.post(route + "/compare", content=b" " * 32769).status_code == 413
        assert c.get(route + "/C401?max_depth=100").status_code == 422
        assert c.get("/api/v1/metrics").json()["repository_reads"] == 1


class Source:
    def __init__(self, delay=0, fail=False):
        self.delay = delay
        self.fail = fail

    def read(self, deadline):
        sleep(self.delay)
        if self.fail:
            raise AnalysisError(503, "snapshot_unavailable")
        return snap(relation("A", "B"))

    def close(self):
        pass


def test_unavailable_expired_cache_and_query_timeout():
    source = Source()
    cache = SnapshotCache(source, ttl=0)
    assert cache.get(monotonic() + 1)
    source.fail = True
    with pytest.raises(AnalysisError, match="snapshot_unavailable"):
        cache.get(monotonic() + 1)
    with TestClient(create_app(source)) as c:
        assert c.get("/health/live").status_code == 200
        assert c.get("/health/ready").status_code == 503
    with TestClient(create_app(Source(delay=0.04), timeout=0.02)) as c:
        assert c.get("/api/v1/prerequisites/B").status_code == 504


def test_concurrent_cold_read_is_single_flight():
    with TestClient(create_app(Source(delay=0.05))) as c, ThreadPoolExecutor(max_workers=8) as pool:
        responses = list(pool.map(lambda _: c.get("/api/v1/prerequisites/B"), range(16)))
        assert all(r.status_code == 200 for r in responses)
        assert sum(r.json()["cache"] == "miss" for r in responses) == 1
        assert c.get("/api/v1/metrics").json()["repository_reads"] == 1


def test_sync_cycle_returns_409():
    source = Source()
    source.read = lambda _: snap(relation("A", "B"), relation("B", "A"))
    with TestClient(create_app(source)) as c:
        out = c.post("/api/v1/prerequisites/learning-order", json={"course_id":"B"})
        assert out.status_code == 409 and out.json()["error"]["code"] == "dependency_cycle"
