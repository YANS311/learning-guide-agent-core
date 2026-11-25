"""Standalone FastAPI entry point. No import from the teaching application."""
import argparse
from contextlib import asynccontextmanager
from ipaddress import ip_address
import logging
import os
from pathlib import Path
from threading import Lock
from time import monotonic
from uuid import uuid4

import anyio
import uvicorn
from fastapi import FastAPI, Query, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import FileResponse, JSONResponse
from pydantic import Field
from starlette.exceptions import HTTPException

from .domain import AnalysisError, CourseID, Input, MAX_DEPTH, MAX_NODES, MAX_PATHS, Policy
from .repository import FileRepository, Neo4jRepository, SnapshotCache
from .service import Service

NAME = "课程先修关系图谱分析系统"
ROOT = Path(__file__).resolve().parents[1]
MAX_BODY = 32768
LOG = logging.getLogger("prereq.requests")


class Constraint(Input):
    course_id: CourseID
    policy: Policy = "required"


class Missing(Constraint):
    completed: tuple[CourseID, ...] = Field(default=(), max_length=MAX_NODES)


class Comparison(Input):
    left: CourseID
    right: CourseID
    policy: Policy = "all"
    path_limit: int = Field(default=12, ge=0, le=MAX_PATHS)
    max_depth: int = Field(default=MAX_DEPTH, ge=1, le=MAX_DEPTH)


def failure(status, code, request_id, fields=None):
    return JSONResponse({"error": {"code": code, "request_id": request_id, "fields": fields or []}}, status_code=status)


class Boundary:
    def __init__(self, app):
        self.app = app
        self.requests = self.failures = 0
        self._lock = Lock()

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.app(scope, receive, send)
        request_id = uuid4().hex
        scope.setdefault("state", {})["request_id"] = request_id
        start, status = monotonic(), 500

        async def respond(message):
            nonlocal status
            if message["type"] == "http.response.start":
                status = message["status"]
                headers = message.setdefault("headers", [])
                headers.extend(((b"x-request-id", request_id.encode()), (b"x-content-type-options", b"nosniff")))
            await send(message)

        try:
            if scope["method"] == "POST":
                raw = bytearray()
                try:
                    with anyio.fail_after(2):
                        while True:
                            part = await receive()
                            if part["type"] == "http.disconnect":
                                return
                            chunk = part.get("body", b"")
                            if len(raw) + len(chunk) > MAX_BODY:
                                return await failure(413, "request_too_large", request_id)(scope, receive, respond)
                            raw.extend(chunk)
                            if not part.get("more_body"):
                                break
                except TimeoutError:
                    return await failure(408, "body_timeout", request_id)(scope, receive, respond)

                async def buffered():
                    return {"type": "http.request", "body": bytes(raw), "more_body": False}

                return await self.app(scope, buffered, respond)
            return await self.app(scope, receive, respond)
        finally:
            with self._lock:
                self.requests += 1
                self.failures += status >= 400
            LOG.info("request", extra={"request_id": request_id, "status_code": status,
                                       "duration_ms": round((monotonic() - start) * 1000, 2)})


def create_app(source=None, ttl=30.0, timeout=2.0):
    if source is None:
        uri = os.getenv("PREREQ_LAB_BOLT")
        marker = os.getenv("PREREQ_LAB_MARKER")
        if bool(uri) != bool(marker):
            raise ValueError("isolated lab URI and marker must be supplied together")
        source = (Neo4jRepository(uri, marker) if uri else
                  FileRepository(Path(os.getenv("PREREQ_SNAPSHOT", ROOT / "testdata" / "catalog.json"))))
    cache = SnapshotCache(source, ttl)
    service = Service(cache, timeout)

    @asynccontextmanager
    async def lifespan(_):
        try:
            yield
        finally:
            await anyio.to_thread.run_sync(source.close)

    api = FastAPI(title=NAME, version="1.0", lifespan=lifespan)
    api.add_middleware(Boundary)
    api.state.cache = cache

    @api.exception_handler(AnalysisError)
    async def domain_error(request: Request, exc: AnalysisError):
        return failure(exc.status, exc.code, request.state.request_id)

    @api.exception_handler(RequestValidationError)
    async def validate_error(request: Request, exc: RequestValidationError):
        fields = [{"position": [str(x) for x in e["loc"]], "type": e["type"]} for e in exc.errors()]
        return failure(422, "invalid_request", request.state.request_id, fields)

    @api.exception_handler(HTTPException)
    async def http_error(request: Request, exc: HTTPException):
        result = failure(exc.status_code, "http_error", request.state.request_id)
        if exc.headers:
            result.headers.update(exc.headers)
        return result

    @api.get("/health/live")
    def live():
        return {"status": "ok"}

    @api.get("/health/ready")
    def ready():
        return service.run("all", lambda a: {"status": "ok", "courses": len(a.snapshot.courses),
                                              "duplicate_relations": a.snapshot.duplicates})

    @api.get("/api/v1/metrics")
    def metrics():
        return {"repository_reads": cache.reads, "cache_hits": cache.hits, "cache_misses": cache.misses, "cache_ttl_seconds": ttl}

    @api.get("/api/v1/courses")
    def search(q: str = Query("", max_length=100), limit: int = Query(40, ge=1, le=100)):
        return service.search(q, limit)

    @api.post("/api/v1/prerequisites/compare")
    def compare(body: Comparison):
        return service.run(body.policy, lambda a: a.compare(body.left, body.right, body.path_limit, body.max_depth))

    @api.post("/api/v1/prerequisites/learning-order")
    def order(body: Constraint):
        return service.run(body.policy, lambda a: a.learning_order(body.course_id))

    @api.post("/api/v1/prerequisites/missing")
    def missing(body: Missing):
        return service.run(body.policy, lambda a: a.missing(body.course_id, body.completed))

    @api.get("/api/v1/prerequisites/{course_id}/dependents")
    def dependents(course_id: CourseID, policy: Policy = "all"):
        return service.run(policy, lambda a: a.dependents(course_id))

    @api.get("/api/v1/prerequisites/{course_id}")
    def detail(course_id: CourseID, policy: Policy = "all",
               path_limit: int = Query(12, ge=0, le=MAX_PATHS), max_depth: int = Query(MAX_DEPTH, ge=1, le=MAX_DEPTH)):
        return service.run(policy, lambda a: a.prerequisites(course_id, path_limit, max_depth))

    def asset(name):
        return FileResponse(ROOT / "web" / name, headers={
            "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
            "Cache-Control": "no-store",
        })

    @api.get("/", include_in_schema=False)
    def index():
        return asset("index.html")

    @api.get("/app.js", include_in_schema=False)
    def javascript():
        return asset("app.js")

    @api.get("/app.css", include_in_schema=False)
    def styles():
        return asset("app.css")

    return api


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8094)
    args = parser.parse_args()
    if not ip_address(args.host).is_loopback or not 0 < args.port < 65536:
        parser.error("a loopback IP and valid port are required")
    LOG.setLevel(logging.INFO)
    # Avoid imposing request-only formatter fields on libraries or Uvicorn.
    handler = logging.StreamHandler()
    handler.setFormatter(logging.Formatter('{"request_id":"%(request_id)s","status":%(status_code)s,"duration_ms":%(duration_ms)s}'))
    LOG.handlers[:] = [handler]
    LOG.propagate = False
    uvicorn.run(create_app(), host=args.host, port=args.port, access_log=False, timeout_keep_alive=3)


if __name__ == "__main__":
    main()
