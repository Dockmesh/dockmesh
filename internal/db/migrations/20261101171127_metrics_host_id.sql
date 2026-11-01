-- metrics_host_id
-- Container series are keyed by host as well as container name, so
-- same-named containers on two hosts stay apart. Existing rows were all
-- collected on the server, which is host 'local'. cpu_cores is the
-- number of cores the sample was taken on (0 for rows from before this
-- migration); cpu_percent stays 100 per busy core.
CREATE TABLE metrics_raw_new (
    host_id        TEXT    NOT NULL DEFAULT 'local',
    container_name TEXT    NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    cpu_cores      INTEGER NOT NULL DEFAULT 0,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (host_id, container_name, ts)
);
INSERT INTO metrics_raw_new
    (host_id, container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
SELECT 'local', container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write
  FROM metrics_raw;
DROP TABLE metrics_raw;
ALTER TABLE metrics_raw_new RENAME TO metrics_raw;
CREATE INDEX idx_metrics_raw_ts ON metrics_raw(ts);

CREATE TABLE metrics_1m_new (
    host_id        TEXT    NOT NULL DEFAULT 'local',
    container_name TEXT    NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    cpu_cores      INTEGER NOT NULL DEFAULT 0,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (host_id, container_name, ts)
);
INSERT INTO metrics_1m_new
    (host_id, container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
SELECT 'local', container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write
  FROM metrics_1m;
DROP TABLE metrics_1m;
ALTER TABLE metrics_1m_new RENAME TO metrics_1m;
CREATE INDEX idx_metrics_1m_ts ON metrics_1m(ts);

CREATE TABLE metrics_1h_new (
    host_id        TEXT    NOT NULL DEFAULT 'local',
    container_name TEXT    NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    cpu_cores      INTEGER NOT NULL DEFAULT 0,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (host_id, container_name, ts)
);
INSERT INTO metrics_1h_new
    (host_id, container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
SELECT 'local', container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write
  FROM metrics_1h;
DROP TABLE metrics_1h;
ALTER TABLE metrics_1h_new RENAME TO metrics_1h;
CREATE INDEX idx_metrics_1h_ts ON metrics_1h(ts);

-- Host series: cpu_percent is 0..100 of the whole host.
CREATE TABLE host_metrics_raw (
    host_id     TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    cpu_percent REAL    NOT NULL,
    cpu_cores   INTEGER NOT NULL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    PRIMARY KEY (host_id, ts)
);
CREATE INDEX idx_host_metrics_raw_ts ON host_metrics_raw(ts);

CREATE TABLE host_metrics_1m (
    host_id     TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    cpu_percent REAL    NOT NULL,
    cpu_cores   INTEGER NOT NULL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    PRIMARY KEY (host_id, ts)
);
CREATE INDEX idx_host_metrics_1m_ts ON host_metrics_1m(ts);

CREATE TABLE host_metrics_1h (
    host_id     TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    cpu_percent REAL    NOT NULL,
    cpu_cores   INTEGER NOT NULL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    PRIMARY KEY (host_id, ts)
);
CREATE INDEX idx_host_metrics_1h_ts ON host_metrics_1h(ts);
