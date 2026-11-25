package advisor

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Rules = "selection-v1"
const MaxCandidates = 16

type Failure string

func (f Failure) Error() string { return string(f) }

const (
	Invalid       Failure = "invalid_request"
	Missing       Failure = "not_found"
	Revision      Failure = "revision_conflict"
	Busy          Failure = "queue_busy"
	Idempotency   Failure = "idempotency_conflict"
	Dependency    Failure = "prerequisites_unavailable"
	Changed       Failure = "snapshot_changed"
	CatalogSize   Failure = "catalog_too_large"
	UnknownCourse Failure = "unknown_course"
	Deadline      Failure = "run_timeout"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var shaRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
var keyRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{12,80}$`)

type Profile struct {
	Revision   int      `json:"revision"`
	Name       string   `json:"name"`
	Stage      string   `json:"stage"`
	Completed  []string `json:"completed"`
	Direction  string   `json:"direction"`
	Preference string   `json:"preference"`
}

type Request struct {
	ProfileRevision int      `json:"profile_revision"`
	ParentRun       string   `json:"parent_run,omitempty"`
	Goal            string   `json:"goal"`
	CourseIDs       []string `json:"course_ids"`
	MaxCredits      *float64 `json:"max_credits,omitempty"`
	Limit           int      `json:"limit"`
	RequiredKeyword string   `json:"required_keyword,omitempty"`
}

type Course struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Semester string   `json:"semester"`
	Credits  *float64 `json:"credits"`
}

type Catalog struct {
	Version string   `json:"version"`
	Courses []Course `json:"courses"`
}

type CourseCheck struct {
	Version  string   `json:"version"`
	Required []string `json:"required"`
}

type Evidence struct {
	DocID   string  `json:"doc_id"`
	Version string  `json:"version"`
	Section string  `json:"section"`
	Source  string  `json:"source"`
	Excerpt string  `json:"excerpt"`
	Score   float64 `json:"score"`
}

type Candidate struct {
	Course  Course   `json:"course"`
	Score   int      `json:"score"`
	Outcome string   `json:"outcome"`
	Missing []string `json:"missing"`
}

type Plan struct {
	Status          string      `json:"status"`
	RuleVersion     string      `json:"rule_version"`
	SnapshotVersion string      `json:"snapshot_version"`
	ProfileRevision int         `json:"profile_revision"`
	TotalCredits    float64     `json:"total_credits"`
	Selected        []Course    `json:"selected"`
	Candidates      []Candidate `json:"candidates"`
	Evidence        []Evidence  `json:"evidence"`
	EvidenceMode    string      `json:"evidence_mode"`
	ExplanationMode string      `json:"explanation_mode"`
	Explanation     string      `json:"explanation,omitempty"`
	Warnings        []string    `json:"warnings"`
	ToolCalls       int         `json:"tool_calls"`
	RetrievalCalls  int         `json:"retrieval_calls"`
	ModelCalls      int         `json:"model_calls"`
	Tokens          *int        `json:"tokens,omitempty"`
}

func bounded(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, 0)
}

func idsOK(ids []string, limit int) bool {
	seen := make(map[string]bool)
	if len(ids) > limit {
		return false
	}
	for _, id := range ids {
		if !idRE.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (v Profile) Validate() error {
	if v.Revision < 0 || strings.TrimSpace(v.Name) == "" || !bounded(v.Name, 60) ||
		!bounded(v.Stage, 40) || !bounded(v.Direction, 80) || !bounded(v.Preference, 120) || !idsOK(v.Completed, 100) {
		return Invalid
	}
	return nil
}

func cents(n float64) (int, bool) {
	i := math.Round(n * 100)
	return int(i), n > 0 && n <= 100 && math.Abs(n*100-i) < 1e-7
}

func (v Request) Validate() error {
	if v.ProfileRevision < 1 || !bounded(v.Goal, 240) || strings.TrimSpace(v.Goal) == "" ||
		!bounded(v.RequiredKeyword, 60) || v.Limit < 1 || v.Limit > 8 ||
		!idsOK(v.CourseIDs, MaxCandidates) || (v.ParentRun != "" && !idRE.MatchString(v.ParentRun)) {
		return Invalid
	}
	if v.MaxCredits != nil {
		if _, ok := cents(*v.MaxCredits); !ok {
			return Invalid
		}
	}
	return nil
}

func FailureCode(err error) string {
	var f Failure
	if errors.As(err, &f) {
		return string(f)
	}
	return "internal_error"
}
