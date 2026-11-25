"""Application-level query orchestration and concurrency budget."""
from threading import BoundedSemaphore
from time import monotonic
from unicodedata import normalize

from .domain import Analysis, AnalysisError, check


class Service:
    def __init__(self, cache, timeout=2.0, workers=8):
        self.cache, self.timeout = cache, timeout
        self._slots = BoundedSemaphore(workers)

    def run(self, policy, operation, deadline=None):
        deadline = monotonic() + self.timeout if deadline is None else deadline
        if not self._slots.acquire(timeout=max(0, deadline - monotonic())):
            raise AnalysisError(504, "query_timeout")
        try:
            snapshot, cache = self.cache.get(deadline)
            data = operation(Analysis(snapshot, policy, deadline))
            check(deadline)
            return {"snapshot_version": snapshot.version, "cache": cache, "policy": policy, "data": data}
        finally:
            self._slots.release()

    def search(self, query, limit):
        def select(analysis):
            folded = normalize("NFKC", query).casefold().strip()
            rows = list(analysis.snapshot.courses.values())
            found = [c for c in rows if folded in c.id.casefold() or folded in normalize("NFKC", c.name).casefold()]
            return {"items": found[:limit], "total": len(found)}
        return self.run("all", select)
