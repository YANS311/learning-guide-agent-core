package advisor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

const dbName = "course-advisor.sqlite3"
const appID = "LADV"

const schema = `
PRAGMA application_id=1279345750;
PRAGMA user_version=1;
CREATE TABLE workspace(id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE);
CREATE TABLE profile(owner TEXT PRIMARY KEY REFERENCES workspace(id), revision INTEGER NOT NULL, body TEXT NOT NULL);
CREATE TABLE conversation(owner TEXT PRIMARY KEY REFERENCES workspace(id));
CREATE TABLE run(id TEXT PRIMARY KEY, owner TEXT NOT NULL REFERENCES conversation(owner), key TEXT NOT NULL, digest TEXT NOT NULL,
	profile TEXT NOT NULL, request TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN
	('QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELLED','INTERRUPTED')),
	plan_id TEXT NOT NULL, version INTEGER NOT NULL, created TEXT NOT NULL, error TEXT NOT NULL DEFAULT '',
	UNIQUE(owner,key));
CREATE INDEX run_queue ON run(state,created,id);
CREATE INDEX run_history ON run(owner,created DESC,id DESC);
CREATE TABLE message(run TEXT NOT NULL REFERENCES run(id), role TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(run,role));
CREATE TABLE event(run TEXT NOT NULL REFERENCES run(id), seq INTEGER NOT NULL, kind TEXT NOT NULL, created TEXT NOT NULL, PRIMARY KEY(run,seq));
CREATE TABLE plan_version(plan_id TEXT NOT NULL, version INTEGER NOT NULL, run TEXT NOT NULL UNIQUE REFERENCES run(id),
	body TEXT NOT NULL, PRIMARY KEY(plan_id,version));
`

type Store struct {
	db    *sql.DB
	guard *flock.Flock
}

