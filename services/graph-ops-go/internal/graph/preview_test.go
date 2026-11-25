package graph

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

var (
	a  = Course{ID: "X01", Name: "Sample A"}
	b  = Course{ID: "X02", Name: "Sample B"}
	c  = Course{ID: "X03", Name: "Sample C"}
	ab = Relation{Type: Prereq, Source: a.ID, Target: b.ID, Strength: "strong"}
)

func verifyPlan(t *testing.T, in Preview) Plan {
	t.Helper()
	got, err := MakePlan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPlanChanges(t *testing.T) {
	graph := &Data{[]Course{a, b}, []Relation{ab}}
	initial := verifyPlan(t, Preview{Current: &Data{}, Target: graph})
	if !initial.Executable || initial.Statistics.Courses.Added != 2 || initial.Statistics.Relations.Added != 1 {
		t.Fatalf("%+v", initial)
	}
	same := verifyPlan(t, Preview{Current: graph, Target: graph})
	if !same.Executable || same.Statistics.Courses != (Counts{Unchanged: 2}) || same.Statistics.Relations != (Counts{Unchanged: 1}) {
		t.Fatalf("%+v", same)
	}
	updated := a
	updated.Name = "Sample revision"
	edge := ab
	edge.Strength = "weak"
	newEdge := Relation{Type: Prereq, Source: b.ID, Target: c.ID}
	target := &Data{[]Course{updated, c}, []Relation{edge, newEdge}}
	got := verifyPlan(t, Preview{Current: graph, Target: target})
	if !got.Executable || got.Statistics.Courses != (Counts{Added: 1, Modified: 1}) || got.Statistics.Relations != (Counts{Added: 1, Modified: 1}) {
		t.Fatalf("%+v", got)
	}
	if got.Courses.Modified[0] != (Change[Course]{a, updated}) || got.Relations.Modified[0] != (Change[Relation]{ab, edge}) {
		t.Fatalf("changes lose context: %+v", got)
	}
	swapped := &Data{[]Course{c, updated}, []Relation{newEdge, edge}}
	oldSwapped := &Data{[]Course{b, a}, []Relation{ab}}
	if again := verifyPlan(t, Preview{Current: oldSwapped, Target: swapped}); !reflect.DeepEqual(got, again) {
		t.Fatalf("preview is not deterministic")
	}
}

func TestSnapshotScope(t *testing.T) {
	ca := Relation{Type: Prereq, Source: c.ID, Target: a.ID}
	old := &Data{[]Course{a, b, c}, []Relation{ab, ca}}
	empty := &Data{Courses: []Course{}, Relations: []Relation{}}
	incremental := verifyPlan(t, Preview{Current: old, Target: empty})
	if !incremental.Executable || len(incremental.Courses.DeleteCandidates)+len(incremental.Relations.DeleteCandidates) != 0 {
		t.Fatalf("incremental deletion: %+v", incremental)
	}
	result := verifyPlan(t, Preview{Current: old, Target: empty, Mode: Snapshot, Scope: &Scope{[]string{a.ID, b.ID}}})
	if !result.Executable || !reflect.DeepEqual(result.Courses.DeleteCandidates, []Course{b}) || !reflect.DeepEqual(result.Relations.DeleteCandidates, []Relation{ab}) {
		t.Fatalf("scoped deletions: %+v", result)
	}
	for _, in := range []Preview{
		{Current: old, Target: empty, Mode: Snapshot},
		{Current: old, Target: empty, Mode: Snapshot, Scope: &Scope{[]string{a.ID, a.ID}}},
		{Current: old, Target: &Data{Courses: []Course{c}, Relations: []Relation{}}, Mode: Snapshot, Scope: &Scope{[]string{a.ID}}},
		{Current: old, Target: &Data{Relations: []Relation{ab}}, Scope: &Scope{[]string{a.ID}}},
		{Current: old, Target: empty, Mode: "unknown"},
	} {
		if got := verifyPlan(t, in); got.Executable || len(got.Errors) == 0 || len(got.Courses.DeleteCandidates) != 0 {
			t.Fatalf("%+v", got)
		}
	}
	if got := verifyPlan(t, Preview{Current: old, Target: &Data{}, Mode: Snapshot, Scope: &Scope{[]string{a.ID}}}); got.Executable || got.Errors[0].Code != "incomplete_snapshot" {
		t.Fatalf("implicit empty snapshot: %+v", got)
	}
}

func TestInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		data Data
	}{
		{"required_field", Data{Courses: []Course{{ID: "X01"}}}},
		{"invalid_value", Data{Courses: []Course{{ID: "X01", Name: "A", Hours: -1}}}},
		{"duplicate_course", Data{Courses: []Course{a, a}}},
		{"duplicate_name", Data{Courses: []Course{a, {ID: "X02", Name: a.Name}}}},
		{"duplicate_relation", Data{[]Course{a, b}, []Relation{ab, ab}}},
		{"invalid_reference", Data{[]Course{a}, []Relation{ab}}},
		{"unsupported_relation", Data{[]Course{a, b}, []Relation{{Type: "OTHER", Source: a.ID, Target: b.ID}}}},
		{"prerequisite_cycle", Data{[]Course{a, b}, []Relation{ab, {Type: Prereq, Source: b.ID, Target: a.ID}}}},
		{"record_limit", Data{Courses: make([]Course, MaxCourses+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues, err := Validate(context.Background(), tc.data)
			if err != nil || len(issues) == 0 || issues[0].Code != tc.name {
				t.Fatalf("%+v %v", issues, err)
			}
			result := verifyPlan(t, Preview{Current: &Data{}, Target: &tc.data})
			if result.Executable || result.Statistics.Errors != len(result.Errors) || result.Errors[0].Code != tc.name {
				t.Fatalf("%+v", result)
			}
		})
	}
}

func TestProjectedCycleAndCancellation(t *testing.T) {
	old := &Data{[]Course{a, b}, []Relation{ab}}
	result := verifyPlan(t, Preview{Current: old, Target: &Data{Relations: []Relation{{Type: Prereq, Source: b.ID, Target: a.ID}}}})
	if result.Executable || result.Errors[0].Code != "prerequisite_cycle" {
		t.Fatalf("%+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MakePlan(ctx, Preview{Current: old, Target: old}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Validate(ctx, *old); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
