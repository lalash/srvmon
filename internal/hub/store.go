package hub

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned by every lookup that addresses a single row.
var ErrNotFound = errors.New("not found")

// Server is a monitored host as the hub knows it between pushes. Everything
// below Token is last-reported metadata, refreshed on each snapshot.
type Server struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Token        string `json:"token,omitempty"`
	Tag          string `json:"tag"`
	Sort         int    `json:"sort"`
	CreatedAt    int64  `json:"createdAt"`
	LastSeen     int64  `json:"lastSeen"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Kernel       string `json:"kernel"`
	AgentVersion string `json:"agentVersion"`
	IPv4         string `json:"ipv4"`
	IPv6         string `json:"ipv6"`
	// UpdateTo is the agent version the operator asked this host to move to.
	// It is handed to the agent on its next push and cleared once it reports
	// having arrived, so a failed update keeps retrying rather than going quiet.
	UpdateTo string `json:"updateTo"`
}

// Sample is one persisted history point; percentages are 0-100 and rates are
// bytes per second.
type Sample struct {
	TS      int64   `json:"t"`
	Cpu     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	Swap    float64 `json:"swap"`
	Disk    float64 `json:"disk"`
	NetUp   float64 `json:"netUp"`
	NetDown float64 `json:"netDown"`
	Tcp     float64 `json:"tcp"`
	Udp     float64 `json:"udp"`
	Load1   float64 `json:"load1"`
}

// AlertEvent records a threshold crossing in either direction.
type AlertEvent struct {
	ID         int64   `json:"id"`
	ServerID   int64   `json:"serverId"`
	ServerName string  `json:"serverName"`
	Kind       string  `json:"kind"`
	State      string  `json:"state"`
	Value      float64 `json:"value"`
	TS         int64   `json:"t"`
}

// Store is the hub's SQLite persistence layer.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS servers (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL,
  token         TEXT NOT NULL UNIQUE,
  tag           TEXT NOT NULL DEFAULT '',
  sort          INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL DEFAULT 0,
  hostname      TEXT NOT NULL DEFAULT '',
  os            TEXT NOT NULL DEFAULT '',
  arch          TEXT NOT NULL DEFAULT '',
  kernel        TEXT NOT NULL DEFAULT '',
  agent_version TEXT NOT NULL DEFAULT '',
  ipv4          TEXT NOT NULL DEFAULT '',
  ipv6          TEXT NOT NULL DEFAULT '',
  update_to     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS samples (
  server_id INTEGER NOT NULL,
  ts        INTEGER NOT NULL,
  cpu       REAL NOT NULL DEFAULT 0,
  mem       REAL NOT NULL DEFAULT 0,
  swap      REAL NOT NULL DEFAULT 0,
  disk      REAL NOT NULL DEFAULT 0,
  net_up    REAL NOT NULL DEFAULT 0,
  net_down  REAL NOT NULL DEFAULT 0,
  tcp       REAL NOT NULL DEFAULT 0,
  udp       REAL NOT NULL DEFAULT 0,
  load1     REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (server_id, ts)
);
CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id   INTEGER NOT NULL,
  server_name TEXT NOT NULL,
  kind        TEXT NOT NULL,
  state       TEXT NOT NULL,
  value       REAL NOT NULL,
  ts          INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alert_events_ts ON alert_events(ts);
`

// OpenStore opens (and creates on first run) the hub database. A single
// connection keeps SQLite writers from ever contending; the workload is a
// handful of small writes per minute.
func OpenStore(path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}
	migrate(db)
	return &Store{db: db}, nil
}

// migrate adds columns to databases created by an earlier version. CREATE TABLE
// IF NOT EXISTS leaves an existing table alone, so new columns need their own
// statement; "duplicate column" just means this one already ran.
func migrate(db *sql.DB) {
	statements := []string{
		`ALTER TABLE servers ADD COLUMN update_to TEXT NOT NULL DEFAULT ''`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			log.Printf("migration %q: %v", statement, err)
		}
	}
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const serverColumns = `id, name, token, tag, sort, created_at, last_seen,
	hostname, os, arch, kernel, agent_version, ipv4, ipv6, update_to`

