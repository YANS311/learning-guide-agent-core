package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func labMySQL(t *testing.T) *Store {
	t.Helper()
	dsn, mark := os.Getenv("ADVISOR_MYSQL_DSN"), os.Getenv("ADVISOR_LAB_MARKER")
	if dsn == "" && mark == "" {
		t.Skip("isolated MySQL fixture is not configured")
	}
	ctx, cancel := context.WithTimeout(background, 10*time.Second)
	defer cancel()
	s, err := OpenMySQL(ctx, dsn, mark)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMySQLContractAndLease(t *testing.T) {
	a, b := labMySQL(t), labMySQL(t)
	var product string
	if err := a.db.QueryRow("SELECT VERSION()").Scan(&product); err != nil {
		t.Fatal(err)
	}
	t.Log("verified MySQL", product)
	ctx, stop := context.WithTimeout(background, 2*time.Second)
	defer stop()
	bad := strings.Repeat("f", 64)
	if bad == os.Getenv("ADVISOR_LAB_MARKER") {
		bad = strings.Repeat("0", 64)
	}
	if other, err := OpenMySQL(ctx, os.Getenv("ADVISOR_MYSQL_DSN"), bad); err == nil {
		other.Close()
		t.Fatal("wrong database marker accepted")
	}
	owner, token := newStudent(t, a)
	stranger, _ := newStudent(t, b)
	if who, err := b.Owner(background, token); err != nil || who != owner {
		t.Fatal(who, err)
	}
	p, _ := a.Profile(background, owner)
	ch := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		go func(s *Store) { _, e := s.SaveProfile(background, owner, p); ch <- e }(s)
	}
	x, y := <-ch, <-ch
	if !(x == nil && errors.Is(y, Revision) || y == nil && errors.Is(x, Revision)) {
		t.Fatal(x, y)
	}
	req := want(4, "B2")
	req.ProfileRevision = 2
	type enqueued struct {
		r      Run
		replay bool
		err    error
	}
	out := make(chan enqueued, 2)
	for _, s := range []*Store{a, b} {
		go func(s *Store) {
			r, replay, e := s.Enqueue(background, owner, "mysql-same-request", req)
			out <- enqueued{r, replay, e}
		}(s)
	}
	f, g := <-out, <-out
	if f.err != nil || g.err != nil || f.replay == g.replay || f.r.ID != g.r.ID {
		t.Fatal(f, g)
	}
	id := f.r.ID
	if _, e := b.Run(background, stranger, id); !errors.Is(e, Missing) {
		t.Fatal("cross owner query", e)
	}
	r2 := req
	r2.Goal = "other"
	if _, _, e := b.Enqueue(background, owner, "mysql-same-request", r2); !errors.Is(e, Idempotency) {
		t.Fatal("key mismatch", e)
	}
	type claimed struct {
		w work
		e error
	}
	attempts := make(chan claimed, 2)
	go func() { w, e := a.Claim(background, "worker-a"); attempts <- claimed{w, e} }()
	go func() { w, e := b.Claim(background, "worker-b"); attempts <- claimed{w, e} }()
	i, j := <-attempts, <-attempts
	if i.e != nil {
		i, j = j, i
	}
	if i.e != nil || !errors.Is(j.e, Missing) || i.w.ID != id {
		t.Fatal(i, j)
	}
	if err := a.Heartbeat(background, i.w); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE run SET lease_deadline=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := a.Progress(background, id, "should_not_exist", i.w); !errors.Is(err, LostLease) {
		t.Fatal(err)
	}
	if err := a.Finish(background, id, "", &Plan{}, i.w); !errors.Is(err, LostLease) {
		t.Fatal(err)
	}
	if found, err := b.Reap(background); err != nil || !found {
		t.Fatal(found, err)
	}
	if _, err := b.Claim(background, "worker-new"); !errors.Is(err, Missing) {
		t.Fatal("backoff was not applied", err)
	}
	time.Sleep(1150 * time.Millisecond)
	fresh, err := b.Claim(background, "worker-new")
	if err != nil || fresh.ID != id || fresh.Attempt != 2 {
		t.Fatal(fresh, err)
	}
	if err = a.Finish(background, id, "", &Plan{}, i.w); !errors.Is(err, LostLease) {
		t.Fatal("stale writer crossed fence", err)
	}
	if err = b.Finish(background, id, "", &Plan{Status: "REVIEW_REQUIRED"}, fresh); err != nil {
		t.Fatal(err)
	}
	if err = b.Cancel(background, owner, id); !errors.Is(err, Revision) {
		t.Fatal("cancel overwrote terminal", err)
	}
	got, err := a.Run(background, owner, id)
	if err != nil || got.State != "SUCCEEDED" || got.Plan == nil {
		t.Fatal(got, err)
	}
	events, err := b.Events(background, owner, id, 0)
	if err != nil || len(events) != 5 || events[2].Kind != "requeued" || events[4].Kind != "completed" {
		t.Fatal(events, err)
	}
	for k, e := range events {
		if e.Seq != k+1 {
			t.Fatal("event sequence", events)
		}
	}
	var plans int
	if err = a.db.QueryRow("SELECT count(*) FROM plan_version WHERE run=?", id).Scan(&plans); err != nil || plans != 1 {
		t.Fatal(plans, err)
	}
	history, err := b.History(background, owner, 0, 10)
	if err != nil || len(history) != 1 || history[0].ID != id {
		t.Fatal(history, err)
	}
}