func OpenStore(name string) (*Store, error) {
	path, err := filepath.Abs(name)
	if err != nil || filepath.Base(path) != dbName {
		return nil, errors.New("use a dedicated " + dbName)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("advisor database already in use")
	}
	fail := true
	defer func() {
		if fail {
			_ = lock.Close()
		}
	}()
	init := false
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("not a regular database file")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		var header [72]byte
		_, err = io.ReadFull(f, header[:])
		_ = f.Close()
		if err != nil || string(header[:16]) != "SQLite format 3\x00" || string(header[68:]) != appID {
			return nil, errors.New("refusing database without adviser application identity")
		}
	} else if os.IsNotExist(err) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		init = true
	} else {
		return nil, err
	}
	dsn := filepath.ToSlash(path)
	if strings.ContainsAny(dsn, "?#") {
		return nil, errors.New("unsupported database path")
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer func() {
		if fail {
			_ = db.Close()
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=2000; PRAGMA foreign_keys=ON"); err != nil {
		return nil, err
	}
	if init {
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(schema); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		return nil, errors.New("unsupported advisor database schema")
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL"); err != nil {
		return nil, err
	}
	s := &Store{db, lock}
	if err = s.recover(); err != nil {
		return nil, err
	}
	fail = false
	return s, nil
}

func (s *Store) Close() error { return errors.Join(s.db.Close(), s.guard.Close()) }
func now() string             { return time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z") }

func randomID(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func digest(b []byte) string {
	value := sha256.Sum256(b)
	return hex.EncodeToString(value[:])
}

func txDo(ctx context.Context, s *Store, f func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) NewWorkspace(ctx context.Context) (string, error) {
	token, err := randomID(32)
	if err != nil {
		return "", err
	}
	id, err := randomID(16)
	if err != nil {
		return "", err
	}
	err = txDo(ctx, s, func(tx *sql.Tx) error {
		if _, err = tx.Exec("INSERT INTO workspace VALUES(?,?)", id, digest([]byte(token))); err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO conversation VALUES(?)", id)
		return err
	})
	return token, err
}

func (s *Store) Owner(ctx context.Context, token string) (string, error) {
	if !shaRE.MatchString(token) {
		return "", Missing
	}
	var owner string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM workspace WHERE token_hash=?", digest([]byte(token))).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		err = Missing
	}
	return owner, err
}

func (s *Store) Profile(ctx context.Context, owner string) (Profile, error) {
	var p Profile
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT body FROM profile WHERE owner=?", owner).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, Missing
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}

func (s *Store) SaveProfile(ctx context.Context, owner string, p Profile) (Profile, error) {
	if err := p.Validate(); err != nil {
		return p, err
	}
	err := txDo(ctx, s, func(tx *sql.Tx) error {
		prior := p.Revision
		p.Revision++
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		q := "UPDATE profile SET revision=?,body=? WHERE owner=? AND revision=?"
		args := []any{p.Revision, string(raw), owner, prior}
		if prior == 0 {
			q = "INSERT OR IGNORE INTO profile(owner,revision,body) VALUES(?,?,?)"
			args = []any{owner, p.Revision, string(raw)}
		}
		res, err := tx.Exec(q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return Revision
		}
		return nil
	})
	return p, err
}

type Run struct {
	ID      string  `json:"id"`
	PlanID  string  `json:"plan_id"`
	Version int     `json:"version"`
	State   string  `json:"state"`
	Created string  `json:"created"`
	Error   string  `json:"error,omitempty"`
	Goal    string  `json:"goal"`
	Request Request `json:"request"`
	Plan    *Plan   `json:"plan,omitempty"`
}
type Event struct {
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"`
	Created string `json:"created"`
}

func addEvent(tx *sql.Tx, id, kind string) error {
	_, err := tx.Exec("INSERT INTO event SELECT ?,coalesce(max(seq),0)+1,?,? FROM event WHERE run=?", id, kind, now(), id)
	return err
}

func (s *Store) Enqueue(ctx context.Context, owner, key string, r Request) (Run, bool, error) {
	out := Run{Version: 1, State: "QUEUED", Created: now(), Goal: r.Goal, Request: r}
	if !keyRE.MatchString(key) {
		return out, false, Invalid
	}
	if err := r.Validate(); err != nil {
		return out, false, err
	}
	req, err := json.Marshal(r)
	if err != nil {
		return out, false, err
	}
	hash := digest(req)
	var replay bool
	err = txDo(ctx, s, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRow("SELECT id,digest FROM run WHERE owner=? AND key=?", owner, key).Scan(&out.ID, &old)
		if err == nil {
			if old != hash {
				return Idempotency
			}
			replay = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var body string
		var revision int
		err = tx.QueryRow("SELECT revision,body FROM profile WHERE owner=?", owner).Scan(&revision, &body)
		if errors.Is(err, sql.ErrNoRows) {
			return Revision
		}
		if err != nil {
			return err
		}
		if revision != r.ProfileRevision {
			return Revision
		}
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM run WHERE state IN ('QUEUED','RUNNING') AND owner=?", owner).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return Busy
		}
		if err = tx.QueryRow("SELECT COUNT(*) FROM run WHERE state IN ('QUEUED','RUNNING')").Scan(&count); err != nil {
			return err
		}
		if count >= 24 {
			return Busy
		}
		out.ID, err = randomID(16)
		if err != nil {
			return err
		}
		out.PlanID = out.ID
		if r.ParentRun != "" {
			err = tx.QueryRow("SELECT plan_id,version FROM run WHERE id=? AND owner=? AND state='SUCCEEDED'", r.ParentRun, owner).Scan(&out.PlanID, &out.Version)
			if errors.Is(err, sql.ErrNoRows) {
				return Missing
			}
			if err != nil {
				return err
			}
			var latest int
			if err = tx.QueryRow("SELECT max(version) FROM plan_version WHERE plan_id=?", out.PlanID).Scan(&latest); err != nil {
				return err
			}
			if latest != out.Version {
				return Revision
			}
			out.Version++
		}
		_, err = tx.Exec(`INSERT INTO run(id,owner,key,digest,profile,request,state,plan_id,version,created)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, out.ID, owner, key, hash, body, string(req), out.State, out.PlanID, out.Version, out.Created)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO message VALUES(?,'user',?)", out.ID, r.Goal); err != nil {
			return err
		}
		return addEvent(tx, out.ID, "queued")
	})
	if replay && err == nil {
		out, err = s.Run(ctx, owner, out.ID)
	}
	return out, replay, err
}

type work struct {
	ID string
	R  Request
	P  Profile
}

func (s *Store) Claim(ctx context.Context) (work, error) {
	var w work
	err := txDo(ctx, s, func(tx *sql.Tx) error {
		var request, profile string
		err := tx.QueryRow("SELECT id,request,profile FROM run WHERE state='QUEUED' ORDER BY created,id LIMIT 1").Scan(&w.ID, &request, &profile)
		if errors.Is(err, sql.ErrNoRows) {
			return Missing
		}
		if err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(request), &w.R); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(profile), &w.P); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE run SET state='RUNNING' WHERE id=? AND state='QUEUED'", w.ID); err != nil {
			return err
		}
		return addEvent(tx, w.ID, "run_started")
	})
	return w, err
}

func (s *Store) Progress(ctx context.Context, id, kind string) error {
	return txDo(ctx, s, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRow("SELECT state FROM run WHERE id=?", id).Scan(&state); err != nil {
			return err
		}
		if state != "RUNNING" {
			return context.Canceled
		}
		return addEvent(tx, id, kind)
	})
}

func (s *Store) Finish(ctx context.Context, id, code string, plan *Plan) error {
	return txDo(ctx, s, func(tx *sql.Tx) error {
		state, kind := "FAILED", "failed"
		if plan != nil {
			state, kind = "SUCCEEDED", "completed"
		}
		res, err := tx.Exec("UPDATE run SET state=?,error=? WHERE id=? AND state='RUNNING'", state, code, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if plan != nil {
			raw, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO plan_version
				SELECT plan_id,version,id,? FROM run WHERE id=?`, string(raw), id); err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO message VALUES(?,'assistant',?)", id, plan.Status); err != nil {
				return err
			}
		}
		return addEvent(tx, id, kind)
	})
}

