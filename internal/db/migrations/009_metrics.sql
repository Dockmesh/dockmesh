-- Metrics time series per concept §15.4. Three-level downsampling
-- cascade so long history stays cheap.
--
--   metrics_raw :  24h at 30s resolution
--   metrics_1m  : 30 days at 1-minute aggregates
--   metrics_1h  :  1 year at 1-hour aggregates
--
-- Key is (container_name, ts) — we use name not id so metrics survive
-- the recreate that happens during auto-update.
CREATE TABLE IF NOT EXISTS metrics_raw (
    container_name TEXT NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (container_name, ts)
);
CREATE INDEX IF NOT EXISTS idx_metrics_raw_ts ON metrics_raw(ts);

CREATE TABLE IF NOT EXISTS metrics_1m (
    container_name TEXT NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (container_name, ts)
);
CREATE INDEX IF NOT EXISTS idx_metrics_1m_ts ON metrics_1m(ts);

CREATE TABLE IF NOT EXISTS metrics_1h (
    container_name TEXT NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (container_name, ts)
);
CREATE INDEX IF NOT EXISTS idx_metrics_1h_ts ON metrics_1h(ts);
