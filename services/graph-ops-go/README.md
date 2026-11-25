# Graph Ops Phase 1

This loopback-only Go service validates an import and compares a supplied course/prerequisite snapshot with a target. It does not connect to a database, execute changes, or expose a public listener. All fixtures are synthetic.

```sh
go run ./cmd/server -listen 127.0.0.1:8090
```

From another terminal in the module:

```sh
curl -fsS http://127.0.0.1:8090/health/ready
curl -fsS -H 'Content-Type: text/csv' --data-binary @testdata/import.csv \
  http://127.0.0.1:8090/api/v1/graph/import/validate
curl -fsS -H 'Content-Type: application/json' --data-binary @testdata/preview.json \
  http://127.0.0.1:8090/api/v1/graph/sync/preview
curl -fsS http://127.0.0.1:8090/metrics
```

On Windows PowerShell, use `curl.exe` and quote each `@testdata/...` argument. The example adds one course and one relation and changes one course. Validate accepts a standalone JSON graph object or CSV with the exact header shown in the fixture. CSV course rows use the course columns, relation rows use the relationship columns, and the other fields must be empty. Preview accepts the JSON envelope in the fixture. Both graph objects contain `courses` and `relations` arrays. Each JSON response has an `X-Request-ID` header.

## Graph and preview contract

- A course uses its trimmed string ID as identity, with a non-empty unique name and optional category, semester, credits, hours, layer and description. Numbers are nonnegative. Relationship identity is `(type, source ID, target ID)`. The supported relation is directed `PREREQ`, with optional reason and strength (`strong` by default or `weak`). Inputs must map course-name endpoints to stable IDs before calling the service. Neo4j internal IDs are unnecessary.
- Default `incremental` mode omits deletions. Relationship endpoints may be found in the current courses or target courses. Current records absent from the target are retained and are excluded from the unchanged counters, which count only matching target records.
- `snapshot` needs explicit current and target arrays and a non-empty `scope.course_ids` list. A relation is managed only when both endpoints are in scope. An existing cross-scope relation prevents deletion candidacy of its course endpoints. Only pass a complete snapshot of the managed slice, including cross-scope prerequisites and their endpoint courses. Relations of other types are outside Phase 1 coverage.
- The plan includes before/after pairs, sorted by stable identity, with statistics. `executable` means the supplied projected course/prerequisite graph passed Phase 1 validation. There is no executor, proof of live snapshot freshness, or guarantee that a course has no relationship of an unsupported type.
- Body limit: 2 MiB. Each graph: 10,000 courses and 20,000 relations. Deadline: 5 s per request. Invalid format/types use HTTP 400, invalid graph/scope HTTP 422, oversize HTTP 413, unsupported media HTTP 415, and cancellation/deadline HTTP 408 when the connection can still receive a response.
- Logs record fixed route, stage, generated request ID, status and error category. Metrics use fixed route and result labels, and provide count and sum for latency. Readiness is local; there is no downstream connection.

```sh
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go test -cover ./...
go build -o /tmp/graph-ops ./cmd/server
python tests/contract.py /tmp/graph-ops
```