func scanServer(row interface{ Scan(...any) error }) (*Server, error) {
	var v Server
	err := row.Scan(&v.ID, &v.Name, &v.Token, &v.Tag, &v.Sort, &v.CreatedAt, &v.LastSeen,
		&v.Hostname, &v.OS, &v.Arch, &v.Kernel, &v.AgentVersion, &v.IPv4, &v.IPv6, &v.UpdateTo)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ListServers returns every server ordered by the manual sort key, then name.
func (s *Store) ListServers() ([]*Server, error) {
	rows, err := s.db.Query(`SELECT ` + serverColumns + ` FROM servers ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Server
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetServer looks a server up by id.
func (s *Store) GetServer(id int64) (*Server, error) {
	v, err := scanServer(s.db.QueryRow(`SELECT `+serverColumns+` FROM servers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// GetServerByToken resolves the agent credential presented on push.
func (s *Store) GetServerByToken(token string) (*Server, error) {
	v, err := scanServer(s.db.QueryRow(`SELECT `+serverColumns+` FROM servers WHERE token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// CreateServer registers a host and mints its agent token.
func (s *Store) CreateServer(name, tag string) (*Server, error) {
	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO servers (name, token, tag, created_at) VALUES (?, ?, ?, ?)`,
		name, token, tag, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetServer(id)
}

// RequestAgentUpdate marks servers whose agent should move to version. Passing
// a single id targets one host; id 0 targets every host not already there.
func (s *Store) RequestAgentUpdate(id int64, version string) (int64, error) {
	var res sql.Result
	var err error
	if id > 0 {
		res, err = s.db.Exec(`UPDATE servers SET update_to = ? WHERE id = ?`, version, id)
	} else {
		res, err = s.db.Exec(`UPDATE servers SET update_to = ? WHERE agent_version <> ?`, version, version)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearAgentUpdate is called once an agent reports the version it was sent to.
func (s *Store) ClearAgentUpdate(id int64) error {
	_, err := s.db.Exec(`UPDATE servers SET update_to = '' WHERE id = ?`, id)
	return err
}

// UpdateServer changes the operator-editable fields.
func (s *Store) UpdateServer(id int64, name, tag string, sort int) error {
	_, err := s.db.Exec(`UPDATE servers SET name = ?, tag = ?, sort = ? WHERE id = ?`, name, tag, sort, id)
	return err
}

// RotateToken issues a fresh credential, immediately invalidating the old one.
func (s *Store) RotateToken(id int64) (string, error) {
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`UPDATE servers SET token = ? WHERE id = ?`, token, id); err != nil {
		return "", err
	}
	return token, nil
}

// DeleteServer removes a server together with its history.
func (s *Store) DeleteServer(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM samples WHERE server_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM servers WHERE id = ?`, id)
	return err
}

// TouchServer records the metadata carried by a snapshot plus the arrival time.
func (s *Store) TouchServer(id int64, seenAt int64, hostname, os, arch, kernel, agent, ipv4, ipv6 string) error {
	_, err := s.db.Exec(`UPDATE servers SET last_seen = ?, hostname = ?, os = ?, arch = ?,
		kernel = ?, agent_version = ?, ipv4 = ?, ipv6 = ? WHERE id = ?`,
		seenAt, hostname, os, arch, kernel, agent, ipv4, ipv6, id)
	return err
}

// InsertSample appends one history point, ignoring a duplicate timestamp.
func (s *Store) InsertSample(serverID int64, v Sample) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO samples
		(server_id, ts, cpu, mem, swap, disk, net_up, net_down, tcp, udp, load1)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serverID, v.TS, v.Cpu, v.Mem, v.Swap, v.Disk, v.NetUp, v.NetDown, v.Tcp, v.Udp, v.Load1)
	return err
}

// History averages samples into fixed-width buckets so a 7-day chart returns a
// few hundred points instead of tens of thousands.
func (s *Store) History(serverID, from int64, bucketSeconds int) ([]Sample, error) {
	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	rows, err := s.db.Query(`SELECT (ts / ?) * ? AS bucket,
			avg(cpu), avg(mem), avg(swap), avg(disk),
			avg(net_up), avg(net_down), avg(tcp), avg(udp), avg(load1)
		FROM samples WHERE server_id = ? AND ts >= ?
		GROUP BY ts / ? ORDER BY bucket`,
		bucketSeconds, bucketSeconds, serverID, from, bucketSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Sample{}
	for rows.Next() {
		var v Sample
		if err := rows.Scan(&v.TS, &v.Cpu, &v.Mem, &v.Swap, &v.Disk,
			&v.NetUp, &v.NetDown, &v.Tcp, &v.Udp, &v.Load1); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PruneSamples drops history older than the cutoff.
func (s *Store) PruneSamples(before int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM samples WHERE ts < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Setting reads a stored setting, returning fallback when it was never set.
func (s *Store) Setting(key, fallback string) string {
	var v string
	if err := s.db.QueryRow(`SELECT v FROM settings WHERE k = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

// SetSetting writes one setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (k, v) VALUES (?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, value)
	return err
}

// AddAlertEvent stores a threshold crossing for the events feed.
func (s *Store) AddAlertEvent(e AlertEvent) error {
	_, err := s.db.Exec(`INSERT INTO alert_events (server_id, server_name, kind, state, value, ts)
		VALUES (?, ?, ?, ?, ?, ?)`, e.ServerID, e.ServerName, e.Kind, e.State, e.Value, e.TS)
	return err
}

// AlertEvents returns the most recent events, newest first.
func (s *Store) AlertEvents(limit int) ([]AlertEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, server_id, server_name, kind, state, value, ts
		FROM alert_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AlertEvent{}
	for rows.Next() {
		var e AlertEvent
		if err := rows.Scan(&e.ID, &e.ServerID, &e.ServerName, &e.Kind, &e.State, &e.Value, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAlertEvents keeps the events table from growing without bound.
func (s *Store) PruneAlertEvents(keep int) error {
	_, err := s.db.Exec(`DELETE FROM alert_events WHERE id NOT IN
		(SELECT id FROM alert_events ORDER BY id DESC LIMIT ?)`, keep)
	return err
}

// NewToken mints a URL-safe 32-character random credential.
func NewToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
