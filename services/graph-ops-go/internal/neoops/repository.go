package neoops

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/config"
	"learning-guide/graph-ops/internal/graph"
)

const Database = "graphops-phase2-test"
const owner = "synthetic"
const prefix = "LAB-"
const txTimeout = 4 * time.Second

type Fault struct {
	Code       string
	Status     int
	RolledBack bool
}

func (f *Fault) Error() string { return f.Code }

func fail(status int, code string) *Fault { return &Fault{Code: code, Status: status} }

type Repository struct {
	driver       neo4j.DriverWithContext
	marker       string
	afterCourses func(context.Context, neo4j.ExplicitTransaction) error
	commitAck    func(context.Context, neo4j.ExplicitTransaction) error
}

func tokenKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err != nil || len(key) != 32 {
		return nil, errors.New("expected 32 bytes in hexadecimal")
	}
	return key, nil
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "bolt" || u.User != nil || u.RawQuery != "" || u.Path != "" || u.Fragment != "" {
		return errors.New("lab requires a direct bolt URL without credentials or options")
	}
	addr, err := netip.ParseAddrPort(u.Host)
	if err != nil || !addr.Addr().IsLoopback() || addr.Port() < 1024 || addr.Port() == 7687 {
		return errors.New("lab requires an explicit loopback port distinct from the default")
	}
	return nil
}

func Open(raw, marker string) (*Repository, error) {
	if err := validateEndpoint(raw); err != nil {
		return nil, err
	}
	if _, err := tokenKey(marker); err != nil {
		return nil, errors.New("invalid lab marker")
	}
	driver, err := neo4j.NewDriverWithContext(raw, neo4j.NoAuth(), func(c *config.Config) {
		c.SocketConnectTimeout = time.Second
		c.ConnectionAcquisitionTimeout = 2 * time.Second
		c.MaxConnectionPoolSize = 8
	})
	if err != nil {
		return nil, errors.New("cannot configure lab driver")
	}
	return &Repository{driver: driver, marker: marker}, nil
}

func (r *Repository) Close(ctx context.Context) error { return r.driver.Close(ctx) }

type runner interface {
	Run(context.Context, string, map[string]any) (neo4j.ResultWithContext, error)
}

func records(ctx context.Context, tx runner, query string, params map[string]any) ([]*neo4j.Record, error) {
	res, err := tx.Run(ctx, query, params)
	if err != nil {
		return nil, err
	}
	return res.Collect(ctx)
}

func consume(ctx context.Context, tx runner, query string, params map[string]any) error {
	res, err := tx.Run(ctx, query, params)
	if err != nil {
		return err
	}
	_, err = res.Consume(ctx)
	return err
}

func decodeValue(row *neo4j.Record, key string, out any) error {
	v, ok := row.Get(key)
	if !ok {
		return errors.New("missing database value")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func cleanup(action func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return action(ctx)
}

const findLab = "MATCH (m:GraphOpsLab {id:'phase2', marker:$marker}) RETURN m.epoch AS epoch"
const lockLab = "MATCH (m:GraphOpsLab {id:'phase2', marker:$marker}) SET m.epoch = m.epoch + 1 RETURN m.epoch - 1 AS epoch"

func (r *Repository) epoch(ctx context.Context, tx runner, lock bool) (int64, error) {
	q := findLab
	if lock {
		q = lockLab
	}
	rows, err := records(ctx, tx, q, map[string]any{"marker": r.marker})
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, fail(503, "lab_identity_unverified")
	}
	v, _ := rows[0].Get("epoch")
	epoch, ok := v.(int64)
	if !ok || epoch < 0 {
		return 0, fail(503, "lab_identity_unverified")
	}
	return epoch, nil
}

func (r *Repository) read(ctx context.Context, work func(runner) (any, error)) (any, error) {
	s := r.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: Database, AccessMode: neo4j.AccessModeRead})
	defer cleanup(s.Close)
	// Use an explicit transaction here as well so a readiness timeout is not retried.
	tx, err := s.BeginTransaction(ctx, neo4j.WithTxTimeout(txTimeout))
	if err != nil {
		return nil, err
	}
	defer cleanup(tx.Close)
	result, err := work(tx)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (r *Repository) Ready(ctx context.Context) error {
	_, err := r.read(ctx, func(tx runner) (any, error) {
		identity, err := records(ctx, tx, "CALL db.info() YIELD name RETURN name", nil)
		if err != nil {
			return nil, err
		}
		if len(identity) != 1 || identity[0].Values[0] != Database {
			return nil, fail(503, "lab_identity_unverified")
		}
		if _, err := r.epoch(ctx, tx, false); err != nil {
			return nil, err
		}
		rows, err := records(ctx, tx, `SHOW CONSTRAINTS YIELD type, entityType, labelsOrTypes, properties
WHERE type = 'UNIQUENESS' AND entityType = 'NODE'
RETURN labelsOrTypes[0] AS label, properties AS fields`, nil)
		if err != nil {
			return nil, err
		}
		wanted := map[string]bool{"Course.id": false, "Course.name": false, "GraphOpsLab.id": false, "GraphOpsOperation.id": false}
		for _, row := range rows {
			var fields []string
			label, _ := row.Get("label")
			if err := decodeValue(row, "fields", &fields); err != nil {
				return nil, err
			}
			if len(fields) == 1 {
				key := fmt.Sprint(label) + "." + fields[0]
				if _, ok := wanted[key]; ok {
					wanted[key] = true
				}
			}
		}
		for _, present := range wanted {
			if !present {
				return nil, fail(503, "lab_constraints_missing")
			}
		}
		return nil, nil
	})
	return err
}

