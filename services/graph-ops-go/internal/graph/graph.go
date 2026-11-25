package graph

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
)

const (
	MaxCourses   = 10000
	MaxRelations = 20000
	Prereq       = "PREREQ"
	Incremental  = "incremental"
	Snapshot     = "snapshot"
)

type Course struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Category    string  `json:"category,omitempty"`
	Semester    string  `json:"semester,omitempty"`
	Credits     float64 `json:"credits,omitempty"`
	Hours       int     `json:"hours,omitempty"`
	Layer       string  `json:"layer,omitempty"`
	Description string  `json:"description,omitempty"`
}

type Relation struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Reason   string `json:"reason,omitempty"`
	Strength string `json:"strength,omitempty"`
}

type Data struct {
	Courses   []Course   `json:"courses"`
	Relations []Relation `json:"relations"`
}

type Issue struct {
	Code     string `json:"code"`
	Location string `json:"location"`
	Message  string `json:"message"`
}

type Scope struct {
	CourseIDs []string `json:"course_ids"`
}

type Preview struct {
	Current *Data  `json:"current"`
	Target  *Data  `json:"target"`
	Mode    string `json:"mode"`
	Scope   *Scope `json:"scope,omitempty"`
}

type Change[T any] struct {
	Before T `json:"before"`
	After  T `json:"after"`
}

type Changes[T any] struct {
	Added            []T         `json:"added"`
	Modified         []Change[T] `json:"modified"`
	DeleteCandidates []T         `json:"delete_candidates"`
	Unchanged        []T         `json:"unchanged"`
}

type Counts struct {
	Added            int `json:"added"`
	Modified         int `json:"modified"`
	DeleteCandidates int `json:"delete_candidates"`
	Unchanged        int `json:"unchanged"`
}

func (c Changes[T]) counts() Counts {
	return Counts{len(c.Added), len(c.Modified), len(c.DeleteCandidates), len(c.Unchanged)}
}

type Plan struct {
	Mode       string            `json:"mode"`
	Courses    Changes[Course]   `json:"courses"`
	Relations  Changes[Relation] `json:"relations"`
	Statistics struct {
		Courses   Counts `json:"courses"`
		Relations Counts `json:"relations"`
		Errors    int    `json:"errors"`
	} `json:"statistics"`
	Errors     []Issue `json:"errors"`
	Executable bool    `json:"executable"`
}

type edgeKey struct{ kind, source, target string }

func (r Relation) key() edgeKey { return edgeKey{r.Type, r.Source, r.Target} }

type indexed struct {
	courses map[string]Course
	edges   map[edgeKey]Relation
}

func issue(code, place, message string) Issue { return Issue{code, place, message} }

func validID(s string) bool { return s != "" && strings.TrimSpace(s) == s }

func index(ctx context.Context, d Data, prefix string) (indexed, []Issue, error) {
	result := indexed{make(map[string]Course), make(map[edgeKey]Relation)}
	var issues []Issue
	if err := ctx.Err(); err != nil {
		return result, nil, err
	}
	if len(d.Courses) > MaxCourses || len(d.Relations) > MaxRelations {
		return result, []Issue{issue("record_limit", prefix, "too many courses or relations")}, nil
	}
	for i, c := range d.Courses {
		if err := ctx.Err(); err != nil {
			return result, nil, err
		}
		p := fmt.Sprintf("%s.courses[%d]", prefix, i)
		switch {
		case !validID(c.ID), strings.TrimSpace(c.Name) == "":
			issues = append(issues, issue("required_field", p, "a trimmed id and a non-empty name are required"))
		case c.Credits < 0 || math.IsNaN(c.Credits) || math.IsInf(c.Credits, 0) || c.Hours < 0:
			issues = append(issues, issue("invalid_value", p, "credits and hours must be non-negative finite numbers"))
		default:
			if _, exists := result.courses[c.ID]; exists {
				issues = append(issues, issue("duplicate_course", p+".id", "course id is repeated"))
			} else {
				result.courses[c.ID] = c
			}
		}
	}
	for i, r := range d.Relations {
		if err := ctx.Err(); err != nil {
			return result, nil, err
		}
		p := fmt.Sprintf("%s.relations[%d]", prefix, i)
		if r.Strength == "" {
			r.Strength = "strong"
		}
		switch {
		case !validID(r.Source), !validID(r.Target), r.Type == "":
			issues = append(issues, issue("required_field", p, "type and trimmed source and target ids are required"))
		case r.Type != Prereq:
			issues = append(issues, issue("unsupported_relation", p+".type", "only PREREQ is supported"))
		case r.Strength != "strong" && r.Strength != "weak":
			issues = append(issues, issue("invalid_value", p+".strength", "must be strong or weak"))
		default:
			if _, exists := result.edges[r.key()]; exists {
				issues = append(issues, issue("duplicate_relation", p, "relation identity is repeated"))
			} else {
				result.edges[r.key()] = r
			}
		}
	}
	return result, issues, nil
}

func compareEdge(a, b edgeKey) int {
	if n := strings.Compare(a.kind, b.kind); n != 0 {
		return n
	}
	if n := strings.Compare(a.source, b.source); n != 0 {
		return n
	}
	return strings.Compare(a.target, b.target)
}

func keys[K comparable, V any](m map[K]V, cmp func(K, K) int) []K {
	result := make([]K, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	slices.SortFunc(result, cmp)
	return result
}

func integrity(ctx context.Context, x indexed, prefix string) ([]Issue, error) {
	var issues []Issue
	names := make(map[string]bool, len(x.courses))
	degrees := make(map[string]int, len(x.courses))
	next := make(map[string][]string, len(x.courses))
	for _, id := range keys(x.courses, strings.Compare) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c := x.courses[id]
		if names[c.Name] {
			issues = append(issues, issue("duplicate_name", prefix+".courses", "course names must be unique"))
		}
		names[c.Name] = true
		degrees[id] = 0
	}
	for _, k := range keys(x.edges, compareEdge) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, sourceExists := x.courses[k.source]
		_, targetExists := x.courses[k.target]
		if !sourceExists || !targetExists {
			issues = append(issues, issue("invalid_reference", fmt.Sprintf("%s.relations[%q,%q]", prefix, k.source, k.target), "relation endpoints must exist"))
			continue
		}
		next[k.source] = append(next[k.source], k.target)
		degrees[k.target]++
	}
	queue := make([]string, 0, len(degrees))
	for id, degree := range degrees {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	for i := 0; i < len(queue); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, id := range next[queue[i]] {
			degrees[id]--
			if degrees[id] == 0 {
				queue = append(queue, id)
			}
		}
	}
	if len(queue) != len(degrees) {
		issues = append(issues, issue("prerequisite_cycle", prefix+".relations", "PREREQ relations contain a cycle"))
	}
	return issues, ctx.Err()
}

func Validate(ctx context.Context, data Data) ([]Issue, error) {
	x, issues, err := index(ctx, data, "data")
	if err != nil || len(issues) != 0 {
		return issues, err
	}
	return integrity(ctx, x, "data")
}
