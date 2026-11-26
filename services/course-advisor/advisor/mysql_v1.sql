CREATE TABLE IF NOT EXISTS workspace (
    id VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS profile (
    owner VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    revision BIGINT NOT NULL,
    body JSON NOT NULL,
    FOREIGN KEY(owner) REFERENCES workspace(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS conversation (
    owner VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    FOREIGN KEY(owner) REFERENCES workspace(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS admission (
    id TINYINT PRIMARY KEY
) ENGINE=InnoDB;
INSERT INTO admission(id) VALUES(1) ON DUPLICATE KEY UPDATE id=1;
CREATE TABLE IF NOT EXISTS run (
    id VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    owner VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    request_key VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    profile JSON NOT NULL,
    request JSON NOT NULL,
    state ENUM('QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELLED','INTERRUPTED') NOT NULL,
    plan_id VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    version INT NOT NULL,
    created CHAR(30) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    error VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    available DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    lease_deadline DATETIME(6) NULL,
    heartbeat DATETIME(6) NULL,
    finished DATETIME(6) NULL,
    attempt INT NOT NULL DEFAULT 0,
    worker VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    UNIQUE KEY run_idem(owner,request_key),
    KEY run_history(owner,created DESC,id DESC),
    KEY run_active(owner,state),
    KEY run_queue(state,available,created,id),
    KEY run_lease(state,lease_deadline,id),
    FOREIGN KEY(owner) REFERENCES conversation(owner)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS message (
    run VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    role VARCHAR(20) NOT NULL,
    body TEXT NOT NULL,
    PRIMARY KEY(run,role),
    FOREIGN KEY(run) REFERENCES run(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS event (
    run VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    seq INT NOT NULL,
    kind VARCHAR(40) NOT NULL,
    created CHAR(30) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    PRIMARY KEY(run,seq),
    FOREIGN KEY(run) REFERENCES run(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS plan_version (
    plan_id VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    version INT NOT NULL,
    run VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
    body JSON NOT NULL,
    PRIMARY KEY(plan_id,version),
    FOREIGN KEY(run) REFERENCES run(id)
) ENGINE=InnoDB;
