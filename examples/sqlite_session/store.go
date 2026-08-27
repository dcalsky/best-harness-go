package sqlitesession

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"encoding/json"

	"github.com/dcalsky/best-harness-go"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type Store struct {
	DB       *sql.DB
	Location string
}

func OpenDatabase(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", absolute)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &Store{DB: db, Location: absolute}, nil
}

func (s *Store) Initialize(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("sqlite session database is required")
	}
	_, err := s.DB.ExecContext(ctx, schema)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *Store) NewManager(opts harness.PersistenceOptions) (*harness.SessionManager, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return harness.NewSessionManager(&backend{store: s}, opts)
}

func (s *Store) Open(ctx context.Context, id string) (*harness.SessionManager, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	b := &backend{store: s, id: id}
	if err := b.acquire(id); err != nil {
		return nil, err
	}
	var raw []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT header_json FROM sdk_sessions WHERE id = ?`, id).Scan(&raw); err != nil {
		b.Close()
		return nil, err
	}
	var header harness.SessionHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		b.Close()
		return nil, fmt.Errorf("decode session header: %w", err)
	}
	entries, err := s.loadEntries(ctx, id)
	if err != nil {
		b.Close()
		return nil, err
	}
	b.created = true
	b.next = len(entries)
	m, err := harness.RestoreSessionManager(header, entries, b)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) ResumeLatest(ctx context.Context, cwd string) (*harness.SessionManager, error) {
	infos, err := s.List(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, io.EOF
	}
	return s.Open(ctx, infos[0].ID)
}

func (s *Store) List(ctx context.Context, cwd string) ([]harness.SessionInfo, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	query := `SELECT id, cwd, name, created_at, modified_at FROM sdk_sessions`
	var args []any
	if cwd != "" {
		query += ` WHERE cwd = ?`
		args = append(args, cwd)
	}
	query += ` ORDER BY modified_at DESC, id DESC`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []harness.SessionInfo
	for rows.Next() {
		var info harness.SessionInfo
		var created, modified string
		if err := rows.Scan(&info.ID, &info.Cwd, &info.Name, &created, &modified); err != nil {
			return nil, err
		}
		info.Location = s.location(info.ID)
		info.Created, _ = time.Parse(time.RFC3339Nano, created)
		info.Modified, _ = time.Parse(time.RFC3339Nano, modified)
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		entries, err := s.loadEntries(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type != "message" || entry.Message == nil {
				continue
			}
			out[i].MessageCount++
			text := entry.Message.Text()
			if out[i].FirstMessage == "" && entry.Message.Role == "user" {
				out[i].FirstMessage = text
			}
			if text != "" {
				out[i].AllMessagesText += text + "\n"
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func (s *Store) loadEntries(ctx context.Context, id string) ([]harness.SessionEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT entry_json
FROM sdk_session_entries
WHERE session_id = ?
ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []harness.SessionEntry
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var entry harness.SessionEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("decode session entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) validate() error {
	if s == nil || s.DB == nil {
		return errors.New("sqlite session database is required")
	}
	if strings.TrimSpace(s.Location) == "" {
		return errors.New("sqlite session location is required")
	}
	return nil
}

func (s *Store) location(id string) string {
	return "sqlite-session://" + filepath.ToSlash(s.Location) + "#" + id
}

type backend struct {
	mu       sync.Mutex
	store    *Store
	id       string
	next     int
	created  bool
	acquired bool
	unlock   func()
}

var writerLocks sync.Map

func (b *backend) Location(header harness.SessionHeader) string { return b.store.location(header.ID) }
func (b *backend) Fork() harness.Persistence                    { return &backend{store: b.store} }

func (b *backend) Create(header harness.SessionHeader, entries []harness.SessionEntry) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.created {
		return errors.New("sqlite session already exists")
	}
	if err := b.acquire(header.ID); err != nil {
		return err
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		b.release()
		return err
	}
	tx, err := b.store.DB.Begin()
	if err != nil {
		b.release()
		return err
	}
	defer tx.Rollback()
	modified := header.Timestamp
	if len(entries) > 0 {
		modified = entries[len(entries)-1].Timestamp
	}
	name := latestName(entries)
	if _, err := tx.Exec(`
INSERT INTO sdk_sessions(id,version,header_json,cwd,parent_session,name,created_at,modified_at)
VALUES(?,?,?,?,?,?,?,?)`, header.ID, header.Version, headerJSON, header.Cwd, header.ParentSession, name, header.Timestamp, modified); err != nil {
		b.release()
		return err
	}
	for position, entry := range entries {
		if err := insertEntry(tx, header.ID, position, entry); err != nil {
			b.release()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		b.release()
		return err
	}
	b.id = header.ID
	b.next = len(entries)
	b.created = true
	return nil
}

func (b *backend) Append(entry harness.SessionEntry) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.created || !b.acquired {
		return errors.New("sqlite session is not open")
	}
	tx, err := b.store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertEntry(tx, b.id, b.next, entry); err != nil {
		return err
	}
	if entry.Type == "session_info" {
		_, err = tx.Exec(`UPDATE sdk_sessions SET name = ?, modified_at = ? WHERE id = ?`, entry.Name, entry.Timestamp, b.id)
	} else {
		_, err = tx.Exec(`UPDATE sdk_sessions SET modified_at = ? WHERE id = ?`, entry.Timestamp, b.id)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	b.next++
	return nil
}

func (b *backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.release()
	return nil
}

func (b *backend) acquire(id string) error {
	if b.acquired {
		return nil
	}
	key := b.store.Location + "\x00" + id
	value, _ := writerLocks.LoadOrStore(key, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		return harness.ErrSessionWriterActive
	}
	b.id = id
	b.acquired = true
	b.unlock = lock.Unlock
	return nil
}

func (b *backend) release() {
	if b.unlock != nil {
		b.unlock()
	}
	b.unlock = nil
	b.acquired = false
}

func insertEntry(tx *sql.Tx, sessionID string, position int, entry harness.SessionEntry) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
INSERT INTO sdk_session_entries(session_id,position,entry_id,entry_type,entry_json,created_at)
VALUES(?,?,?,?,?,?)`, sessionID, position, entry.ID, entry.Type, raw, entry.Timestamp)
	return err
}

func latestName(entries []harness.SessionEntry) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "session_info" {
			return entries[i].Name
		}
	}
	return ""
}
