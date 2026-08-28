// Package session stores append-only v4 conversation and state trees.
package session

import (
	"context"
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/run"
)

const Version = 4

type EntryID string
type Header struct {
	Type          string          `json:"type"`
	Version       int             `json:"version"`
	ID            string          `json:"id"`
	Timestamp     string          `json:"timestamp"`
	Cwd           string          `json:"cwd"`
	ParentSession string          `json:"parentSession,omitempty"`
	InitialState  json.RawMessage `json:"initialState"`
}
type Entry struct {
	Type             string           `json:"type"`
	ID               EntryID          `json:"id"`
	ParentID         *EntryID         `json:"parentId"`
	Timestamp        string           `json:"timestamp"`
	Message          *message.Message `json:"message,omitempty"`
	ThinkingLevel    string           `json:"thinkingLevel,omitempty"`
	Provider         string           `json:"provider,omitempty"`
	ModelID          string           `json:"modelId,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	FirstKeptEntryID EntryID          `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int64            `json:"tokensBefore,omitempty"`
	Details          json.RawMessage  `json:"details,omitempty"`
	Usage            *message.Usage   `json:"usage,omitempty"`
	FromHook         bool             `json:"fromHook,omitempty"`
	FromID           string           `json:"fromId,omitempty"`
	CustomType       string           `json:"customType,omitempty"`
	Data             json.RawMessage  `json:"data,omitempty"`
	Content          json.RawMessage  `json:"content,omitempty"`
	Display          *bool            `json:"display,omitempty"`
	TargetID         EntryID          `json:"targetId,omitempty"`
	Label            *string          `json:"label,omitempty"`
	Name             string           `json:"name,omitempty"`
	RunID            run.ID           `json:"runId,omitempty"`
	RunStatus        run.Status       `json:"runStatus,omitempty"`
	RunCause         run.Cause        `json:"runCause,omitempty"`
	RunEndReason     run.EndReason    `json:"runEndReason,omitempty"`
	RunStats         *run.Stats       `json:"runStats,omitempty"`
	RunError         string           `json:"runError,omitempty"`
	State            json.RawMessage  `json:"state,omitempty"`
}

type CustomEntry[T any] struct {
	ID         EntryID
	ParentID   *EntryID
	Timestamp  string
	CustomType string
	Data       T
}
type Context struct {
	Messages      []message.Message
	ThinkingLevel string
	Provider      string
	ModelID       string
}

const (
	CompactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	CompactionSummarySuffix = "\n</summary>"
	BranchSummaryPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	BranchSummarySuffix     = "</summary>"
)

type TreeNode struct {
	Entry          Entry
	Children       []*TreeNode
	Label          string
	LabelTimestamp string
}
type Options struct {
	ID, Cwd, ParentSession string
	InitialState           json.RawMessage
}

// Persistence is the append target for one session. Every Manager requires an
// explicit Persistence implementation, including ephemeral in-memory sessions.
// A Persistence instance is owned by exactly one Manager and must not be reused.
type Persistence interface {
	Location(Header) string
	Create(Header, []Entry) error
	Append(Entry) error
	Fork() Persistence
	Close() error
}

type Manager struct {
	mu          sync.RWMutex
	header      Header
	entries     []Entry
	byID        map[EntryID]int
	leaf        *EntryID
	persistence Persistence
	location    string
	created     bool
	closed      bool
}

var ErrClosed = errors.New("session is closed")
var ErrEntryNotFound = errors.New("session entry not found")
var ErrWriterActive = errors.New("session already has a writer")

func nowString() string {
	return time.Now().UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}
