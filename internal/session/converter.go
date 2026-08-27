package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"io"
	"time"
)

// Snapshot is the backend-independent storage representation of a session.
// Header is written first, followed by Entries in append order.
type Snapshot struct {
	Header  Header
	Entries []Entry
}

// Converter adapts an application-owned value to the session storage model.
// Implementations only describe the mapping; Convert performs the common
// compatibility validation.
type Converter[T any] interface {
	ConvertSession(context.Context, T) (Snapshot, error)
}

// ConverterFunc lets an ordinary function implement Converter.
type ConverterFunc[T any] func(context.Context, T) (Snapshot, error)

func (f ConverterFunc[T]) ConvertSession(ctx context.Context, source T) (Snapshot, error) {
	if f == nil {
		return Snapshot{}, ErrConverterRequired
	}
	return f(ctx, source)
}

var ErrConverterRequired = errors.New("session converter is required")

// Convert maps source with converter and validates that the result can be
// consumed by this package and its Persistence implementations.
func Convert[T any](ctx context.Context, source T, converter Converter[T]) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if converter == nil {
		return Snapshot{}, ErrConverterRequired
	}
	snapshot, err := converter.ConvertSession(ctx, source)
	if err != nil {
		return Snapshot{}, fmt.Errorf("convert session: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("convert session: %w", err)
	}
	return snapshot, nil
}

// Validate checks the invariants shared by Persistence implementations.
func (s Snapshot) Validate() error {
	if s.Header.Type != "session" {
		return errors.New("invalid session header type")
	}
	if s.Header.Version != Version {
		return fmt.Errorf("unsupported session version %d (want %d)", s.Header.Version, Version)
	}
	if s.Header.ID == "" {
		return errors.New("session id is required")
	}
	if err := validateTimestamp("session header", s.Header.Timestamp); err != nil {
		return err
	}
	if err := validateJSONValue("session initial state", s.Header.InitialState, true); err != nil {
		return err
	}
	seen := make(map[EntryID]struct{}, len(s.Entries))
	for i, entry := range s.Entries {
		where := fmt.Sprintf("session entry %d", i)
		if entry.Type == "" {
			return fmt.Errorf("%s type is required", where)
		}
		if entry.ID == "" {
			return fmt.Errorf("%s id is required", where)
		}
		if _, exists := seen[entry.ID]; exists {
			return fmt.Errorf("duplicate session entry id %q", entry.ID)
		}
		if entry.ParentID != nil {
			if _, exists := seen[*entry.ParentID]; !exists {
				return fmt.Errorf("%s %q references unknown or later parent %q", where, entry.ID, *entry.ParentID)
			}
		}
		if err := validateTimestamp(where, entry.Timestamp); err != nil {
			return err
		}
		for _, value := range []struct {
			name  string
			value json.RawMessage
		}{
			{name: "details", value: entry.Details},
			{name: "data", value: entry.Data},
			{name: "content", value: entry.Content},
			{name: "state", value: entry.State},
		} {
			if err := validateJSONValue(where+" "+value.name, value.value, false); err != nil {
				return err
			}
		}
		if _, err := json.Marshal(entry); err != nil {
			return fmt.Errorf("encode %s %q: %w", where, entry.ID, err)
		}
		seen[entry.ID] = struct{}{}
	}
	if _, err := json.Marshal(s.Header); err != nil {
		return fmt.Errorf("encode session header: %w", err)
	}
	return nil
}

func validateTimestamp(where, value string) error {
	if value == "" {
		return fmt.Errorf("%s timestamp is required", where)
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%s has invalid timestamp %q: %w", where, value, err)
	}
	return nil
}

func validateJSONValue(where string, value json.RawMessage, required bool) error {
	if len(value) == 0 {
		if required {
			return fmt.Errorf("%s is required", where)
		}
		return nil
	}
	if !value.IsValid() {
		return fmt.Errorf("%s is invalid JSON", where)
	}
	return nil
}

// WriteTo writes the snapshot as JSONL v4. It validates the complete snapshot
// before writing, so validation errors never leave a partial document.
func (s Snapshot) WriteTo(w io.Writer) (int64, error) {
	if w == nil {
		return 0, errors.New("session snapshot writer is required")
	}
	data, err := s.MarshalJSONL()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(data)
	if err != nil {
		return int64(n), fmt.Errorf("write session snapshot: %w", err)
	}
	if n != len(data) {
		return int64(n), io.ErrShortWrite
	}
	return int64(n), nil
}

// MarshalJSONL returns the JSONL v4 encoding of the snapshot.
func (s Snapshot) MarshalJSONL() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	values := make([]any, 0, len(s.Entries)+1)
	values = append(values, s.Header)
	for i := range s.Entries {
		values = append(values, s.Entries[i])
	}
	for i, value := range values {
		line, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode session JSONL line %d: %w", i+1, err)
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// Store creates a snapshot in backend and returns a Manager that can continue
// appending to it. The Persistence must represent a new session destination.
func (s Snapshot) Store(persistence Persistence) (*Manager, error) {
	if persistence == nil {
		return nil, errors.New("session persistence is required")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if err := persistence.Create(s.Header, append([]Entry(nil), s.Entries...)); err != nil {
		return nil, fmt.Errorf("create converted session: %w", err)
	}
	return Restore(s.Header, s.Entries, persistence)
}

// Restore opens a snapshot that the caller has already loaded and initialized
// in persistence. Use Store when importing into a new Persistence destination.
func (s Snapshot) Restore(persistence Persistence) (*Manager, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return Restore(s.Header, s.Entries, persistence)
}
