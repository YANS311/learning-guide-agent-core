"""Versioned file snapshots and a guarded Neo4j batch reader."""
from pathlib import Path
from threading import Lock
from time import monotonic
import json
import re
from urllib.parse import urlsplit

from neo4j import GraphDatabase, READ_ACCESS
from neo4j.exceptions import DriverError, Neo4jError
from pydantic import ValidationError

from .domain import AnalysisError, Dataset, MAX_EDGES, MAX_NODES, Snapshot, check

DATABASE = "prereq-phase4-test"
MAX_FILE = 8 * 1024 * 1024
PROJECTION = """
MATCH (marker:PrereqAnalysisLab {token: $marker})
WITH marker LIMIT 2
WITH collect(marker) AS markers
WHERE size(markers) = 1
CALL () {
    MATCH (c:Course)
    WITH c LIMIT $nodes
    RETURN collect({id:c.id,name:c.name,semester:coalesce(c.semester,''),
                    credits:coalesce(c.credits,0)}) AS courses
}
CALL () {
    MATCH (a:Course)-[e:PREREQ]->(b:Course)
    WITH a,e,b LIMIT $edges
    RETURN collect({source:a.id,target:b.id,strength:coalesce(e.strength,'strong'),
                    reason:coalesce(e.reason,'')}) AS relations
}
RETURN courses,relations
"""


def validated(doc):
    try:
        return Snapshot.build(Dataset.model_validate(doc))
    except ValidationError:
        raise AnalysisError(503, "invalid_snapshot") from None


class FileRepository:
    def __init__(self, path: Path):
        self.path = path

    def read(self, deadline):
        check(deadline)
        try:
            with self.path.open("rb") as source:
                data = source.read(MAX_FILE + 1)
            if len(data) > MAX_FILE:
                raise AnalysisError(503, "snapshot_size_limit")
            parsed = json.loads(data)
        except (OSError, ValueError):
            raise AnalysisError(503, "snapshot_unavailable") from None
        check(deadline)
        return validated(parsed)

    def close(self):
        pass


class Neo4jRepository:
    def __init__(self, uri, marker):
        loc = urlsplit(uri)
        if (loc.scheme != "bolt" or loc.hostname != "127.0.0.1" or loc.port in (None, 7687)
                or loc.port < 1024 or loc.netloc != f"127.0.0.1:{loc.port}" or loc.path or loc.query or loc.fragment
                or not re.fullmatch(r"[0-9a-fA-F]{64}", marker)):
            raise ValueError("expected guarded loopback prerequisite lab")
        self._driver = GraphDatabase.driver(uri, auth=None, max_connection_pool_size=4,
                                            connection_timeout=0.7, connection_acquisition_timeout=1.0)
        self._marker = marker

    def read(self, deadline):
        check(deadline)
        try:
            with self._driver.session(database=DATABASE, default_access_mode=READ_ACCESS) as session:
                with session.begin_transaction(timeout=max(0.05, min(1.5, deadline - monotonic()))) as tx:
                    row = tx.run(PROJECTION, marker=self._marker, nodes=MAX_NODES + 1, edges=MAX_EDGES + 1).single()
                    if row is None:
                        raise AnalysisError(503, "snapshot_identity_unverified")
                    raw = row.data()
                    tx.commit()
        except (DriverError, Neo4jError) as exc:
            code = str(getattr(exc, "code", ""))
            if "TimedOut" in code or "Timeout" in code or monotonic() >= deadline:
                raise AnalysisError(504, "query_timeout") from None
            raise AnalysisError(503, "snapshot_unavailable") from None
        check(deadline)
        return validated(raw)

    def close(self):
        self._driver.close()


class SnapshotCache:
    """One reload per process. Never use an expired snapshot after a failed load."""
    def __init__(self, source, ttl=30.0):
        self.source, self.ttl = source, ttl
        self._lock = Lock()
        self._entry = None
        self._until = 0.0
        self.reads = self.hits = self.misses = 0

    def get(self, deadline):
        if not self._lock.acquire(timeout=max(0, deadline - monotonic())):
            raise AnalysisError(504, "query_timeout")
        try:
            check(deadline)
            if self._entry is not None and monotonic() < self._until:
                self.hits += 1
                return self._entry, "hit"
            self.misses += 1
            self.reads += 1
            fresh = self.source.read(deadline)
            check(deadline)
            self._entry, self._until = fresh, monotonic() + self.ttl
            return fresh, "miss"
        finally:
            self._lock.release()
