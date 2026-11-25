package advisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var background = context.Background()

func demoStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), dbName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func newStudent(t *testing.T, s *Store) (string, string) {
	t.Helper()
	token, err := s.NewWorkspace(background)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Owner(background, token)
	if err != nil {
		t.Fatal(err)
	}
	p := student()
	p.Revision = 0
	if _, err = s.SaveProfile(background, owner, p); err != nil {
		t.Fatal(err)
	}
	return owner, token
}

func TestVersionConflictAndIdempotency(t *testing.T) {
	s := demoStore(t)
	owner, _ := newStudent(t, s)
	other, _ := newStudent(t, s)
	req := want(3, "B2")
	var wg sync.WaitGroup
	out := make(chan Run, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, err := s.Enqueue(background, owner, "run-key-1111", req)
			if err != nil {
				t.Error(err)
			}
			out <- r
		}()
	}
	wg.Wait()
	close(out)
	var id string
	for run := range out {
		if id != "" && id != run.ID {
			t.Fatal("duplicate run")
		}
		id = run.ID
	}
	if _, err := s.Run(background, other, id); !errors.Is(err, Missing) {
		t.Fatalf("cross-owner read: %v", err)
	}
	if err := s.Cancel(background, other, id); !errors.Is(err, Missing) {
		t.Fatalf("cross-owner cancel: %v", err)
	}
	if _, _, err := s.Enqueue(background, owner, "run-key-1111", want(5, "B2")); !errors.Is(err, Idempotency) {
		t.Fatal(err)
	}
	if _, _, err := s.Enqueue(background, owner, "run-key-busy", want(5, "B2")); !errors.Is(err, Busy) {
		t.Fatal(err)
	}
	if _, err := s.Claim(background); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(background, id, "", &Plan{Status: "REVIEW_REQUIRED"}); err != nil {
		t.Fatal(err)
	}
	profile, _ := s.Profile(background, owner)
	winners := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := s.SaveProfile(background, owner, profile); winners <- err }()
	}
	a, b := <-winners, <-winners
	if !(a == nil && errors.Is(b, Revision) || b == nil && errors.Is(a, Revision)) {
		t.Fatalf("CAS results %v %v", a, b)
	}
	req.ParentRun = id
	req.ProfileRevision = 2
	next, _, err := s.Enqueue(background, owner, "run-key-next", req)
	if err != nil || next.PlanID != id || next.Version != 2 {
		t.Fatal(next, err)
	}
	if _, err = s.Claim(background); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(background, next.ID, "", &Plan{Status: "REVIEW_REQUIRED"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Enqueue(background, owner, "run-key-stale", req); !errors.Is(err, Revision) {
		t.Fatal(err)
	}
	own, err := s.History(background, owner, 0, 1)
	if err != nil || len(own) != 1 || own[0].Version != 2 || own[0].Plan == nil {
		t.Fatal(own, err)
	}
	older, err := s.History(background, owner, 1, 1)
	if err != nil || len(older) != 1 || older[0].Version != 1 {
		t.Fatal(older, err)
	}
}

func TestAtomicRollbackAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbName)
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, token := newStudent(t, s)
	if _, err = OpenStore(path); err == nil {
		t.Fatal("second process lock accepted")
	}
	run, _, err := s.Enqueue(background, owner, "rollback-key", want(3, "B2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(background); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_plan BEFORE INSERT ON plan_version
		BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(background, run.ID, "", &Plan{}); err == nil {
		t.Fatal("expected real SQL insertion failure")
	}
	row, err := s.Run(background, owner, run.ID)
	if err != nil || row.State != "RUNNING" || row.Plan != nil {
		t.Fatal(row, err)
	}
	if _, err = s.db.Exec("DROP TRIGGER reject_plan"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if id, err := s.Owner(background, token); err != nil || id != owner {
		t.Fatal(id, err)
	}
	row, err = s.Run(background, owner, run.ID)
	if err != nil || row.State != "INTERRUPTED" {
		t.Fatal(row, err)
	}
	events, err := s.Events(background, owner, run.ID, 1)
	if err != nil || len(events) != 2 || events[1].Seq != 3 || events[1].Kind != "interrupted" {
		t.Fatal(events, err)
	}
	retry, replay, err := s.Enqueue(background, owner, "rollback-key", want(3, "B2"))
	if err != nil || !replay || retry.ID != run.ID || retry.State != "INTERRUPTED" {
		t.Fatal(retry, replay, err)
	}
}

func TestUnknownDatabaseIsNeverChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbName)
	content := strings.Repeat("a", 80)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("foreign database accepted")
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != content {
		t.Fatal("foreign database was modified")
	}
}
