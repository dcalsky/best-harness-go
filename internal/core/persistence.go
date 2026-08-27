package core

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	store "github.com/dcalsky/best-harness-go/internal/session"
)

// NewMemoryPersistence creates an ephemeral Persistence that never accesses
// the filesystem. Its contents live only as long as the owning Manager.
func NewMemoryPersistence() Persistence { return &memoryPersistence{} }

type memoryPersistence struct {
	mu      sync.Mutex
	header  SessionHeader
	entries []SessionEntry
	closed  bool
}

func (p *memoryPersistence) Location(header SessionHeader) string {
	return "memory-session://" + header.ID
}

func (p *memoryPersistence) Create(header SessionHeader, entries []SessionEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return store.ErrClosed
	}
	p.header = header
	p.entries = append([]SessionEntry(nil), entries...)
	return nil
}

func (p *memoryPersistence) Append(entry SessionEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return store.ErrClosed
	}
	p.entries = append(p.entries, entry)
	return nil
}

func (*memoryPersistence) Fork() Persistence { return &memoryPersistence{} }
func (p *memoryPersistence) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

// NewFilePersistence creates a Persistence that stores each session as one
// generated JSONL file inside directory. Callers select a directory, never an
// individual session file.
func NewFilePersistence(directory string) (Persistence, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("session persistence directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	return &filePersistence{directory: absolute}, nil
}

// OpenFileSession restores one JSONL session previously created by a directory
// FilePersistence.
func OpenFileSession(path string) (*SessionManager, error) { return openFileManager(path) }

// ListFileSessions lists JSONL sessions created inside directory.
func ListFileSessions(ctx context.Context, directory string) ([]SessionInfo, error) {
	return listFileSessions(ctx, directory)
}

// ResumeLatestFileSession restores the most recently modified matching session.
func ResumeLatestFileSession(ctx context.Context, directory, cwd string) (*SessionManager, error) {
	return resumeLatestFileManager(ctx, directory, cwd)
}

type filePersistence struct {
	mu        sync.Mutex
	directory string
	path      string
	file      *os.File
	unlock    func()
	created   bool
	closed    bool
}

var fileWriterLocks sync.Map

func (p *filePersistence) Location(header SessionHeader) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == "" {
		stamp := strings.NewReplacer(":", "-", ".", "-").Replace(header.Timestamp)
		p.path = filepath.Join(p.directory, stamp+"_"+header.ID+".jsonl")
	}
	return p.path
}

func (p *filePersistence) Create(header SessionHeader, entries []SessionEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return store.ErrClosed
	}
	if p.created {
		return errors.New("session persistence already exists")
	}
	if p.path == "" {
		stamp := strings.NewReplacer(":", "-", ".", "-").Replace(header.Timestamp)
		p.path = filepath.Join(p.directory, stamp+"_"+header.ID+".jsonl")
	}
	unlock, err := lockFilePersistence(p.path)
	if err != nil {
		return err
	}
	p.unlock = unlock
	if err := os.MkdirAll(p.directory, 0700); err != nil {
		p.release()
		return err
	}
	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		p.release()
		return err
	}
	p.file = f
	if err := writeJSONLine(f, header); err != nil {
		p.release()
		return err
	}
	for _, entry := range entries {
		if err := writeJSONLine(f, entry); err != nil {
			p.release()
			return err
		}
	}
	p.created = true
	return nil
}

func (p *filePersistence) Append(entry SessionEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return store.ErrClosed
	}
	if !p.created || p.file == nil {
		return errors.New("session persistence is not open")
	}
	return writeJSONLine(p.file, entry)
}

func (p *filePersistence) Fork() Persistence {
	return &filePersistence{directory: p.directory}
}

func (p *filePersistence) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.release()
}

func (p *filePersistence) release() error {
	var err error
	if p.file != nil {
		err = p.file.Close()
		p.file = nil
	}
	if p.unlock != nil {
		p.unlock()
		p.unlock = nil
	}
	return err
}

func lockFilePersistence(path string) (func(), error) {
	value, _ := fileWriterLocks.LoadOrStore(path, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		return nil, store.ErrWriterActive
	}
	return lock.Unlock, nil
}

func writeJSONLine(file *os.File, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func openFileManager(path string) (*SessionManager, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = absolute
	unlock, err := lockFilePersistence(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		unlock()
		return nil, err
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		last := bytes.LastIndexByte(data, '\n')
		if last < 0 {
			unlock()
			return nil, errors.New("session has no complete JSONL line")
		}
		data = data[:last+1]
		if err := os.Truncate(path, int64(len(data))); err != nil {
			unlock()
			return nil, err
		}
	}
	snapshot, err := decodeFileSnapshot(data)
	if err != nil {
		unlock()
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		unlock()
		return nil, err
	}
	persistence := &filePersistence{directory: filepath.Dir(path), path: path, file: file, unlock: unlock, created: true}
	manager, err := store.Restore(snapshot.Header, snapshot.Entries, persistence)
	if err != nil {
		persistence.Close()
		return nil, err
	}
	return manager, nil
}

func decodeFileSnapshot(data []byte) (SessionSnapshot, error) {
	var snapshot SessionSnapshot
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &kind) != nil {
			continue
		}
		if kind.Type == "session" && snapshot.Header.ID == "" {
			if json.Unmarshal(line, &snapshot.Header) != nil {
				continue
			}
			continue
		}
		var entry SessionEntry
		if json.Unmarshal(line, &entry) == nil && entry.ID != "" {
			snapshot.Entries = append(snapshot.Entries, entry)
		}
	}
	if err := snapshot.Validate(); err != nil {
		return SessionSnapshot{}, err
	}
	return snapshot, nil
}

func listFileSessions(ctx context.Context, directory string) ([]SessionInfo, error) {
	files, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(directory, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info := SessionInfo{Location: path}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64<<10), 16<<20)
		for scanner.Scan() {
			var kind struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(scanner.Bytes(), &kind) != nil {
				continue
			}
			if kind.Type == "session" {
				var header SessionHeader
				if json.Unmarshal(scanner.Bytes(), &header) == nil {
					info.ID = header.ID
					info.Cwd = header.Cwd
					info.Created, _ = time.Parse(time.RFC3339Nano, header.Timestamp)
				}
				continue
			}
			var entry SessionEntry
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			if entry.Type == "message" && entry.Message != nil {
				info.MessageCount++
				text := entry.Message.Text()
				if info.FirstMessage == "" && entry.Message.Role == RoleUser {
					info.FirstMessage = text
				}
				if text != "" {
					info.AllMessagesText += text + "\n"
				}
			}
			if entry.Type == "session_info" {
				info.Name = entry.Name
			}
		}
		if stat, err := file.Info(); err == nil {
			info.Modified = stat.ModTime()
		}
		if info.ID != "" {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func resumeLatestFileManager(ctx context.Context, directory, cwd string) (*SessionManager, error) {
	infos, err := listFileSessions(ctx, directory)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if cwd == "" || info.Cwd == cwd {
			return openFileManager(info.Location)
		}
	}
	return nil, io.EOF
}
