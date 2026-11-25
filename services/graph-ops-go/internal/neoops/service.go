package neoops

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"learning-guide/graph-ops/internal/graph"
)

const lifetime = 3 * time.Minute

var operationKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,128}$`)

type Service struct {
	repo *Repository
	key  []byte
	now  func() time.Time
}

func New(repo *Repository, key string) (*Service, error) {
	k, err := tokenKey(key)
	if err != nil || strings.EqualFold(key, repo.marker) {
		return nil, errors.New("lab plan signing key must be a separate 32-byte value")
	}
	return &Service{repo: repo, key: k, now: time.Now}, nil
}

func (s *Service) Ready(ctx context.Context) error { return s.repo.Ready(ctx) }

type Target struct {
	Mode   string      `json:"mode,omitempty"`
	Scope  graph.Scope `json:"scope"`
	Target *graph.Data `json:"target"`
}

type counts struct {
	Courses   graph.Counts `json:"courses"`
	Relations graph.Counts `json:"relations"`
}

type claims struct {
	ID           string      `json:"plan_id"`
	TargetHash   string      `json:"target_digest"`
	SnapshotHash string      `json:"snapshot_digest"`
	Version      int64       `json:"version"`
	Scope        graph.Scope `json:"scope"`
	Created      time.Time   `json:"created_at"`
	Expires      time.Time   `json:"expires_at"`
	Statistics   counts      `json:"statistics"`
}

type PreviewResult struct {
	Claims claims     `json:"metadata"`
	Plan   graph.Plan `json:"plan"`
	Token  string     `json:"plan_token"`
}

type Command struct {
	PlanToken string      `json:"plan_token"`
	Target    *graph.Data `json:"target"`
}

type Operation struct {
	ID          string    `json:"operation_id"`
	PlanID      string    `json:"plan_id"`
	RequestHash string    `json:"request_digest"`
	Started     time.Time `json:"started_at"`
	Finished    time.Time `json:"finished_at"`
	Statistics  counts    `json:"statistics"`
	Status      string    `json:"status"`
	ErrorCode   string    `json:"error_code"`
}

type Execution struct {
	Operation       Operation     `json:"operation"`
	Replay          bool          `json:"replay"`
	TransactionTime time.Duration `json:"-"`
	AuditAttempt    Operation     `json:"-"`
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return hash(b), nil
}

func canonical(input graph.Data) graph.Data {
	d := graph.Data{Courses: append([]graph.Course{}, input.Courses...), Relations: append([]graph.Relation{}, input.Relations...)}
	for i := range d.Relations {
		if d.Relations[i].Strength == "" {
			d.Relations[i].Strength = "strong"
		}
	}
	slices.SortFunc(d.Courses, func(a, b graph.Course) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(d.Relations, func(a, b graph.Relation) int {
		if n := strings.Compare(a.Type, b.Type); n != 0 {
			return n
		}
		if n := strings.Compare(a.Source, b.Source); n != 0 {
			return n
		}
		return strings.Compare(a.Target, b.Target)
	})
	return d
}

func (s *Service) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(Database + ":" + s.repo.marker + ":"))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func (s *Service) decode(token string) (claims, error) {
	var c claims
	p, sig, ok := strings.Cut(token, ".")
	if !ok {
		return c, fail(422, "invalid_plan_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return c, fail(422, "invalid_plan_token")
	}
	proof, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(proof, s.sign(raw)) || json.Unmarshal(raw, &c) != nil {
		return c, fail(422, "invalid_plan_token")
	}
	return c, nil
}

func (s *Service) Preview(ctx context.Context, in Target) (PreviewResult, error) {
	if in.Mode != "" && in.Mode != graph.Incremental {
		return PreviewResult{}, fail(422, "incremental_only")
	}
	if in.Target == nil || len(in.Scope.CourseIDs) == 0 || len(in.Scope.CourseIDs) > graph.MaxCourses {
		return PreviewResult{}, fail(422, "target_and_scope_required")
	}
	if len(in.Target.Courses) > graph.MaxCourses || len(in.Target.Relations) > graph.MaxRelations {
		return PreviewResult{}, fail(422, "record_limit")
	}
	scope := graph.Scope{CourseIDs: append([]string{}, in.Scope.CourseIDs...)}
	slices.Sort(scope.CourseIDs)
	target := canonical(*in.Target)
	targetHash, err := digest(target)
	if err != nil {
		return PreviewResult{}, fail(422, "invalid_graph_plan")
	}

	result, err := s.repo.read(ctx, func(tx runner) (any, error) {
		version, err := s.repo.epoch(ctx, tx, false)
		if err != nil {
			return nil, err
		}
		data, err := readSnapshot(ctx, tx)
		if err != nil {
			return nil, err
		}
		last, err := s.repo.epoch(ctx, tx, false)
		if err != nil {
			return nil, err
		}
		if last != version {
			return nil, fail(409, "database_state_changed")
		}
		plan, err := data.plan(ctx, target, scope)
		if err != nil {
			return PreviewResult{Plan: plan}, err
		}
		snapshotHash, err := digest(data)
		if err != nil {
			return nil, err
		}
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return nil, err
		}
		now := s.now().UTC()
		c := claims{hex.EncodeToString(id), targetHash, snapshotHash, version, scope, now, now.Add(lifetime), counts{plan.Statistics.Courses, plan.Statistics.Relations}}
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		b64 := base64.RawURLEncoding
		return PreviewResult{c, plan, b64.EncodeToString(raw) + "." + b64.EncodeToString(s.sign(raw))}, nil
	})
	if err != nil {
		if partial, ok := result.(PreviewResult); ok {
			return partial, err
		}
		return PreviewResult{}, err
	}
	return result.(PreviewResult), nil
}

func OperationID(key string) (string, error) {
	if !operationKey.MatchString(key) {
		return "", fail(422, "invalid_idempotency_key")
	}
	return hash([]byte("graphops-operation:" + key)), nil
}

func getOperation(ctx context.Context, tx runner, id string) (Operation, bool, error) {
	var op Operation
	rows, err := records(ctx, tx, "MATCH (o:GraphOpsOperation {id:$id}) RETURN o.result AS result", map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return op, false, err
	}
	if len(rows) != 1 {
		return op, false, fail(503, "invalid_operation_record")
	}
	v, _ := rows[0].Get("result")
	raw, ok := v.(string)
	if !ok || json.Unmarshal([]byte(raw), &op) != nil || op.ID != id || op.Status != "committed" {
		return op, false, fail(503, "invalid_operation_record")
	}
	return op, true, nil
}

func (s *Service) Lookup(ctx context.Context, key string) (Operation, error) {
	id, err := OperationID(key)
	if err != nil {
		return Operation{}, err
	}
	result, err := s.repo.read(ctx, func(tx runner) (any, error) {
		if _, err := s.repo.epoch(ctx, tx, false); err != nil {
			return nil, err
		}
		op, ok, err := getOperation(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fail(404, "operation_not_observed")
		}
		return op, nil
	})
	if err != nil {
		return Operation{}, err
	}
	return result.(Operation), nil
}

func (s *Service) Execute(ctx context.Context, in Command, key string) (out Execution, finalErr error) {
	id, err := OperationID(key)
	if err != nil {
		return Execution{}, err
	}
	c, err := s.decode(in.PlanToken)
	if err != nil {
		return Execution{}, err
	}
	if in.Target == nil || len(in.Target.Courses) > graph.MaxCourses || len(in.Target.Relations) > graph.MaxRelations {
		return Execution{}, fail(422, "invalid_graph_plan")
	}
	target := canonical(*in.Target)
	targetHash, err := digest(target)
	if err != nil {
		return Execution{}, fail(422, "invalid_graph_plan")
	}
	requestHash := hash([]byte(c.ID + ":" + targetHash))
	start := s.now().UTC()
	defer func() {
		if finalErr == nil {
			return
		}
		status := "rejected"
		code := "neo4j_unavailable"
		var fault *Fault
		if errors.As(finalErr, &fault) {
			code = fault.Code
			if fault.RolledBack {
				status = "rolled_back"
			}
			if fault.Code == "commit_outcome_unknown" {
				status = "unknown"
			}
		}
		if out.TransactionTime > 0 && status == "rejected" && code == "execution_failed" {
			status = "failed_before_commit"
		}
		out.AuditAttempt = Operation{id, c.ID, requestHash, start, s.now().UTC(), c.Statistics, status, code}
	}()
	if err = s.repo.Ready(ctx); err != nil {
		return Execution{}, err
	}
	session := s.repo.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: Database, AccessMode: neo4j.AccessModeWrite})
	defer cleanup(session.Close)
	tx, err := session.BeginTransaction(ctx, neo4j.WithTxTimeout(txTimeout))
	if err != nil {
		return Execution{}, err
	}
	txStarted := time.Now()
	defer func() { out.TransactionTime = time.Since(txStarted) }()
	defer cleanup(tx.Close)
	version, err := s.repo.epoch(ctx, tx, true)
	if err != nil {
		return Execution{}, err
	}
	stored, replay, err := getOperation(ctx, tx, id)
	if err != nil {
		return Execution{}, err
	}
	if replay {
		if stored.RequestHash != requestHash {
			return Execution{}, fail(409, "idempotency_conflict")
		}
		return Execution{Operation: stored, Replay: true}, nil
	}
	if targetHash != c.TargetHash {
		return Execution{}, fail(409, "plan_target_mismatch")
	}
	if !s.now().Before(c.Expires) {
		return Execution{}, fail(409, "plan_expired")
	}
	if c.Statistics.Courses.DeleteCandidates+c.Statistics.Relations.DeleteCandidates != 0 {
		return Execution{}, fail(422, "deletion_not_supported")
	}
	if version != c.Version {
		return Execution{}, fail(409, "database_state_changed")
	}
	data, err := readSnapshot(ctx, tx)
	if err != nil {
		return Execution{}, err
	}
	current, err := digest(data)
	if err != nil {
		return Execution{}, err
	}
	if current != c.SnapshotHash {
		return Execution{}, fail(409, "database_state_changed")
	}
	p, err := data.plan(ctx, target, c.Scope)
	if err != nil {
		return Execution{}, err
	}
	stats := counts{p.Statistics.Courses, p.Statistics.Relations}
	if stats.Courses.DeleteCandidates+stats.Relations.DeleteCandidates != 0 {
		return Execution{}, fail(422, "deletion_not_supported")
	}
	if stats != c.Statistics {
		return Execution{}, fail(409, "database_state_changed")
	}

	op, err := s.apply(ctx, tx, p, id, c.ID, requestHash, start, stats)
	if err != nil {
		code := fail(503, "execution_failed")
		var underlying *Fault
		if errors.As(err, &underlying) {
			code = underlying
		}
		code.RolledBack = cleanup(tx.Rollback) == nil
		return Execution{}, code
	}
	if err = tx.Commit(ctx); err != nil {
		// A failed commit acknowledgement can follow a successful server-side commit.
		return Execution{}, fail(503, "commit_outcome_unknown")
	}
	return Execution{Operation: op}, nil
}

func (s *Service) apply(ctx context.Context, tx neo4j.ExplicitTransaction, p graph.Plan, id, planID, requestHash string, start time.Time, stats counts) (Operation, error) {
	var op Operation
	if err := applyCourses(ctx, tx, p); err != nil {
		return op, err
	}
	if s.repo.afterCourses != nil {
		if err := s.repo.afterCourses(ctx, tx); err != nil {
			return op, err
		}
	}
	if err := applyRelations(ctx, tx, p); err != nil {
		return op, err
	}
	op = Operation{id, planID, requestHash, start, s.now().UTC(), stats, "committed", ""}
	body, err := json.Marshal(op)
	if err != nil {
		return op, err
	}
	err = consume(ctx, tx, "CREATE (o:GraphOpsOperation {id:$id,result:$result})", map[string]any{"id": id, "result": string(body)})
	return op, err
}
