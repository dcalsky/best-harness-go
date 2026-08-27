package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
	openaisdk "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
)

const modelID = "deepseek-v4-flash"

var nonThinking = harness.GenerationConfig{Extra: map[string]json.RawMessage{
	"thinking.type": json.RawMessage(`"disabled"`),
}}

var thinkingEnabled = harness.GenerationConfig{Extra: map[string]json.RawMessage{
	"thinking.type": json.RawMessage(`"enabled"`),
}}

func deepSeekAPIKey(t *testing.T) string {
	t.Helper()
	if os.Getenv("BEST_HARNESS_DEEPSEEK_E2E") != "1" {
		t.Skip("set BEST_HARNESS_DEEPSEEK_E2E=1 to run paid DeepSeek tests")
	}
	if key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); key != "" {
		return key
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate DeepSeek E2E source file")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(source)), ".env"))
	if err != nil {
		t.Fatalf("read repository .env: %v", err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || strings.TrimSpace(name) != "DEEPSEEK_API_KEY" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if value != "" {
			return value
		}
	}
	t.Fatal("DEEPSEEK_API_KEY is not set in the environment or repository .env")
	return ""
}

func deepSeek(t *testing.T) (*harness.OpenAIProvider, harness.Model) {
	t.Helper()
	key := deepSeekAPIKey(t)
	client := openaisdk.NewClient(
		openaioption.WithAPIKey(key),
		openaioption.WithBaseURL("https://api.deepseek.com"),
	)
	p := harness.NewOpenAIProvider(client)
	m := harness.Model{Provider: "deepseek", API: harness.APIOpenAI, ID: modelID, Name: "DeepSeek V4 Flash", ContextWindow: 1_000_000, MaxOutput: 512, SupportsReasoning: true}
	return p, m
}

func streamText(ctx context.Context, p harness.Provider, request harness.Request) (string, harness.Usage, error) {
	stream, err := p.Stream(ctx, request)
	if err != nil {
		return "", harness.Usage{}, err
	}
	defer stream.Close()
	var text strings.Builder
	var usage harness.Usage
	for {
		event, err := stream.Next()
		if err == io.EOF {
			return text.String(), usage, nil
		}
		if err != nil {
			return "", usage, err
		}
		if event.Type == harness.EventTextDelta {
			text.WriteString(event.Text)
		}
		if event.Usage.TotalTokens > 0 {
			usage = event.Usage
		}
		if event.Err != nil {
			return "", usage, event.Err
		}
	}
}

func TestDeepSeekProviderMessageAndModel(t *testing.T) {
	p, selected := deepSeek(t)
	registry := harness.NewModelRegistry()
	if err := registry.Register(selected); err != nil {
		t.Fatal(err)
	}
	if got, err := registry.Get("deepseek", modelID); err != nil || got.ID != modelID {
		t.Fatalf("model lookup: model=%#v error=%v", got, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	text, usage, err := streamText(ctx, p, harness.Request{
		Model:      selected,
		Messages:   []harness.Message{harness.User("Reply with exactly PROVIDER_OK and nothing else.")},
		MaxTokens:  32,
		Generation: nonThinking,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text) != "PROVIDER_OK" {
		t.Fatalf("response=%q", text)
	}
	if usage.InputTokens == 0 || usage.OutputTokens == 0 || usage.TotalTokens == 0 {
		t.Fatalf("usage=%#v", usage)
	}
}

type addParams struct {
	A int `json:"a"`
	B int `json:"b"`
}

type addDetails struct {
	Sum int `json:"sum"`
}

func TestDeepSeekTypedToolAndAgent(t *testing.T) {
	p, selected := deepSeek(t)
	recorded := &providerRecorder{base: p}
	selected.MaxOutput = 2_048
	tools := harness.NewToolRegistry()
	var calls atomic.Int32
	var before atomic.Int32
	var after atomic.Int32
	if err := tools.Register(harness.Tool[addParams, addDetails]{
		Name:        "add_numbers",
		Description: "Add two integers and return their sum.",
		Execute: func(_ context.Context, _ harness.ToolCall, p addParams, update harness.Update[addDetails]) (harness.ToolResult[addDetails], error) {
			calls.Add(1)
			d := addDetails{Sum: p.A + p.B}
			update(d)
			return harness.ToolResult[addDetails]{Content: []harness.Content{harness.Text(fmt.Sprintf("%d", d.Sum))}, Details: d}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	tools.AddBeforeHook(func(_ context.Context, call harness.ToolCall) (harness.ToolCall, error) {
		before.Add(1)
		return call, nil
	})
	tools.AddAfterHook(func(_ context.Context, _ harness.ToolCall, result harness.Result) (harness.Result, error) {
		after.Add(1)
		return result, nil
	})
	a := harness.NewAgent(harness.AgentOptions{Provider: recorded, Model: selected, Tools: tools, ActiveTools: []string{"add_numbers"}, Generation: thinkingEnabled})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := startAgentRun(t, ctx, a, harness.AgentPrompt{Steps: harness.Sequence{harness.UserText("Call add_numbers exactly once with a=19 and b=23. After the tool result, reply with TOOL_OK 42 without calling the tool again.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	messages := a.Messages()
	if calls.Load() != 1 || before.Load() != 1 || after.Load() != 1 {
		t.Fatalf("tool calls=%d before=%d after=%d", calls.Load(), before.Load(), after.Load())
	}
	if len(messages) < 4 || !strings.Contains(messages[len(messages)-1].Text(), "42") {
		t.Fatalf("messages=%#v", messages)
	}
	requests := recorded.snapshot()
	if len(requests) != 2 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleTool)
}

type lifecycleExtension struct {
	input, request, before, response, start, stop, resourcePrompt atomic.Int32
	mu                                                            sync.Mutex
	requests                                                      []harness.Request
}

func (e *lifecycleExtension) Register(r *harness.ExtensionRegistry[harness.NoState]) error {
	r.AddInputHook(func(_ context.Context, _ harness.Context[harness.NoState], m harness.Message) (harness.Message, error) {
		e.input.Add(1)
		return m, nil
	})
	r.AddRequestHook(func(_ context.Context, _ harness.Context[harness.NoState], request *harness.Request) error {
		e.request.Add(1)
		e.mu.Lock()
		e.requests = append(e.requests, *request)
		e.mu.Unlock()
		if strings.Contains(request.SystemPrompt, "RESOURCE_OK") {
			e.resourcePrompt.Add(1)
		}
		return nil
	})
	r.AddBeforeAgentHook(func(context.Context, harness.Context[harness.NoState]) error { e.before.Add(1); return nil })
	r.AddResponseHook(func(context.Context, harness.Context[harness.NoState], harness.Message) error {
		e.response.Add(1)
		return nil
	})
	r.AddSessionStartHook(func(context.Context, harness.Context[harness.NoState]) error { e.start.Add(1); return nil })
	r.AddShutdownHook(func(context.Context, harness.Context[harness.NoState]) error { e.stop.Add(1); return nil })
	return nil
}

func TestDeepSeekHarnessSessionResourceExtensionAndSettings(t *testing.T) {
	p, selected := deepSeek(t)
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("End the final answer with RESOURCE_OK."), 0o644); err != nil {
		t.Fatal(err)
	}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	resources := harness.NewResourceRegistry()
	resources.Register(harness.NewFileSystemResourceLoader(project))
	config := harness.NewSettings()
	if err := config.Set(harness.QueueMode, harness.QueueOneAtATime); err != nil {
		t.Fatal(err)
	}
	ext := &lifecycleExtension{}
	h, err := harness.NewStateless(harness.Options{Models: models, Resources: resources, Settings: config})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterExtension(ext); err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("deepseek", p); err != nil {
		t.Fatal(err)
	}
	persistence, err := harness.NewFilePersistence(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), persistence, harness.SessionOptions{Model: &selected, Cwd: project, Generation: nonThinking}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Location()
	var appended atomic.Int32
	s.On(func(_ context.Context, _ harness.Context[harness.NoState], event harness.EntryAppendedEvent) {
		if event.Entry.Type == "message" {
			appended.Add(1)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply with SESSION_OK and follow the project instruction.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	var messageEntries []harness.SessionEntry
	for _, entry := range entries {
		if entry.Type == "message" {
			messageEntries = append(messageEntries, entry)
		}
	}
	if len(entries) != 4 || len(messageEntries) != 2 || appended.Load() != 2 {
		t.Fatalf("entries=%d appended=%d", len(entries), appended.Load())
	}
	if !strings.Contains(s.Conversation().Messages[1].Text(), "SESSION_OK") {
		t.Fatalf("assistant=%q", s.Conversation().Messages[1].Text())
	}
	ext.mu.Lock()
	requests := append([]harness.Request(nil), ext.requests...)
	ext.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	if strings.Contains(requests[0].SystemPrompt, "Available tools:") ||
		!strings.Contains(requests[0].SystemPrompt, `<project_instructions path="`+filepath.Join(project, "AGENTS.md")+`">`) ||
		!strings.HasSuffix(requests[0].SystemPrompt, "Current working directory: "+project) {
		t.Fatalf("pi-incompatible system prompt=%q", requests[0].SystemPrompt)
	}
	customID, err := s.AppendCustom(ctx, "e2e", struct {
		Value int `json:"value"`
	}{Value: 7})
	if err != nil || customID == "" {
		t.Fatal(err)
	}
	leaf := messageEntries[len(messageEntries)-1].ID
	fork, err := s.Fork(ctx, leaf, harness.SessionOptions{Model: &selected, Cwd: project, Generation: nonThinking})
	if err != nil {
		t.Fatal(err)
	}
	if len(fork.Conversation().Messages) != 2 {
		t.Fatalf("fork messages=%d", len(fork.Conversation().Messages))
	}
	_ = fork.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if ext.input.Load() != 1 || ext.request.Load() == 0 || ext.resourcePrompt.Load() == 0 || ext.before.Load() != 1 || ext.response.Load() != 1 || ext.start.Load() < 1 || ext.stop.Load() < 1 {
		t.Fatalf("hooks input=%d request=%d resource=%d before=%d response=%d start=%d stop=%d", ext.input.Load(), ext.request.Load(), ext.resourcePrompt.Load(), ext.before.Load(), ext.response.Load(), ext.start.Load(), ext.stop.Load())
	}
	opened, err := h.OpenSession(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if len(opened.Conversation().Messages) != 2 {
		t.Fatalf("reopened messages=%d", len(opened.Conversation().Messages))
	}
	values, err := opened.CustomEntries[struct {
		Value int `json:"value"`
	}]("e2e")
	if err != nil || len(values) != 1 || values[0].Data.Value != 7 {
		t.Fatalf("custom=%#v error=%v", values, err)
	}
}

type deepSeekSummarizer struct {
	p     harness.Provider
	model harness.Model
}

func (s deepSeekSummarizer) Summarize(ctx context.Context, messages []harness.Message, _ string) (harness.CompactionSummary, error) {
	var source strings.Builder
	for _, m := range messages {
		fmt.Fprintf(&source, "%s: %s\n", m.Role, m.Text())
	}
	text, usage, err := streamText(ctx, s.p, harness.Request{Model: s.model, Messages: []harness.Message{harness.User("Summarize the following conversation in one short sentence starting with COMPACT_OK.\n" + source.String())}, MaxTokens: 96, Generation: nonThinking})
	if err != nil {
		return harness.CompactionSummary{}, err
	}
	return harness.CompactionSummary{Text: text, Usage: &usage}, nil
}

func TestDeepSeekCompaction(t *testing.T) {
	p, selected := deepSeek(t)
	m, err := harness.NewSessionManager(harness.NewMemoryPersistence(), harness.PersistenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		role harness.Role
		text string
	}{{harness.RoleUser, strings.Repeat("old requirement ", 20)}, {harness.RoleAssistant, strings.Repeat("old answer ", 20)}, {harness.RoleUser, strings.Repeat("recent requirement ", 12)}, {harness.RoleAssistant, strings.Repeat("recent answer ", 12)}} {
		msg := harness.Message{Role: item.role, Content: []harness.Content{harness.Text(item.text)}, Timestamp: time.Now().UnixMilli()}
		if _, err := m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := harness.RunCompaction(ctx, m, harness.CompactionManual, harness.CompactionOptions{KeepRecentTokens: 60}, deepSeekSummarizer{p: p, model: selected})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "COMPACT_OK") || result.Usage == nil || result.Usage.TotalTokens == 0 {
		t.Fatalf("result=%#v", result)
	}
	if got := m.Entries()[len(m.Entries())-1].Type; got != "compaction" {
		t.Fatalf("last entry=%s", got)
	}
	contextMessages := m.Context().Messages
	if len(contextMessages) == 0 || !strings.HasPrefix(contextMessages[0].Text(), harness.CompactionSummaryPrefix) || !strings.HasSuffix(contextMessages[0].Text(), harness.CompactionSummarySuffix) {
		t.Fatalf("pi-incompatible compaction context=%#v", contextMessages)
	}
}

func TestDeepSeekBuiltins(t *testing.T) {
	p, selected := deepSeek(t)
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seed, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, prompt string
		verify       func(*testing.T)
	}{
		{"write", fmt.Sprintf("Call write exactly once to write E2E_WRITE to %s. Then reply WRITE_OK without another tool call.", filepath.Join(dir, "written.txt")), func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, "written.txt"))
			if err != nil || string(b) != "E2E_WRITE" {
				t.Fatalf("written=%q error=%v", b, err)
			}
		}},
		{"read", fmt.Sprintf("Call read exactly once for %s. Then reply READ_OK and mention beta without another tool call.", seed), func(t *testing.T) {}},
		{"edit", fmt.Sprintf("Call edit exactly once for %s, replacing beta with gamma. Then reply EDIT_OK without another tool call.", seed), func(t *testing.T) {
			b, _ := os.ReadFile(seed)
			if !strings.Contains(string(b), "gamma") {
				t.Fatalf("file=%q", b)
			}
		}},
		{"grep", fmt.Sprintf("Call grep exactly once with pattern gamma and path %s. Then reply GREP_OK without another tool call.", dir), func(t *testing.T) {}},
		{"find", fmt.Sprintf("Call find exactly once with pattern *.txt and path %s. Then reply FIND_OK without another tool call.", dir), func(t *testing.T) {}},
		{"ls", fmt.Sprintf("Call ls exactly once with path %s. Then reply LS_OK without another tool call.", dir), func(t *testing.T) {}},
		{"bash", "Call bash exactly once with command printf E2E_BASH. Then reply BASH_OK without another tool call.", func(t *testing.T) {}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorded := &providerRecorder{base: p}
			tools := harness.NewToolRegistry()
			if err := harness.RegisterBuiltinTools(tools, harness.BuiltinConfig{Cwd: dir, MaxOutputBytes: 4096}); err != nil {
				t.Fatal(err)
			}
			a := harness.NewAgent(harness.AgentOptions{Provider: recorded, Model: selected, Tools: tools, ActiveTools: []string{tc.name}, Generation: nonThinking, ExecutionMode: harness.Sequential})
			var calls atomic.Int32
			a.On(func(event harness.AgentLifecycleEvent) {
				if event.Type == harness.AgentEventToolStart && event.Call != nil && event.Call.Name == tc.name {
					calls.Add(1)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			run := startAgentRun(t, ctx, a, harness.AgentPrompt{Steps: harness.Sequence{harness.UserText(tc.prompt)}})
			if err := run.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("%s calls=%d messages=%#v", tc.name, calls.Load(), a.Messages())
			}
			requests := recorded.snapshot()
			if len(requests) != 2 {
				t.Fatalf("provider requests=%d", len(requests))
			}
			assertContextRoles(t, requests[0], harness.RoleUser)
			assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleTool)
			tc.verify(t)
		})
	}
}
