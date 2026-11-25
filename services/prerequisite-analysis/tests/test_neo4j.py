import os

import pytest
from fastapi.testclient import TestClient
from neo4j import GraphDatabase

from prereq.app import create_app
from prereq.repository import DATABASE, Neo4jRepository


def test_isolated_neo4j_read_only_and_cache():
    uri = os.getenv("PREREQ_LAB_BOLT")
    marker = os.getenv("PREREQ_LAB_MARKER")
    if not uri and not marker:
        pytest.skip("set isolated Neo4j lab credentials and run bootstrap first")
    assert uri and marker
    before = graph_contents(uri)
    with TestClient(create_app(Neo4jRepository(uri, marker))) as client:
        detail = client.get("/api/v1/prerequisites/C401")
        assert detail.status_code == 200
        assert detail.json()["data"]["depth"] == 3
        assert len(detail.json()["data"]["relations"]) == 9
        assert client.get("/api/v1/prerequisites/C301").json()["cache"] == "hit"
        assert client.get("/api/v1/metrics").json()["repository_reads"] == 1
    other = ("0" if marker[0] != "0" else "1") + marker[1:]
    with TestClient(create_app(Neo4jRepository(uri, other))) as client:
        assert client.get("/health/ready").json()["error"]["code"] == "snapshot_identity_unverified"
    assert graph_contents(uri) == before
    print("PASS: database contents unchanged before/after batch reads")


def graph_contents(uri):
    with GraphDatabase.driver(uri, auth=None) as driver, driver.session(database=DATABASE) as session:
        courses = session.run("MATCH (n:Course) RETURN properties(n) AS data ORDER BY n.id").data()
        edges = session.run("""MATCH (a)-[r:PREREQ]->(b)
                               RETURN a.id AS source,b.id AS target,properties(r) AS data ORDER BY a.id,b.id""").data()
        return courses, edges


@pytest.mark.parametrize("uri", ["bolt://127.0.0.1:7687", "bolt://localhost:27694", "neo4j://127.0.0.1:27694",
                                 "bolt://192.0.2.1:27694", "bolt://127.0.0.1:27694?x=1"])
def test_reject_other_endpoints(uri):
    with pytest.raises(ValueError):
        Neo4jRepository(uri, "a" * 64)
