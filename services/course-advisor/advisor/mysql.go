package advisor

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

//go:embed mysql_v1.sql
var mysqlMigration string

const maxAttempts = 3

// OpenMySQL permits only the separately provisioned synthetic lab database.
func OpenMySQL(ctx context.Context, raw, marker string) (*Store, error) {
	cfg, err := mysql.ParseDSN(raw)
	if err != nil || cfg == nil || cfg.Net != "tcp" || cfg.DBName != "advisor_phase6_lab" ||
		cfg.User != "advisor_lab" || cfg.Passwd == "" || !shaRE.MatchString(marker) {
		return nil, errors.New("invalid isolated MySQL configuration")
	}
	if !labAddress(cfg.Addr, "mysql", 3306) {
		return nil, errors.New("refusing non-lab MySQL endpoint")
	}
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 2*time.Second, 3*time.Second, 3*time.Second
	// Do not accept connection parameters that change session SQL semantics.
	cfg.Params = map[string]string{"time_zone": "'+00:00'"}
	c, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("invalid MySQL connector")
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(6)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = migrateMySQL(ctx, db, marker); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, distributed: true, lease: 6 * time.Second}, nil
}

func labAddress(addr, service string, defaultPort int) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 {
		return false
	}
	return (host == "127.0.0.1" && n != defaultPort) || (host == service && n == defaultPort)
}

func migrateMySQL(ctx context.Context, db *sql.DB, marker string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		var serverError *mysql.MySQLError
		if errors.As(err, &serverError) {
			return fmt.Errorf("isolated MySQL connection rejected (server code %d)", serverError.Number)
		}
		return errors.New("isolated MySQL is unavailable")
	}
	defer conn.Close()
	var identity string
	if err = conn.QueryRowContext(ctx, "SELECT token FROM phase6_lab_identity WHERE id=1").Scan(&identity); err != nil || identity != marker {
		return errors.New("isolated MySQL identity cannot be verified")
	}
	var family string
	if err = conn.QueryRowContext(ctx, "SELECT VERSION()").Scan(&family); err != nil || !strings.HasPrefix(family, "8.4.") {
		return errors.New("MySQL lab requires verified 8.4 server")
	}
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('course_advisor_phase6_migrate', 2)").Scan(&locked); err != nil || locked != 1 {
		return errors.New("advisor migration lock unavailable")
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = conn.ExecContext(release, "DO RELEASE_LOCK('course_advisor_phase6_migrate')")
	}()
	// Each DDL can implicitly commit. Creation is reentrant; record the version last.
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS advisor_schema(
		id TINYINT PRIMARY KEY, version INT NOT NULL) ENGINE=InnoDB`); err != nil {
		return err
	}
	var version int
	err = conn.QueryRowContext(ctx, "SELECT version FROM advisor_schema WHERE id=1").Scan(&version)
	if err == nil {
		if version != 1 {
			return errors.New("unsupported MySQL schema version")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, line := range strings.Split(mysqlMigration, ";") {
		if q := strings.TrimSpace(line); q != "" {
			if _, err = conn.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO advisor_schema(id,version) VALUES(1,1)")
	return err
}

func (s *Store) keyColumn() string {
	if s.distributed {
		return "request_key"
	}
	return "key"
}
func duplicateKey(err error) bool {
	var duplicate *mysql.MySQLError
	return errors.As(err, &duplicate) && duplicate.Number == 1062
}
func (s *Store) lockSuffix() string {
	if s.distributed {
		return " FOR UPDATE"
	}
	return ""
}
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) Distributed() bool              { return s.distributed }

func validLease(tx *sql.Tx, w work) error {
	var marker int
	err := tx.QueryRow(`SELECT 1 FROM run WHERE id=? AND state='RUNNING' AND attempt=? AND worker=?
		AND lease_deadline>UTC_TIMESTAMP(6) FOR UPDATE`, w.ID, w.Attempt, w.Worker).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return LostLease
	}
	return err
}

func (s *Store) Heartbeat(ctx context.Context, w work) error {
	if !s.distributed {
		return nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE run SET heartbeat=UTC_TIMESTAMP(6),
		lease_deadline=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))
		WHERE id=? AND state='RUNNING' AND attempt=? AND worker=? AND lease_deadline>UTC_TIMESTAMP(6)`,
		s.lease.Microseconds(), w.ID, w.Attempt, w.Worker)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return LostLease
	}
	return nil
}

func releaseLease(tx *sql.Tx, id string, attempt int) error {
	if attempt >= maxAttempts {
		_, err := tx.Exec(`UPDATE run SET state='INTERRUPTED',error='lease_attempts_exhausted',
			finished=UTC_TIMESTAMP(6),lease_deadline=NULL WHERE id=?`, id)
		if err != nil {
			return err
		}
		return addEvent(tx, id, "interrupted")
	}
	_, err := tx.Exec(`UPDATE run SET state='QUEUED',worker='',lease_deadline=NULL,
		available=TIMESTAMPADD(SECOND, ?, UTC_TIMESTAMP(6)) WHERE id=?`, attempt, id)
	if err != nil {
		return err
	}
	return addEvent(tx, id, "requeued")
}

func (s *Store) Reap(ctx context.Context) (bool, error) {
	if !s.distributed {
		return false, nil
	}
	found := false
	err := txDo(ctx, s, func(tx *sql.Tx) error {
		var id string
		var attempt int
		err := tx.QueryRow(`SELECT id,attempt FROM run WHERE state='RUNNING' AND lease_deadline<=UTC_TIMESTAMP(6)
			ORDER BY lease_deadline,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &attempt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return releaseLease(tx, id, attempt)
	})
	return found, err
}

func (s *Store) Abandon(ctx context.Context, w work) error {
	if !s.distributed {
		return nil
	}
	return txDo(ctx, s, func(tx *sql.Tx) error {
		if err := validLease(tx, w); err != nil {
			return err
		}
		return releaseLease(tx, w.ID, w.Attempt)
	})
}