func TestMySQLRollbackAndCancellation(t *testing.T) {
	s := labMySQL(t)
	owner, _ := newStudent(t, s)
	r, _, err := s.Enqueue(background, owner, "mysql-rollback", want(3, "B2"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.Claim(background, "rollback-worker")
	if err != nil || claim.ID != r.ID {
		t.Fatal(claim, err)
	}
	t.Cleanup(func() { _ = s.Cancel(background, owner, r.ID) })
	if _, err = s.db.Exec("INSERT INTO message VALUES(?,'assistant','isolated rollback fixture')", r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(background, r.ID, "", &Plan{}, claim); err == nil {
		t.Fatal("duplicate message constraint did not fail")
	}
	got, err := s.Run(background, owner, r.ID)
	if err != nil || got.State != "RUNNING" || got.Plan != nil {
		t.Fatal(got, err)
	}
	ev, err := s.Events(background, owner, r.ID, 0)
	if err != nil || len(ev) != 2 {
		t.Fatal(ev, err)
	}
	if err = s.Cancel(background, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(background, r.ID, "", &Plan{}, claim); !errors.Is(err, LostLease) {
		t.Fatal(err)
	}
	ev, err = s.Events(background, owner, r.ID, 2)
	if err != nil || len(ev) != 1 || ev[0].Kind != "cancelled" {
		t.Fatal(ev, err)
	}
}

type countedGraph struct {
	calls   atomic.Int64
	version atomic.Value
}

func (g *countedGraph) Catalog(ctx context.Context) (Catalog, error) { return fakeGraph{}.Catalog(ctx) }
func (g *countedGraph) Check(ctx context.Context, _ string) (CourseCheck, error) {
	g.calls.Add(1)
	select {
	case <-ctx.Done():
		return CourseCheck{}, ctx.Err()
	case <-time.After(15 * time.Millisecond):
	}
	return CourseCheck{Version: g.version.Load().(string), Required: []string{"A1"}}, nil
}
func TestRedisCacheAndAtomicLimit(t *testing.T) {
	addr := os.Getenv("ADVISOR_REDIS_ADDR")
	if addr == "" {
		t.Skip("isolated Redis fixture is not configured")
	}
	a, err := NewRedisLab(addr, os.Getenv("ADVISOR_REDIS_PASSWORD"), os.Getenv("ADVISOR_LAB_MARKER"), new(Metrics))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewRedisLab(addr, os.Getenv("ADVISOR_REDIS_PASSWORD"), os.Getenv("ADVISOR_LAB_MARKER"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	info, err := a.client.Info(background, "server").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			t.Log(strings.TrimSpace(line))
		}
	}
	id, _ := randomID(8)
	first, second := digest([]byte(id+"first")), digest([]byte(id+"second"))
	source := new(countedGraph)
	source.version.Store(first)
	cache := &CachedGraph{Graph: source, Cache: a, TTL: 2 * time.Second}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := cache.CheckVersion(background, "B2", first)
			if err != nil || len(p.Required) != 1 {
				t.Error(p, err)
			}
		}()
	}
	wg.Wait()
	peer := &CachedGraph{Graph: source, Cache: b}
	if p, err := peer.CheckVersion(background, "B2", first); err != nil || p.Version != first || source.calls.Load() != 1 {
		t.Fatal(p, err, source.calls.Load())
	}
	key := "advisor:p6:prereq:" + Rules + ":" + first + ":B2"
	if err := a.client.Set(background, key, `{"proof":{"version":"`+first+`","required":[]},"mac":"wrong"}`, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	cache.TTL = 15 * time.Millisecond
	if _, err := cache.CheckVersion(background, "B2", first); err != nil || source.calls.Load() != 2 {
		t.Fatal(err, source.calls.Load())
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := cache.CheckVersion(background, "B2", first); err != nil || source.calls.Load() != 3 {
		t.Fatal("no TTL reload", err, source.calls.Load())
	}
	if _, err := cache.CheckVersion(background, "B2", second); !errors.Is(err, Changed) {
		t.Fatal("old proof polluted new snapshot", err)
	}
	source.version.Store(second)
	if p, err := cache.CheckVersion(background, "B2", second); err != nil || p.Version != second {
		t.Fatal(p, err)
	}
	var accepted, limited atomic.Int32
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := a
			if i%2 == 0 {
				r = b
			}
			switch err := r.Admit(background, id); {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, Busy):
				limited.Add(1)
			default:
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 20 || limited.Load() != 4 {
		t.Fatal(accepted.Load(), limited.Load())
	}
}

func TestMySQLHistoryExplain(t *testing.T) {
	if os.Getenv("ADVISOR_INDEX_REPORT") == "" {
		t.Skip("optional 10000-row index experiment")
	}
	s := labMySQL(t)
	owner, _ := newStudent(t, s)
	profile, err := json.Marshal(student())
	if err != nil {
		t.Fatal(err)
	}
	req, err := json.Marshal(want(3, "B2"))
	if err != nil {
		t.Fatal(err)
	}
	for base := 0; base < 10000; base += 200 {
		rows := make([]string, 0, 200)
		args := make([]any, 0, 200*7)
		for i := base; i < base+200; i++ {
			id := owner[:21] + fmt.Sprintf("%011d", i)
			ts := time.Unix(1800000000+int64(i), 0).UTC().Format("2006-01-02T15:04:05.000000000Z")
			rows = append(rows, "(?,?,?,?,?,?,'FAILED',?,1,?)")
			args = append(args, id, owner, id, digest(req), string(profile), string(req), id, ts)
		}
		_, err = s.db.Exec("INSERT INTO run(id,owner,request_key,digest,profile,request,state,plan_id,version,created) VALUES "+strings.Join(rows, ","), args...)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.db.Exec("ANALYZE TABLE run")
	indexed := selectRun + " WHERE r.owner=? ORDER BY r.created DESC,r.id DESC LIMIT 20 OFFSET 9000"
	var truth string
	evidence := map[string]any{"row_count": 10000, "offset": 9000, "page": 20}
	for _, entry := range []struct {
		key, sql string
		args     []any
	}{
		{"indexed", indexed, []any{owner}},
		{"without_history_index", strings.Replace(indexed, "FROM run r", "FROM run r IGNORE INDEX (run_history)", 1), []any{owner}},
		{"covering_then_join", mysqlHistory, []any{owner, 20, 9000}},
	} {
		var explain string
		if err = s.db.QueryRow("EXPLAIN ANALYZE "+entry.sql, entry.args...).Scan(&explain); err != nil {
			t.Fatal(err)
		}
		var times []float64
		for i := 0; i < 33; i++ {
			start := time.Now()
			rows, err := s.db.Query(entry.sql, entry.args...)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for rows.Next() {
				r, e := scanRun(rows)
				if e != nil {
					rows.Close()
					t.Fatal(e)
				}
				ids = append(ids, r.ID)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			order := strings.Join(ids, ",")
			if len(ids) != 20 {
				t.Fatal(len(ids))
			}
			if truth == "" {
				truth = order
			} else if truth != order {
				t.Fatal("pagination changed")
			}
			if i > 2 {
				times = append(times, float64(time.Since(start).Microseconds())/1000)
			}
		}
		sort.Float64s(times)
		evidence[entry.key] = map[string]any{"p50_ms": times[14], "p95_ms": times[28], "plan": explain, "samples_ms": times}
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv("ADVISOR_INDEX_REPORT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