type courseState struct {
	graph.Course
	Owner string `json:"owner"`
}
type relationState struct {
	graph.Relation
	Owner string `json:"owner"`
}
type snapshot struct {
	Courses   []courseState
	Relations []relationState
}

func readSnapshot(ctx context.Context, tx runner) (snapshot, error) {
	var data snapshot
	nodes, err := records(ctx, tx, `MATCH (c:Course)
RETURN c {.id,.name,.category,.semester,.credits,.hours,.layer,.description,owner:c.graphOpsScope} AS value
ORDER BY c.id LIMIT $limit`, map[string]any{"limit": graph.MaxCourses + 1})
	if err != nil {
		return data, err
	}
	edges, err := records(ctx, tx, `MATCH (s:Course)-[r:PREREQ]->(t:Course)
RETURN {type:'PREREQ', source:s.id, target:t.id, reason:r.reason, strength:r.strength,owner:r.graphOpsScope} AS value
ORDER BY s.id,t.id LIMIT $limit`, map[string]any{"limit": graph.MaxRelations + 1})
	if err != nil {
		return data, err
	}
	if len(nodes) > graph.MaxCourses || len(edges) > graph.MaxRelations {
		return data, fail(409, "snapshot_too_large")
	}
	for _, row := range nodes {
		var c courseState
		if err := decodeValue(row, "value", &c); err != nil {
			return data, fail(409, "invalid_database_snapshot")
		}
		data.Courses = append(data.Courses, c)
	}
	for _, row := range edges {
		var e relationState
		if err := decodeValue(row, "value", &e); err != nil {
			return data, fail(409, "invalid_database_snapshot")
		}
		data.Relations = append(data.Relations, e)
	}
	return data, nil
}

func (d snapshot) plan(ctx context.Context, target graph.Data, scope graph.Scope) (graph.Plan, error) {
	current := graph.Data{}
	courseOwners := make(map[string]string, len(d.Courses))
	edgeOwners := make(map[[2]string]string, len(d.Relations))
	for _, c := range d.Courses {
		current.Courses = append(current.Courses, c.Course)
		courseOwners[c.ID] = c.Owner
	}
	for _, e := range d.Relations {
		current.Relations = append(current.Relations, e.Relation)
		edgeOwners[[2]string{e.Source, e.Target}] = e.Owner
	}
	if issues, err := graph.Validate(ctx, current); err != nil {
		return graph.Plan{}, err
	} else if len(issues) != 0 {
		return graph.Plan{}, fail(409, "invalid_database_snapshot")
	}
	for _, id := range scope.CourseIDs {
		if !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
			return graph.Plan{}, fail(422, "outside_managed_scope")
		}
		if v, ok := courseOwners[id]; ok && v != owner {
			return graph.Plan{}, fail(422, "outside_managed_scope")
		}
	}
	for _, e := range target.Relations {
		if v, ok := edgeOwners[[2]string{e.Source, e.Target}]; ok && v != owner {
			return graph.Plan{}, fail(422, "outside_managed_scope")
		}
	}
	p, err := graph.MakePlan(ctx, graph.Preview{Current: &current, Target: &target, Scope: &scope})
	if err == nil && !p.Executable {
		return p, fail(422, "invalid_graph_plan")
	}
	return p, err
}

func exactlyOne(ctx context.Context, tx runner, q string, params map[string]any) error {
	rows, err := records(ctx, tx, q, params)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return fail(409, "database_state_changed")
	}
	return nil
}

func applyCourses(ctx context.Context, tx runner, p graph.Plan) error {
	for _, c := range p.Courses.Added {
		if err := exactlyOne(ctx, tx, `CREATE (c:Course {id:$id,graphOpsScope:$owner})
SET c.name=$name,c.category=$category,c.semester=$semester,c.credits=$credits,c.hours=$hours,c.layer=$layer,c.description=$description RETURN c.id`, courseParams(c)); err != nil {
			return err
		}
	}
	for _, c := range p.Courses.Modified {
		if err := exactlyOne(ctx, tx, `MATCH (c:Course {id:$id,graphOpsScope:$owner})
SET c.name=$name,c.category=$category,c.semester=$semester,c.credits=$credits,c.hours=$hours,c.layer=$layer,c.description=$description RETURN c.id`, courseParams(c.After)); err != nil {
			return err
		}
	}
	return nil
}

func courseParams(c graph.Course) map[string]any {
	return map[string]any{"id": c.ID, "owner": owner, "name": c.Name, "category": c.Category, "semester": c.Semester, "credits": c.Credits, "hours": c.Hours, "layer": c.Layer, "description": c.Description}
}

func applyRelations(ctx context.Context, tx runner, p graph.Plan) error {
	for _, e := range p.Relations.Added {
		if err := exactlyOne(ctx, tx, `MATCH (s:Course {id:$source,graphOpsScope:$owner}),(t:Course {id:$target,graphOpsScope:$owner})
WHERE NOT (s)-[:PREREQ]->(t)
CREATE (s)-[r:PREREQ {graphOpsScope:$owner,reason:$reason,strength:$strength}]->(t) RETURN r.strength`, edgeParams(e)); err != nil {
			return err
		}
	}
	for _, e := range p.Relations.Modified {
		if err := exactlyOne(ctx, tx, `MATCH (:Course {id:$source,graphOpsScope:$owner})-[r:PREREQ {graphOpsScope:$owner}]->(:Course {id:$target,graphOpsScope:$owner})
SET r.reason=$reason,r.strength=$strength RETURN r.strength`, edgeParams(e.After)); err != nil {
			return err
		}
	}
	return nil
}

func edgeParams(e graph.Relation) map[string]any {
	return map[string]any{"source": e.Source, "target": e.Target, "owner": owner, "reason": e.Reason, "strength": e.Strength}
}
