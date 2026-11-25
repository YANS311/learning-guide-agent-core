"""Explicitly provision a newly started empty synthetic instance; never used by the application."""
import json
import os
from pathlib import Path

from neo4j import GraphDatabase

from prereq.repository import DATABASE, Neo4jRepository

ROOT = Path(__file__).resolve().parents[1]


def main():
    uri, marker = os.environ["PREREQ_LAB_BOLT"], os.environ["PREREQ_LAB_MARKER"]
    guard = Neo4jRepository(uri, marker)
    guard.close()
    data = json.loads((ROOT / "testdata" / "catalog.json").read_text(encoding="utf-8"))
    with GraphDatabase.driver(uri, auth=None) as driver, driver.session(database=DATABASE) as session:
        assert session.run("CALL db.info() YIELD name RETURN name").single()[0] == DATABASE
        assert session.run("MATCH (n) RETURN count(n)").single()[0] == 0, "fixture requires a clean database"
        with session.begin_transaction(timeout=5) as tx:
            tx.run("CREATE (:PrereqAnalysisLab {token:$marker})", marker=marker).consume()
            tx.run("UNWIND $courses AS row CREATE (c:Course) SET c=row", courses=data["courses"]).consume()
            tx.run("""UNWIND $relations AS row MATCH (a:Course {id:row.source}),(b:Course {id:row.target})
                      CREATE (a)-[:PREREQ {strength:coalesce(row.strength,'strong'),reason:coalesce(row.reason,'')}]->(b)""",
                   relations=data["relations"]).consume()
            tx.commit()
        assert session.run("MATCH (n:Course) RETURN count(n)").single()[0] == 8
        assert session.run("MATCH ()-[r:PREREQ]->() RETURN count(r)").single()[0] == 9
    print("PASS: empty isolated database seeded with 8 synthetic courses and 9 relations")


if __name__ == "__main__":
    main()
