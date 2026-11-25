package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"learning-guide/graph-ops/internal/importer"
	"learning-guide/graph-ops/internal/neoops"
)

const labPreview = "/api/v1/graph/db/preview"
const labExecute = "/api/v1/graph/db/execute"
const labOperation = "/api/v1/graph/db/operation"

func NewWithLab(log *slog.Logger, timeout time.Duration, lab *neoops.Service) *API {
	a := New(log, timeout)
	a.lab = lab
	return a
}

type labMetrics struct {
	ready                                                    bool
	success, failure, conflicts, replays, rollbacks, unknown uint64
	tx                                                       counter
}

func (m labMetrics) render(out io.Writer) {
	ready := 0
	if m.ready {
		ready = 1
	}
	fmt.Fprintf(out, "# TYPE graphops_neo4j_ready gauge\ngraphops_neo4j_ready %d\n", ready)
	fmt.Fprintf(out, "# TYPE graphops_execute_total counter\ngraphops_execute_total{result=\"success\"} %d\ngraphops_execute_total{result=\"failure\"} %d\n", m.success, m.failure)
	fmt.Fprintf(out, "# TYPE graphops_execute_conflicts_total counter\ngraphops_execute_conflicts_total %d\n", m.conflicts)
	fmt.Fprintf(out, "# TYPE graphops_execute_replays_total counter\ngraphops_execute_replays_total %d\n", m.replays)
	fmt.Fprintf(out, "# TYPE graphops_execute_confirmed_rollbacks_total counter\ngraphops_execute_confirmed_rollbacks_total %d\n", m.rollbacks)
	fmt.Fprintf(out, "# TYPE graphops_execute_unknown_outcomes_total counter\ngraphops_execute_unknown_outcomes_total %d\n", m.unknown)
	fmt.Fprintf(out, "# TYPE graphops_neo4j_transaction_duration_seconds summary\ngraphops_neo4j_transaction_duration_seconds_sum %g\ngraphops_neo4j_transaction_duration_seconds_count %d\n", m.tx.seconds, m.tx.count)
}

func labError(err error) response {
	var f *neoops.Fault
	if errors.As(err, &f) {
		if f.Code == "commit_outcome_unknown" || f.Code == "operation_not_observed" {
			return failure(f.Status, f.Code, "database", "committed outcome is not yet known; query again with the same Idempotency-Key")
		}
		return failure(f.Status, f.Code, "database", "graph operation rejected or unavailable")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return canceled(err)
	}
	return failure(503, "neo4j_unavailable", "database", "lab database request could not be completed")
}

func (a *API) labReady(ctx context.Context) response {
	err := a.lab.Ready(ctx)
	a.mu.Lock()
	a.labMetrics.ready = err == nil
	a.mu.Unlock()
	if err != nil {
		return labError(err)
	}
	return response{http.StatusOK, "", map[string]any{"status": "ok", "snapshot_source": "neo4j", "database_connected": true}}
}

func (a *API) processLab(ctx context.Context, r *http.Request, route string, body []byte, log *slog.Logger) response {
	key := r.Header.Get("Idempotency-Key")
	switch route {
	case labPreview:
		var data neoops.Target
		issue := importer.DecodeJSON(ctx, body, &data)
		if ctx.Err() != nil {
			return canceled(ctx.Err())
		}
		if issue != nil {
			return failure(400, issue.Code, issue.Location, issue.Message)
		}
		log.Info("database_preview")
		out, err := a.lab.Preview(ctx, data)
		if err != nil {
			if len(out.Plan.Errors) > 0 {
				return response{422, "invalid_graph_plan", out.Plan}
			}
			return labError(err)
		}
		return response{200, "", out}
	case labExecute:
		var command neoops.Command
		issue := importer.DecodeJSON(ctx, body, &command)
		if ctx.Err() != nil {
			return canceled(ctx.Err())
		}
		if issue != nil {
			return failure(400, issue.Code, issue.Location, issue.Message)
		}
		id, _ := neoops.OperationID(key)
		out, err := a.lab.Execute(ctx, command, key)
		a.mu.Lock()
		if out.TransactionTime > 0 {
			a.labMetrics.tx.count++
			a.labMetrics.tx.seconds += out.TransactionTime.Seconds()
		}
		if out.Replay {
			a.labMetrics.replays++
		}
		if err != nil {
			var f *neoops.Fault
			if errors.As(err, &f) && f.RolledBack {
				a.labMetrics.rollbacks++
			}
			if errors.As(err, &f) && f.Code == "commit_outcome_unknown" {
				a.labMetrics.unknown++
			}
		}
		a.mu.Unlock()
		if err != nil {
			res := labError(err)
			attempt := out.AuditAttempt
			log.Info("sync_outcome", "operation_id", id, "plan_id", attempt.PlanID,
				"request_digest", attempt.RequestHash, "started_at", attempt.Started, "finished_at", attempt.Finished,
				"planned_changes", attempt.Statistics, "outcome", attempt.Status, "status", res.status, "error_code", res.code)
			if id != "" {
				res.body.(map[string]any)["operation_id"] = id
			}
			return res
		}
		log.Info("sync_outcome", "operation_id", out.Operation.ID, "plan_id", out.Operation.PlanID, "status", out.Operation.Status, "replay", out.Replay)
		return response{200, "", out}
	case labOperation:
		out, err := a.lab.Lookup(ctx, key)
		if err != nil {
			return labError(err)
		}
		return response{200, "", out}
	default:
		return failure(404, "not_found", "path", "route not found")
	}
}