func (s *Store) Cancel(ctx context.Context, owner, id string) error {
	return txDo(ctx, s, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRow("SELECT state FROM run WHERE id=? AND owner=?", id, owner).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return Missing
		}
		if err != nil {
			return err
		}
		if state == "CANCELLED" {
			return nil
		}
		if state != "QUEUED" && state != "RUNNING" {
			return Revision
		}
		if _, err = tx.Exec("UPDATE run SET state='CANCELLED' WHERE id=?", id); err != nil {
			return err
		}
		return addEvent(tx, id, "cancelled")
	})
}

func (s *Store) recover() error {
	return txDo(context.Background(), s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO event
			SELECT r.id,coalesce(max(e.seq),0)+1,'interrupted',? FROM run r
			LEFT JOIN event e ON e.run=r.id WHERE r.state IN ('QUEUED','RUNNING') GROUP BY r.id`, now())
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE run SET state='INTERRUPTED',error='service_restarted' WHERE state IN ('QUEUED','RUNNING')")
		return err
	})
}

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var out Run
	var request string
	var result sql.NullString
	err := row.Scan(&out.ID, &out.PlanID, &out.Version, &out.State, &out.Created, &out.Error, &request, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return out, Missing
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(request), &out.Request); err != nil {
		return out, err
	}
	out.Goal = out.Request.Goal
	if result.Valid {
		out.Plan = new(Plan)
		err = json.Unmarshal([]byte(result.String), out.Plan)
	}
	return out, err
}

const selectRun = `SELECT r.id,r.plan_id,r.version,r.state,r.created,r.error,r.request,v.body
	FROM run r LEFT JOIN plan_version v ON v.run=r.id`

func (s *Store) Run(ctx context.Context, owner, id string) (Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, selectRun+" WHERE r.owner=? AND r.id=?", owner, id))
}

func (s *Store) History(ctx context.Context, owner string, offset, limit int) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, selectRun+" WHERE r.owner=? ORDER BY r.created DESC,r.id DESC LIMIT ? OFFSET ?", owner, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) Events(ctx context.Context, owner, id string, after int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.seq,e.kind,e.created FROM event e
		JOIN run r ON r.id=e.run WHERE r.owner=? AND r.id=? AND e.seq>? ORDER BY e.seq`, owner, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Seq, &e.Kind, &e.Created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
