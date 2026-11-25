package neoops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"learning-guide/graph-ops/internal/graph"
)

func labQuery(t *testing.T, r *Repository, q string, params map[string]any) []*neo4j.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s := r.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: Database})
	defer cleanup(s.Close)
	res, err := s.Run(ctx, q, params)
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func count(t *testing.T, r *Repository, q string) int64 {
	t.Helper()
	rows := labQuery(t, r, q, nil)
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	return rows[0].Values[0].(int64)
}

func fixture(t *testing.T) (*Repository, *Service) {
	t.Helper()
	r, err := Open(os.Getenv("GRAPHOPS_LAB_BOLT"), os.Getenv("GRAPHOPS_LAB_MARKER"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup(r.Close) })
	s, err := New(r, os.Getenv("GRAPHOPS_LAB_PLAN_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	return r, s
}

func freshLab(t *testing.T, r *Repository, incomplete func()) {
	t.Helper()
	db := labQuery(t, r, "CALL db.info() YIELD name RETURN name", nil)
	if len(db) != 1 || db[0].Values[0] != Database || count(t, r, "MATCH (n) RETURN count(n)") != 0 || len(labQuery(t, r, "SHOW CONSTRAINTS", nil)) != 0 {
		t.Fatal("refusing setup: expected a dedicated empty lab database with no constraints")
	}
	labQuery(t, r, "CREATE (:GraphOpsLab {id:'phase2', epoch:0, marker:$marker})", map[string]any{"marker": r.marker})
	incomplete()
	for _, q := range []string{
		"CREATE CONSTRAINT graphops_course_id FOR (n:Course) REQUIRE n.id IS UNIQUE",
		"CREATE CONSTRAINT graphops_course_name FOR (n:Course) REQUIRE n.name IS UNIQUE",
		"CREATE CONSTRAINT graphops_lab_id FOR (n:GraphOpsLab) REQUIRE n.id IS UNIQUE",
		"CREATE CONSTRAINT graphops_operation_id FOR (n:GraphOpsOperation) REQUIRE n.id IS UNIQUE",
	} {
		labQuery(t, r, q, nil)
	}
}

func previewFor(t *testing.T, s *Service, data graph.Data, ids ...string) Command {
	t.Helper()
	pr, err := s.Preview(context.Background(), Target{Scope: graph.Scope{CourseIDs: ids}, Target: &data})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Token == "" || pr.Claims.ID == "" || !pr.Plan.Executable {
		t.Fatalf("invalid preview: %+v", pr)
	}
	return Command{pr.Token, &data}
}

func code(t *testing.T, err error, want string) {
	t.Helper()
	var f *Fault
	if !errors.As(err, &f) || f.Code != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}

func course(s string) graph.Course {
	return graph.Course{ID: "LAB-" + s, Name: "Synthetic " + s, Credits: 1}
}

func TestNeo4jIntegration(t *testing.T) {
	if os.Getenv("GRAPHOPS_INTEGRATION") != "1" {
		t.Skip("requires a fresh dedicated Neo4j lab")
	}
	ctx := context.Background()
	r, s := fixture(t)
	r2, s2 := fixture(t)
	freshLab(t, r, func() {
		t.Run("schema_admission", func(t *testing.T) {
			c := course("NO-SCHEMA")
			cmd := previewFor(t, s, graph.Data{Courses: []graph.Course{c}}, c.ID)
			_, err := s.Execute(ctx, cmd, "synthetic-schema-check")
			code(t, err, "lab_constraints_missing")
			if count(t, r, "MATCH (n:Course) RETURN count(n)") != 0 {
				t.Fatal("write before schema verification")
			}
			wrong, err := Open(os.Getenv("GRAPHOPS_LAB_BOLT"), strings.Repeat("aa", 32))
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup(wrong.Close)
			code(t, wrong.Ready(ctx), "lab_identity_unverified")
		})
	})
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := course("A"), course("B")
	edge := graph.Relation{Type: graph.Prereq, Source: a.ID, Target: b.ID}
	var first Command
	const originalKey = "synthetic-original-op"
	t.Run("empty_graph", func(t *testing.T) {
		if count(t, r, "MATCH (n:Course) RETURN count(n)") != 0 {
			t.Fatal("expected empty graph")
		}
		first = previewFor(t, s, graph.Data{Courses: []graph.Course{a, b}, Relations: []graph.Relation{edge}}, a.ID, b.ID)
		out, err := s.Execute(ctx, first, originalKey)
		if err != nil || out.Replay || out.Operation.Statistics.Courses.Added != 2 || out.Operation.Statistics.Relations.Added != 1 {
			t.Fatalf("%+v %v", out, err)
		}
		if count(t, r, "MATCH (n:Course) RETURN count(n)") != 2 || count(t, r, "MATCH ()-[r:PREREQ]->() RETURN count(r)") != 1 ||
			count(t, r, "MATCH (o:GraphOpsOperation) RETURN count(o)") != 1 {
			t.Fatal("wrong committed state")
		}
	})
	t.Run("replay_and_conflicting_key", func(t *testing.T) {
		swapped := first
		swapped.Target = &graph.Data{Courses: []graph.Course{b, a}, Relations: []graph.Relation{edge}}
		out, err := s2.Execute(ctx, swapped, originalKey)
		if err != nil || !out.Replay {
			t.Fatalf("%+v %v", out, err)
		}
		if count(t, r, "MATCH (m:GraphOpsLab) RETURN m.epoch") != 1 || count(t, r, "MATCH (o:GraphOpsOperation) RETURN count(o)") != 1 {
			t.Fatal("replay changed the database")
		}
		changed := first
		changed.Target = &graph.Data{Courses: []graph.Course{a}}
		_, err = s.Execute(ctx, changed, originalKey)
		code(t, err, "idempotency_conflict")
	})
	t.Run("preserve_unmanaged_attributes_and_relations", func(t *testing.T) {
		labQuery(t, r, `MATCH (a:Course {id:'LAB-A'})-[r:PREREQ]->(:Course {id:'LAB-B'})
SET a:OtherLabel, a.external='retained', r.external='retained'
CREATE (a)-[:SUPPORTS]->(:Indicator {code:'SYN-1'})`, nil)
		a.Credits = 2
		edge.Strength = "weak"
		target := graph.Data{Courses: []graph.Course{a}, Relations: []graph.Relation{edge}}
		out, err := s.Execute(ctx, previewFor(t, s, target, a.ID, b.ID), "synthetic-modification")
		if err != nil || out.Operation.Statistics.Courses.Modified != 1 || out.Operation.Statistics.Relations.Modified != 1 {
			t.Fatalf("%+v %v", out, err)
		}
		if count(t, r, `MATCH (a:Course:OtherLabel {id:'LAB-A',external:'retained',credits:2})-[r:PREREQ {strength:'weak',external:'retained'}]->(:Course {id:'LAB-B'})
MATCH (a)-[:SUPPORTS]->(:Indicator {code:'SYN-1'}) RETURN count(r)`) != 1 {
			t.Fatal("existing data was overwritten")
		}
	})
	t.Run("concurrent_two_clients", func(t *testing.T) {
		c, d := course("CONCURRENT-C"), course("CONCURRENT-D")
		rel := graph.Relation{Type: graph.Prereq, Source: c.ID, Target: d.ID}
		cmd := previewFor(t, s, graph.Data{Courses: []graph.Course{c, d}, Relations: []graph.Relation{rel}}, c.ID, d.ID)
		var wg sync.WaitGroup
		outcomes := make(chan Execution, 2)
		errs := make(chan error, 2)
		for _, client := range []*Service{s, s2} {
			wg.Add(1)
			go func(client *Service) {
				defer wg.Done()
				out, err := client.Execute(ctx, cmd, "synthetic-concurrent")
				outcomes <- out
				errs <- err
			}(client)
		}
		wg.Wait()
		close(outcomes)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		replays := 0
		for out := range outcomes {
			if out.Replay {
				replays++
			}
		}
		if replays != 1 || count(t, r, "MATCH (:Course {id:'LAB-CONCURRENT-C'})-[r:PREREQ]->(:Course {id:'LAB-CONCURRENT-D'}) RETURN count(r)") != 1 {
			t.Fatal("concurrent replay was not deduplicated")
		}
	})
	t.Run("out_of_band_snapshot_drift", func(t *testing.T) {
		c := course("STALE")
		cmd := previewFor(t, s, graph.Data{Courses: []graph.Course{c}}, c.ID)
		labQuery(t, r, "MATCH (a:Course {id:'LAB-A'}) SET a.credits=3", nil)
		_, err := s.Execute(ctx, cmd, "synthetic-stale-key")
		code(t, err, "database_state_changed")
		if count(t, r, "MATCH (n:Course {id:'LAB-STALE'}) RETURN count(n)") != 0 {
			t.Fatal("stale plan wrote data")
		}
	})
	t.Run("rollback_after_real_course_write", func(t *testing.T) {
		c := course("ROLLBACK")
		cmd := previewFor(t, s, graph.Data{Courses: []graph.Course{c}}, c.ID)
		r.afterCourses = func(ctx context.Context, tx neo4j.ExplicitTransaction) error {
			rows, err := records(ctx, tx, "MATCH (n:Course {id:'LAB-ROLLBACK'}) RETURN count(n) AS n", nil)
			if err != nil || len(rows) != 1 || rows[0].Values[0] != int64(1) {
				return fail(500, "injection_precondition_failed")
			}
			return fail(503, "injected_mid_transaction_failure")
		}
		defer func() { r.afterCourses = nil }()
		result, err := s.Execute(ctx, cmd, "synthetic-rollback-key")
		code(t, err, "injected_mid_transaction_failure")
		if result.AuditAttempt.Status != "rolled_back" || result.AuditAttempt.RequestHash == "" {
			t.Fatalf("missing rollback audit: %+v", result.AuditAttempt)
		}
		var f *Fault
		if !errors.As(err, &f) || !f.RolledBack {
			t.Fatal("rollback was not confirmed")
		}
		if count(t, r, "MATCH (n:Course {id:'LAB-ROLLBACK'}) RETURN count(n)") != 0 {
			t.Fatal("partial course write survived rollback")
		}
		_, err = s2.Lookup(ctx, "synthetic-rollback-key")
		code(t, err, "operation_not_observed")
	})
	t.Run("scope_and_dangling_endpoints", func(t *testing.T) {
		o := course("OUTSIDE")
		_, err := s.Preview(ctx, Target{Scope: graph.Scope{CourseIDs: []string{a.ID}}, Target: &graph.Data{Courses: []graph.Course{o}}})
		code(t, err, "invalid_graph_plan")
		labQuery(t, r, "CREATE (:Course {id:'LAB-UNMANAGED',name:'Synthetic unmanaged'})", nil)
		_, err = s.Preview(ctx, Target{Scope: graph.Scope{CourseIDs: []string{"LAB-UNMANAGED"}}, Target: &graph.Data{}})
		code(t, err, "outside_managed_scope")
		bad := graph.Relation{Type: graph.Prereq, Source: a.ID, Target: o.ID}
		_, err = s.Preview(ctx, Target{Scope: graph.Scope{CourseIDs: []string{a.ID, o.ID}}, Target: &graph.Data{Relations: []graph.Relation{bad}}})
		code(t, err, "invalid_graph_plan")
		if count(t, r, "MATCH (n:Course {id:'LAB-OUTSIDE'}) RETURN count(n)") != 0 {
			t.Fatal("invalid target was written")
		}
	})
	t.Run("deletion_and_expiry_rejected", func(t *testing.T) {
		c := course("EXPIRY")
		cmd := previewFor(t, s, graph.Data{Courses: []graph.Course{c}}, c.ID)
		forged := cmd
		forged.PlanToken += "x"
		_, err := s.Execute(ctx, forged, "synthetic-bad-token")
		code(t, err, "invalid_plan_token")
		token, err := s.decode(cmd.PlanToken)
		if err != nil {
			t.Fatal(err)
		}
		token.Statistics.Courses.DeleteCandidates = 1
		cmd.PlanToken = encodeForTest(t, s, token)
		_, err = s.Execute(ctx, cmd, "synthetic-delete-key")
		code(t, err, "deletion_not_supported")
		token.Statistics.Courses.DeleteCandidates = 0
		token.Expires = time.Now().Add(-time.Second)
		cmd.PlanToken = encodeForTest(t, s, token)
		_, err = s.Execute(ctx, cmd, "synthetic-expired-key")
		code(t, err, "plan_expired")
		if count(t, r, "MATCH (n:Course {id:'LAB-EXPIRY'}) RETURN count(n)") != 0 {
			t.Fatal("invalid plan wrote data")
		}
	})
	t.Run("blocked_transaction_deadline_releases_session", func(t *testing.T) {
		c := course("TIMEOUT")
		cmd := previewFor(t, s, graph.Data{Courses: []graph.Course{c}}, c.ID)
		lockSession := r2.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: Database})
		defer cleanup(lockSession.Close)
		lock, err := lockSession.BeginTransaction(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup(lock.Close)
		if _, err = r2.epoch(ctx, lock, true); err != nil {
			t.Fatal(err)
		}
		sub, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		_, err = s.Execute(sub, cmd, "synthetic-timeout-key")
		if err == nil {
			t.Fatal("blocked transaction should not commit")
		}
		if err = lock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if count(t, r, "MATCH (n:Course {id:'LAB-TIMEOUT'}) RETURN count(n)") != 0 {
			t.Fatal("timed-out write survived")
		}
		if _, err = s.Execute(ctx, cmd, "synthetic-timeout-key"); err != nil {
			t.Fatal("same-key retry after rollback failed:", err)
		}
	})
	t.Run("restart_and_lookup", func(t *testing.T) {
		_, restarted := fixture(t)
		op, err := restarted.Lookup(ctx, originalKey)
		if err != nil || op.Status != "committed" || op.Statistics.Courses.Added != 2 {
			t.Fatalf("%+v %v", op, err)
		}
	})
	t.Run("commit_ack_loss_emulated_after_real_commit", func(t *testing.T) {
		c := course("ACK-LOSS")
		cmd := previewFor(t, s, graph.Data{Courses: []graph.Course{c}}, c.ID)
		r.commitAck = func(ctx context.Context, tx neo4j.ExplicitTransaction) error {
			if err := tx.Commit(ctx); err != nil {
				t.Error("real commit error:", err)
				return err
			}
			return errors.New("synthetic missing acknowledgement after commit")
		}
		defer func() { r.commitAck = nil }()
		const key = "synthetic-ack-loss-key"
		_, err := s.Execute(ctx, cmd, key)
		code(t, err, "commit_outcome_unknown")
		if count(t, r, "MATCH (:Course {id:'LAB-ACK-LOSS'}) RETURN count(*)") != 1 {
			t.Fatal("committed course missing")
		}
		record, err := s2.Lookup(ctx, key)
		if err != nil || record.Status != "committed" {
			t.Fatalf("%+v %v", record, err)
		}
		again, err := s2.Execute(ctx, cmd, key)
		if err != nil || !again.Replay || again.Operation.ID != record.ID {
			t.Fatalf("%+v %v", again, err)
		}
	})
	t.Run("committed_history_and_detail", func(t *testing.T) {
		head, err := s.History(ctx, 2, "", "committed")
		if err != nil || len(head.Items) != 2 || head.NextCursor == "" || head.Items[0].Finished.Before(head.Items[1].Finished) {
			t.Fatalf("%+v %v", head, err)
		}
		next, err := s.History(ctx, 2, head.NextCursor, "committed")
		if err != nil || len(next.Items) != 2 || next.Items[0].ID == head.Items[1].ID {
			t.Fatalf("%+v %v", next, err)
		}
		if _, err := s.History(ctx, 2, "", "failed"); err == nil {
			t.Fatal("unsupported status accepted")
		}
		got, err := s.Detail(ctx, head.Items[0].ID)
		if err != nil || got.ID != head.Items[0].ID {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Logf("actual Neo4j state: courses=%d prereq=%d operations=%d supports=%d",
		count(t, r, "MATCH (n:Course) RETURN count(n)"), count(t, r, "MATCH ()-[r:PREREQ]->() RETURN count(r)"),
		count(t, r, "MATCH (n:GraphOpsOperation) RETURN count(n)"), count(t, r, "MATCH ()-[r:SUPPORTS]->() RETURN count(r)"))
}

func encodeForTest(t *testing.T, s *Service, c claims) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(s.sign(b))
}

func TestUnavailableAndIsolationGuards(t *testing.T) {
	marker := strings.Repeat("12", 32)
	for _, uri := range []string{"bolt://192.0.2.7:27687", "bolt://127.0.0.1:7687", "neo4j://127.0.0.1:27687", "bolt://127.0.0.1:27687?x=y"} {
		if _, err := Open(uri, marker); err == nil {
			t.Fatalf("accepted %q", uri)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	r, err := Open("bolt://"+addr, marker)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(r.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Ready(ctx); err == nil {
		t.Fatal("expected unavailable dependency")
	}
}

func TestBrowserDatabaseState(t *testing.T) {
	if os.Getenv("GRAPHOPS_E2E_ASSERT") != "1" {
		t.Skip("run after the authenticated browser workflow against the isolated lab")
	}
	r, s := fixture(t)
	if err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"MATCH (:Course {id:'LAB-E2E-A'})-[r:PREREQ]->(:Course {id:'LAB-E2E-B'}) RETURN count(r)",
		"MATCH (c:Course {id:'LAB-E2E-LOST'}) RETURN count(c)",
		"MATCH (c:Course {id:'LAB-E2E-DRIFT'}) RETURN count(c)",
	} {
		if count(t, r, q) != 1 {
			t.Fatalf("expected exactly one committed row for %s", q)
		}
	}
	if count(t, r, "MATCH (c:Course) WHERE c.id IN ['LAB-E2E-STALE','LAB-E2E-EXPIRED'] RETURN count(c)") != 0 {
		t.Fatal("rejected course was written")
	}
	if count(t, r, "MATCH (o:GraphOpsOperation) RETURN count(o)") != 9 {
		t.Fatal("unexpected committed operation count")
	}
	t.Logf("browser final Neo4j state: courses=%d, prereq=%d, operations=9",
		count(t, r, "MATCH (n:Course) RETURN count(n)"), count(t, r, "MATCH ()-[r:PREREQ]->() RETURN count(r)"))
}
