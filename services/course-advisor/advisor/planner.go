package advisor

import (
	"context"
	"sort"
	"strings"
)

type Graph interface {
	Catalog(context.Context) (Catalog, error)
	Check(context.Context, string) (CourseCheck, error)
}
type EvidenceSource interface {
	Find(context.Context, string) ([]Evidence, string, error)
}
type Narrator interface {
	Explain(context.Context, []Course) (string, *int, error)
}

type Planner struct {
	Graph            Graph
	Evidence         EvidenceSource
	Narrator         Narrator
	EvidenceSnapshot string
}

func score(c Course, terms ...string) int {
	title := strings.ToLower(c.Name)
	total := 0
	for n, raw := range terms {
		parts := strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
			return strings.ContainsRune(" ,，、；;/\t\n", r)
		})
		seen := map[string]bool{}
		for _, part := range parts {
			if part != "" && !seen[part] && strings.Contains(title, part) {
				total += 3 - n
			}
			seen[part] = true
		}
	}
	return total
}

func (p Planner) Run(ctx context.Context, profile Profile, req Request, progress func(string) error) (Plan, error) {
	plan := Plan{RuleVersion: Rules, ProfileRevision: profile.Revision, Selected: []Course{},
		Candidates: []Candidate{}, Evidence: []Evidence{}, EvidenceMode: "NO_EVIDENCE", ExplanationMode: "RULES",
		Warnings: []string{"institutional_conditions_unverified"}}
	if err := req.Validate(); err != nil {
		return plan, err
	}
	if err := profile.Validate(); err != nil {
		return plan, err
	}
	catalog, err := p.Graph.Catalog(ctx)
	plan.ToolCalls++
	if err != nil {
		return plan, err
	}
	if !shaRE.MatchString(catalog.Version) {
		return plan, Dependency
	}
	plan.SnapshotVersion = catalog.Version
	if p.Evidence != nil && (p.EvidenceSnapshot == "" || p.EvidenceSnapshot == catalog.Version) {
		plan.RetrievalCalls++
		evidence, mode, err := p.Evidence.Find(ctx, strings.Join([]string{req.Goal, profile.Direction, profile.Preference}, " "))
		if err == nil && len(evidence) != 0 {
			plan.Evidence, plan.EvidenceMode = evidence, mode
		} else if err != nil {
			plan.Warnings = append(plan.Warnings, "retrieval_unavailable")
		}
	}
	if len(plan.Evidence) == 0 {
		plan.Warnings = append(plan.Warnings, "evidence_unavailable")
	}
	if err := progress("retrieval_completed"); err != nil {
		return plan, err
	}
	valid := make(map[string]Course)
	for _, c := range catalog.Courses {
		if !idRE.MatchString(c.ID) || strings.TrimSpace(c.Name) == "" || valid[c.ID].ID != "" {
			return plan, Dependency
		}
		valid[c.ID] = c
	}
	if len(valid) == 0 || len(valid) > 100 {
		return plan, CatalogSize
	}
	done := make(map[string]bool)
	for _, id := range profile.Completed {
		if _, ok := valid[id]; !ok {
			return plan, UnknownCourse
		}
		done[id] = true
	}
	candidate := catalog.Courses
	if len(req.CourseIDs) > 0 {
		candidate = make([]Course, 0, len(req.CourseIDs))
		for _, id := range req.CourseIDs {
			c, ok := valid[id]
			if !ok {
				return plan, UnknownCourse
			}
			candidate = append(candidate, c)
		}
	}
	ranked := make([]Candidate, 0, len(candidate))
	for _, c := range candidate {
		ranked = append(ranked, Candidate{Course: c, Score: score(c, profile.Direction, profile.Preference, req.Goal), Missing: []string{}})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].Course.ID < ranked[j].Course.ID
		}
		return ranked[i].Score > ranked[j].Score
	})
	var budget, credits int
	if req.MaxCredits != nil {
		budget, _ = cents(*req.MaxCredits)
	}
	for _, item := range ranked {
		if ctx.Err() != nil {
			return plan, ctx.Err()
		}
		c := item.Course
		switch {
		case done[c.ID]:
			item.Outcome = "ALREADY_COMPLETED"
		case strings.TrimSpace(req.RequiredKeyword) != "" &&
			!strings.Contains(strings.ToLower(c.Name), strings.ToLower(strings.TrimSpace(req.RequiredKeyword))):
			item.Outcome = "USER_FILTER_MISMATCH"
		case plan.ToolCalls > MaxCandidates:
			item.Outcome = "NOT_CHECKED"
		default:
			proof, failure := p.Graph.Check(ctx, c.ID)
			plan.ToolCalls++
			if failure != nil {
				return plan, failure
			}
			if proof.Version != catalog.Version {
				return plan, Changed
			}
			seen := map[string]bool{}
			for _, prerequisite := range proof.Required {
				if _, ok := valid[prerequisite]; !ok || prerequisite == c.ID || seen[prerequisite] {
					return plan, Dependency
				}
				seen[prerequisite] = true
				if !done[prerequisite] {
					item.Missing = append(item.Missing, prerequisite)
				}
			}
			sort.Strings(item.Missing)
			units, known := 0, false
			if c.Credits != nil {
				units, known = cents(*c.Credits)
			}
			switch {
			case len(item.Missing) > 0:
				item.Outcome = "MISSING_REQUIRED"
			case !known:
				item.Outcome = "UNKNOWN_CREDITS"
			case budget > 0 && credits+units > budget:
				item.Outcome = "CREDIT_LIMIT"
			case len(plan.Selected) == req.Limit:
				item.Outcome = "NOT_SELECTED_LIMIT"
			default:
				item.Outcome = "SELECTED"
				credits += units
				plan.Selected = append(plan.Selected, c)
			}
		}
		plan.Candidates = append(plan.Candidates, item)
	}
	if err := progress("constraints_checked"); err != nil {
		return plan, err
	}
	capped, unknown := false, false
	for _, item := range plan.Candidates {
		capped = capped || item.Outcome == "NOT_CHECKED"
		unknown = unknown || item.Outcome == "UNKNOWN_CREDITS"
	}
	if capped {
		plan.Warnings = append(plan.Warnings, "candidate_budget_reached")
	}
	switch {
	case len(plan.Selected) != 0:
		plan.Status = "REVIEW_REQUIRED"
	case capped || unknown:
		plan.Status = "UNKNOWN"
	default:
		plan.Status = "NO_FEASIBLE_SELECTION"
	}
	plan.TotalCredits = float64(credits) / 100
	if err := progress("plan_generated"); err != nil {
		return plan, err
	}
	if p.Narrator != nil && len(plan.Selected) > 0 {
		plan.ModelCalls++
		explanation, tokens, err := p.Narrator.Explain(ctx, plan.Selected)
		if err == nil && bounded(explanation, 480) && strings.TrimSpace(explanation) != "" {
			plan.Explanation, plan.Tokens, plan.ExplanationMode = explanation, tokens, "LOCAL_MODEL"
		} else if ctx.Err() != nil {
			return plan, ctx.Err()
		} else {
			plan.Warnings = append(plan.Warnings, "model_explanation_unavailable")
			plan.ExplanationMode = "RULES_FALLBACK"
		}
	}
	if ctx.Err() != nil {
		return plan, ctx.Err()
	}
	return plan, nil
}
