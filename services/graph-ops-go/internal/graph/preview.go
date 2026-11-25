package graph

import (
	"context"
	"strings"
)

func emptyChanges[T any]() Changes[T] {
	return Changes[T]{[]T{}, []Change[T]{}, []T{}, []T{}}
}

func diff[K, T comparable](ctx context.Context, before, target map[K]T, less func(K, K) int, mayDelete func(K) bool) (Changes[T], error) {
	out := emptyChanges[T]()
	for _, key := range keys(target, less) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		after := target[key]
		if old, ok := before[key]; !ok {
			out.Added = append(out.Added, after)
		} else if old == after {
			out.Unchanged = append(out.Unchanged, after)
		} else {
			out.Modified = append(out.Modified, Change[T]{old, after})
		}
	}
	for _, key := range keys(before, less) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if _, retained := target[key]; !retained && mayDelete(key) {
			out.DeleteCandidates = append(out.DeleteCandidates, before[key])
		}
	}
	return out, ctx.Err()
}

func MakePlan(ctx context.Context, in Preview) (Plan, error) {
	p := Plan{Mode: in.Mode, Courses: emptyChanges[Course](), Relations: emptyChanges[Relation](), Errors: []Issue{}}
	finish := func(issues []Issue) Plan {
		p.Errors = append(p.Errors, issues...)
		p.Statistics.Errors = len(p.Errors)
		p.Statistics.Courses = p.Courses.counts()
		p.Statistics.Relations = p.Relations.counts()
		p.Executable = len(p.Errors) == 0
		return p
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if p.Mode == "" {
		p.Mode = Incremental
	}
	if p.Mode != Incremental && p.Mode != Snapshot {
		return finish([]Issue{issue("invalid_mode", "mode", "use incremental or snapshot")}), nil
	}
	if in.Current == nil || in.Target == nil {
		return finish([]Issue{issue("required_field", "current/target", "both graph objects are required")}), nil
	}
	if p.Mode == Snapshot && (in.Current.Courses == nil || in.Current.Relations == nil ||
		in.Target.Courses == nil || in.Target.Relations == nil) {
		return finish([]Issue{issue("incomplete_snapshot", "current/target", "both snapshots must explicitly declare courses and relations arrays")}), nil
	}
	if (in.Scope != nil && (len(in.Scope.CourseIDs) == 0 || len(in.Scope.CourseIDs) > MaxCourses)) ||
		(p.Mode == Snapshot && in.Scope == nil) {
		return finish([]Issue{issue("invalid_scope", "scope.course_ids", "provide a bounded non-empty course id scope")}), nil
	}
	scope := make(map[string]bool)
	if in.Scope != nil {
		for _, id := range in.Scope.CourseIDs {
			if err := ctx.Err(); err != nil {
				return p, err
			}
			if !validID(id) || scope[id] {
				return finish([]Issue{issue("invalid_scope", "scope.course_ids", "course ids must be non-empty, trimmed and unique")}), nil
			}
			scope[id] = true
		}
	}
	inScope := func(id string) bool { return in.Scope == nil || scope[id] }
	edgeInScope := func(k edgeKey) bool { return inScope(k.source) && inScope(k.target) }
	old, oldIssues, err := index(ctx, *in.Current, "current")
	if err != nil {
		return p, err
	}
	target, targetIssues, err := index(ctx, *in.Target, "target")
	if err != nil {
		return p, err
	}
	if issues := append(oldIssues, targetIssues...); len(issues) != 0 {
		return finish(issues), nil
	}
	for _, id := range keys(target.courses, strings.Compare) {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if !inScope(id) {
			return finish([]Issue{issue("out_of_scope", "target.courses", "a target course is outside the managed scope")}), nil
		}
	}
	for _, k := range keys(target.edges, compareEdge) {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if !edgeInScope(k) {
			return finish([]Issue{issue("out_of_scope", "target.relations", "target relations must have both endpoints inside the managed scope")}), nil
		}
	}
	// A retained edge crossing the scope boundary protects its in-scope endpoint.
	protected := make(map[string]bool)
	for k := range old.edges {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if !edgeInScope(k) {
			protected[k.source], protected[k.target] = true, true
		}
	}
	deleteCourse := func(id string) bool { return p.Mode == Snapshot && inScope(id) && !protected[id] }
	deleteEdge := func(k edgeKey) bool { return p.Mode == Snapshot && edgeInScope(k) }
	p.Courses, err = diff(ctx, old.courses, target.courses, strings.Compare, deleteCourse)
	if err != nil {
		return p, err
	}
	p.Relations, err = diff(ctx, old.edges, target.edges, compareEdge, deleteEdge)
	if err != nil {
		return p, err
	}
	// Validate the projected graph, including retained edges and courses.
	for _, c := range p.Courses.DeleteCandidates {
		delete(old.courses, c.ID)
	}
	for _, r := range p.Relations.DeleteCandidates {
		delete(old.edges, r.key())
	}
	for k, c := range target.courses {
		old.courses[k] = c
	}
	for k, r := range target.edges {
		old.edges[k] = r
	}
	issues, err := integrity(ctx, old, "projected")
	return finish(issues), err
}