func New(persistence Persistence, opts Options) (*Manager, error) {
	if persistence == nil {
		return nil, errors.New("session persistence is required")
	}
	id := opts.ID
	if id == "" {
		id = uuid.NewV7().String()
	} else if !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`).MatchString(id) {
		return nil, errors.New("session id must contain only alphanumeric characters, '-', '_', and '.', and start and end with an alphanumeric character")
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if absolute, err := filepath.Abs(cwd); err == nil {
		cwd = absolute
	}
	initialState := opts.InitialState.Clone()
	if len(initialState) == 0 {
		initialState = json.RawMessage(`{}`)
	}
	m := &Manager{header: Header{Type: "session", Version: Version, ID: id, Timestamp: nowString(), Cwd: cwd, ParentSession: opts.ParentSession, InitialState: initialState}, byID: make(map[EntryID]int), persistence: persistence}
	m.location = persistence.Location(m.header)
	if m.location == "" {
		persistence.Close()
		return nil, errors.New("session persistence location is required")
	}
	return m, nil
}

func Restore(header Header, entries []Entry, persistence Persistence) (*Manager, error) {
	if persistence == nil {
		return nil, errors.New("session persistence is required")
	}
	if header.Type != "session" || header.ID == "" {
		return nil, errors.New("invalid session header")
	}
	if header.Version != Version {
		return nil, fmt.Errorf("unsupported session version %d (want %d)", header.Version, Version)
	}
	m := &Manager{
		header:      header,
		entries:     append([]Entry(nil), entries...),
		byID:        make(map[EntryID]int, len(entries)),
		persistence: persistence,
		location:    persistence.Location(header),
		created:     true,
	}
	if m.location == "" {
		persistence.Close()
		return nil, errors.New("session persistence location is required")
	}
	for i := range m.entries {
		if m.entries[i].ID == "" {
			persistence.Close()
			return nil, errors.New("session entry id is required")
		}
		if _, exists := m.byID[m.entries[i].ID]; exists {
			persistence.Close()
			return nil, fmt.Errorf("duplicate session entry id %q", m.entries[i].ID)
		}
		m.byID[m.entries[i].ID] = i
		id := m.entries[i].ID
		m.leaf = &id
	}
	return m, nil
}
func (m *Manager) Header() Header   { m.mu.RLock(); defer m.mu.RUnlock(); return m.header }
func (m *Manager) Location() string { m.mu.RLock(); defer m.mu.RUnlock(); return m.location }
func (m *Manager) Entries() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Entry(nil), m.entries...)
}
func (m *Manager) LeafID() *EntryID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.leaf == nil {
		return nil
	}
	id := *m.leaf
	return &id
}
func (m *Manager) newID() EntryID {
	for range 100 {
		id := EntryID(strings.ReplaceAll(uuid.NewV4().String(), "-", "")[:8])
		if _, ok := m.byID[id]; !ok {
			return id
		}
	}
	return EntryID(uuid.NewV4().String())
}
func (m *Manager) append(e Entry) (EntryID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendLocked(e)
}
func (m *Manager) appendLocked(e Entry) (EntryID, error) {
	if m.closed {
		return "", ErrClosed
	}
	e.ID = m.newID()
	e.Timestamp = nowString()
	if m.leaf != nil {
		id := *m.leaf
		e.ParentID = &id
	}
	m.byID[e.ID] = len(m.entries)
	m.entries = append(m.entries, e)
	id := e.ID
	m.leaf = &id
	if m.created || e.Type == "state" || e.Type == "run_start" || (e.Type == "message" && e.Message != nil && e.Message.Role == message.RoleAssistant) {
		if err := m.persistLocked(e); err != nil {
			delete(m.byID, e.ID)
			m.entries = m.entries[:len(m.entries)-1]
			m.leaf = e.ParentID
			return "", err
		}
	}
	return e.ID, nil
}
func (m *Manager) persistLocked(e Entry) error {
	if !m.created {
		if err := m.persistence.Create(m.header, append([]Entry(nil), m.entries...)); err != nil {
			return err
		}
		m.created = true
		return nil
	}
	return m.persistence.Append(e)
}
func (m *Manager) AppendMessage(msg message.Message) (EntryID, error) {
	return m.append(Entry{Type: "message", Message: &msg})
}
func (m *Manager) AppendState(state json.RawMessage) (EntryID, error) {
	if len(state) == 0 {
		return "", errors.New("state is required")
	}
	return m.append(Entry{Type: "state", State: state.Clone()})
}
func (m *Manager) AppendRunStart(id run.ID) (EntryID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.entries {
		if entry.Type == "run_start" && entry.RunID == id {
			return "", run.ErrDuplicateID
		}
	}
	return m.appendLocked(Entry{Type: "run_start", RunID: id, RunStatus: run.StatusRunning})
}
func (m *Manager) AppendRunEnd(id run.ID, status run.Status, cause run.Cause, runErr error) (EntryID, error) {
	return m.AppendRunOutcome(id, run.Outcome{Status: status, Cause: cause}, runErr)
}
func (m *Manager) AppendRunOutcome(id run.ID, outcome run.Outcome, runErr error) (EntryID, error) {
	status := outcome.Status
	if !run.Terminal(status) {
		return "", errors.New("run end requires a terminal status")
	}
	text := ""
	if runErr != nil {
		text = runErr.Error()
	}
	stats := outcome.Stats
	return m.append(Entry{Type: "run_end", RunID: id, RunStatus: status, RunCause: outcome.Cause, RunEndReason: outcome.EndReason, RunStats: &stats, RunError: text})
}
func (m *Manager) RunInfo(id run.ID) (run.Info, error) {
	for _, info := range m.RunHistory() {
		if info.ID == id {
			return info, nil
		}
	}
	return run.Info{}, run.ErrNotFound
}
func (m *Manager) RunHistory() []run.Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	indices := make(map[run.ID]int)
	var out []run.Info
	for _, entry := range m.entries {
		switch entry.Type {
		case "run_start":
			if _, exists := indices[entry.RunID]; exists {
				continue
			}
			started, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			indices[entry.RunID] = len(out)
			out = append(out, run.Info{ID: entry.RunID, Status: run.StatusRunning, StartedAt: started})
		case "run_end":
			i, exists := indices[entry.RunID]
			if !exists {
				continue
			}
			ended, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			out[i].Status = entry.RunStatus
			out[i].Cause = entry.RunCause
			out[i].EndReason = entry.RunEndReason
			if entry.RunStats != nil {
				out[i].Stats = *entry.RunStats
			}
			out[i].Error = entry.RunError
			out[i].EndedAt = ended
		}
	}
	return out
}
func (m *Manager) AppendThinkingLevel(level string) (EntryID, error) {
	return m.append(Entry{Type: "thinking_level_change", ThinkingLevel: level})
}
func (m *Manager) AppendModel(provider, modelID string) (EntryID, error) {
	return m.append(Entry{Type: "model_change", Provider: provider, ModelID: modelID})
}
func (m *Manager) AppendCompaction(summary string, first EntryID, tokens int64, details json.RawMessage, usage *message.Usage, fromHook bool) (EntryID, error) {
	return m.append(Entry{Type: "compaction", Summary: summary, FirstKeptEntryID: first, TokensBefore: tokens, Details: details.Clone(), Usage: usage, FromHook: fromHook})
}
func (m *Manager) AppendBranchSummary(from *EntryID, summary string, details json.RawMessage, usage *message.Usage, fromHook bool) (EntryID, error) {
	m.mu.Lock()
	if from != nil {
		if _, ok := m.byID[*from]; !ok {
			m.mu.Unlock()
			return "", ErrEntryNotFound
		}
		id := *from
		m.leaf = &id
	} else {
		m.leaf = nil
	}
	m.mu.Unlock()
	fromID := "root"
	if from != nil {
		fromID = string(*from)
	}
	return m.append(Entry{Type: "branch_summary", FromID: fromID, Summary: summary, Details: details.Clone(), Usage: usage, FromHook: fromHook})
}
func (m *Manager) AppendCustom[T any](ctx context.Context, customType string, value T) (EntryID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return m.append(Entry{Type: "custom", CustomType: customType, Data: b})
}
func (m *Manager) CustomEntries[T any](customType string) ([]CustomEntry[T], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []CustomEntry[T]
	for _, e := range m.entries {
		if e.Type != "custom" || e.CustomType != customType {
			continue
		}
		var v T
		if err := json.Unmarshal(e.Data, &v); err != nil {
			return nil, fmt.Errorf("decode custom entry %s: %w", e.ID, err)
		}
		out = append(out, CustomEntry[T]{ID: e.ID, ParentID: e.ParentID, Timestamp: e.Timestamp, CustomType: e.CustomType, Data: v})
	}
	return out, nil
}
func (m *Manager) AppendCustomMessage(customType string, content any, display bool, details any) (EntryID, error) {
	c, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	d, err := json.Marshal(details)
	if err != nil {
		return "", err
	}
	return m.append(Entry{Type: "custom_message", CustomType: customType, Content: c, Display: &display, Details: d})
}
func (m *Manager) SetLabel(target EntryID, label string) (EntryID, error) {
	m.mu.RLock()
	_, ok := m.byID[target]
	m.mu.RUnlock()
	if !ok {
		return "", ErrEntryNotFound
	}
	var p *string
	if label != "" {
		clean := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(label))
		p = &clean
	}
	return m.append(Entry{Type: "label", TargetID: target, Label: p})
}
func (m *Manager) SetName(name string) (EntryID, error) {
	name = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(name))
	return m.append(Entry{Type: "session_info", Name: name})
}
func (m *Manager) Name() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Type == "session_info" {
			return m.entries[i].Name
		}
	}
	return ""
}
func (m *Manager) Navigate(id *EntryID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == nil {
		m.leaf = nil
		return nil
	}
	if _, ok := m.byID[*id]; !ok {
		return ErrEntryNotFound
	}
	v := *id
	m.leaf = &v
	return nil
}
func (m *Manager) Branch(id EntryID) ([]Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.byID[id]; !ok {
		return nil, ErrEntryNotFound
	}
	var reverse []Entry
	current := &id
	seen := map[EntryID]bool{}
	for current != nil && !seen[*current] {
		seen[*current] = true
		e := m.entries[m.byID[*current]]
		reverse = append(reverse, e)
		current = e.ParentID
	}
	out := make([]Entry, len(reverse))
	for i := range reverse {
		out[len(reverse)-1-i] = reverse[i]
	}
	return out, nil
}
func (m *Manager) CurrentBranch() []Entry {
	m.mu.RLock()
	if m.leaf == nil {
		m.mu.RUnlock()
		return nil
	}
	id := *m.leaf
	m.mu.RUnlock()
	branch, _ := m.Branch(id)
	return branch
}

func messageTimestamp(timestamp string) int64 {
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return time.Now().UnixMilli()
	}
	return t.UnixMilli()
}
func (m *Manager) Context() Context {
	m.mu.RLock()
	var leaf *EntryID
	if m.leaf != nil {
		id := *m.leaf
		leaf = &id
	}
	m.mu.RUnlock()
	if leaf == nil {
		return Context{ThinkingLevel: "off"}
	}
	path, _ := m.Branch(*leaf)
	ctx := Context{ThinkingLevel: "off"}
	for _, e := range path {
		if e.Type == "thinking_level_change" {
			ctx.ThinkingLevel = e.ThinkingLevel
		}
		if e.Type == "model_change" {
			ctx.Provider = e.Provider
			ctx.ModelID = e.ModelID
		}
		if e.Type == "message" && e.Message != nil && e.Message.Role == message.RoleAssistant {
			ctx.Provider = e.Message.Provider
			ctx.ModelID = e.Message.Model
		}
	}
	contextEntries := path
	lastCompact := -1
	for i, e := range path {
		if e.Type == "compaction" {
			lastCompact = i
		}
	}
	if lastCompact >= 0 {
		c := path[lastCompact]
		selected := []Entry{c}
		found := false
		for _, e := range path[:lastCompact] {
			if e.ID == c.FirstKeptEntryID {
				found = true
			}
			if found {
				selected = append(selected, e)
			}
		}
		selected = append(selected, path[lastCompact+1:]...)
		contextEntries = selected
	}
	for _, e := range contextEntries {
		switch e.Type {
		case "message":
			if e.Message != nil {
				ctx.Messages = append(ctx.Messages, *e.Message)
			}
		case "compaction", "branch_summary":
			if e.Summary != "" {
				text := CompactionSummaryPrefix + e.Summary + CompactionSummarySuffix
				if e.Type == "branch_summary" {
					text = BranchSummaryPrefix + e.Summary + BranchSummarySuffix
				}
				msg := message.User(text)
				msg.Timestamp = messageTimestamp(e.Timestamp)
				ctx.Messages = append(ctx.Messages, msg)
			}
		case "custom_message":
			var content []message.Content
			if json.Unmarshal(e.Content, &content) == nil {
				ctx.Messages = append(ctx.Messages, message.Message{Role: message.RoleUser, Content: content, Timestamp: messageTimestamp(e.Timestamp)})
			} else {
				var text string
				if json.Unmarshal(e.Content, &text) == nil {
					msg := message.User(text)
					msg.Timestamp = messageTimestamp(e.Timestamp)
					ctx.Messages = append(ctx.Messages, msg)
				}
			}
		}
	}
	return ctx
}

// State returns the latest full state snapshot on the current branch. At the
// root, it returns the initial state stored in the session header.
func (m *Manager) State() json.RawMessage {
	m.mu.RLock()
	initial := m.header.InitialState.Clone()
	var leaf *EntryID
	if m.leaf != nil {
		id := *m.leaf
		leaf = &id
	}
	m.mu.RUnlock()
	if leaf == nil {
		return initial
	}
	path, err := m.Branch(*leaf)
	if err != nil {
		return initial
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Type == "state" && len(path[i].State) != 0 {
			return path[i].State.Clone()
		}
	}
	return initial
}
func (m *Manager) Tree() []*TreeNode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	nodes := make(map[EntryID]*TreeNode, len(m.entries))
	labels := map[EntryID]struct{ v, t string }{}
	for _, e := range m.entries {
		if e.Type == "label" {
			v := ""
			if e.Label != nil {
				v = *e.Label
			}
			labels[e.TargetID] = struct{ v, t string }{v, e.Timestamp}
		}
	}
	for _, e := range m.entries {
		l := labels[e.ID]
		nodes[e.ID] = &TreeNode{Entry: e, Label: l.v, LabelTimestamp: l.t}
	}
	var roots []*TreeNode
	for _, e := range m.entries {
		n := nodes[e.ID]
		if e.ParentID == nil || *e.ParentID == e.ID || nodes[*e.ParentID] == nil {
			roots = append(roots, n)
		} else {
			nodes[*e.ParentID].Children = append(nodes[*e.ParentID].Children, n)
		}
	}
	return roots
}
func (m *Manager) Fork(ctx context.Context, leaf EntryID, opts Options) (*Manager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := m.Branch(leaf)
	if err != nil {
		return nil, err
	}
	if opts.Cwd == "" {
		opts.Cwd = m.header.Cwd
	}
	opts.InitialState = m.header.InitialState.Clone()
	opts.ParentSession = m.location
	fork, err := New(m.persistence.Fork(), opts)
	if err != nil {
		return nil, err
	}
	var parent *EntryID
	for _, e := range path {
		if e.Type == "label" {
			continue
		}
		e.ParentID = parent
		fork.byID[e.ID] = len(fork.entries)
		fork.entries = append(fork.entries, e)
		id := e.ID
		parent = &id
	}
	fork.leaf = parent
	// Labels are side entries in the tree. Recreate the latest resolved label for
	// each retained target and re-chain them after the copied path, as pi does.
	m.mu.RLock()
	labels := make(map[EntryID]Entry)
	retained := make(map[EntryID]bool, len(path))
	for _, e := range path {
		if e.Type != "label" {
			retained[e.ID] = true
		}
	}
	for _, e := range m.entries {
		if e.Type == "label" && retained[e.TargetID] {
			labels[e.TargetID] = e
		}
	}
	m.mu.RUnlock()
	targets := make([]string, 0, len(labels))
	for target := range labels {
		targets = append(targets, string(target))
	}
	sort.Strings(targets)
	for _, target := range targets {
		old := labels[EntryID(target)]
		entry := Entry{Type: "label", ID: fork.newID(), ParentID: parent, Timestamp: old.Timestamp, TargetID: old.TargetID, Label: old.Label}
		fork.byID[entry.ID] = len(fork.entries)
		fork.entries = append(fork.entries, entry)
		id := entry.ID
		parent = &id
	}
	fork.leaf = parent
	if len(fork.entries) > 0 {
		fork.mu.Lock()
		last := fork.entries[len(fork.entries)-1]
		// persistLocked creates the destination with all retained entries.
		err = fork.persistLocked(last)
		fork.mu.Unlock()
		if err != nil {
			fork.Close()
			return nil, err
		}
	}
	return fork, nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.persistence.Close()
}

type Info struct {
	Location, ID, Cwd, Name       string
	Created, Modified             time.Time
	MessageCount                  int
	FirstMessage, AllMessagesText string
}
