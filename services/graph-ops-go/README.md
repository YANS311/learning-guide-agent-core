# Graph Ops

The [Phase 3 console guide](CONSOLE.md) covers authentication, browser workflows, APIs and recovery.

In the default mode, this loopback-only Go service validates an import and compares a supplied course/prerequisite snapshot with a target. Database writes are available only in the opt-in isolated Phase 2 lab. All fixtures are synthetic.

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

## Phase 2 isolated Neo4j lab

The original preview endpoint still accepts caller-provided snapshots. The Neo4j lab also
requires `GRAPHOPS_ADMIN_TOKEN` in addition to `GRAPHOPS_LAB_BOLT`,
`GRAPHOPS_LAB_MARKER`, and `GRAPHOPS_LAB_PLAN_KEY`. Use a fresh Neo4j Community
5.26 instance with database `graphops-phase2-test`, no external writers and a
non-default Bolt port bound to a loopback IP. The runtime does not create schema
or enable writes on a general database. The isolated fixture provisions four uniqueness
constraints and a matching marker, after checking the database name and proving it is empty.

| Method | Route | Request |
|---|---|---|
| POST | `/api/v1/graph/db/preview` | `{"scope":{"course_ids":["LAB-EXAMPLE"]},"target":{"courses":[{"id":"LAB-EXAMPLE","name":"Synthetic example"}],"relations":[]}}` |
| POST | `/api/v1/graph/db/execute` | `{"plan_token":"<preview token>","target":{...}}` and a unique random `Idempotency-Key` header |
| GET | `/api/v1/graph/db/operation` | Same `Idempotency-Key` header |

Only incremental upserts are allowed. Scope must explicitly list managed `LAB-*`
course IDs. Both relation endpoints must be in scope. Course and relationship records
describe complete desired values for owned fields. If a managed optional field is
omitted, its Go default is used. Existing nodes and edges must carry the lab's
ownership marker. Other labels, fields and relationship types are left intact.

A preview token expires after three minutes. It binds the scope, canonical target digest,
database epoch, snapshot digest and change counts. Go writers share one Neo4j lock node
and read the version under that lock. The commit and successful execution record are
atomic in Neo4j. Keep the same signing key across lab processes. If a commit response
is ambiguous, look up by the same key. A 404 means no committed result has been
observed. When retrying use the identical command and key. A known rejected or
rolled-back attempt appears in structured logs and has no committed operation record.

This coordination does not serialize clients that bypass the lock. The tested configuration
is a disposable lab with locally disabled Neo4j authentication. Do not expose it through
public listeners, proxy it, or point existing import tools at it.

### Reproduce the isolated tests

Example with Docker, from the module directory on a trusted developer machine.
The readiness query must succeed before launching tests. Run the integration suite
only once per fresh container. The ordinary suite requires no Neo4j.

```sh
docker run -d --rm --name graphops-phase2-lab -p 127.0.0.1:27687:7687 \
  -e NEO4J_AUTH=none -e NEO4J_initial_dbms_default__database=graphops-phase2-test \
  neo4j:5.26.18-community
# Wait until this command returns 1.
docker exec graphops-phase2-lab cypher-shell -d graphops-phase2-test 'RETURN 1'
export GRAPHOPS_LAB_BOLT=bolt://127.0.0.1:27687
export GRAPHOPS_LAB_MARKER="$(openssl rand -hex 32)"
export GRAPHOPS_LAB_PLAN_KEY="$(openssl rand -hex 32)"
export GRAPHOPS_ADMIN_TOKEN="$(openssl rand -hex 32)"
GRAPHOPS_INTEGRATION=1 go test -race -v -count=1 ./internal/neoops -run '^TestNeo4jIntegration$'
go build -o /tmp/graph-ops ./cmd/server
python tests/lab_contract.py /tmp/graph-ops
docker stop graphops-phase2-lab
```

The integration suite asserts stored nodes, relationships, foreign data, and operation
records with real Cypher queries. No school or deployment data is used.
