package e2e_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

func TestDeepSeekNavigateBranchSummaryTreeLabelsAndStaleContext(t *testing.T) {
	p, selected := deepSeek(t)
	recorded := &providerRecorder{base: p}
	h := newDeepSeekHarness(t, recorded, selected)
	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), persistence, harness.SessionOptions{Model: &selected, Generation: nonThinking}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Location()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply ORIGINAL_BRANCH_OK only.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	var rootID harness.SessionEntryID
	for _, entry := range entries {
		if entry.Type == "message" {
			rootID = entry.ID
			break
		}
	}
	if rootID == "" {
		t.Fatal("session has no message entry")
	}
	oldContext := s.Context()
	summary := "The abandoned branch answered ORIGINAL_BRANCH_OK."
	if err = s.Navigate(ctx, &rootID, harness.NavigateOptions{Summary: summary}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(oldContext.Check(), harness.ErrStaleContext) {
		t.Fatalf("stale check=%v", oldContext.Check())
	}
	if _, err = s.SetLabel(rootID, "branch-root"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetName("branched session"); err != nil {
		t.Fatal(err)
	}
	run = startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply NEW_BRANCH_OK only.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if !stringsContainAssistant(s.Conversation().Messages, "NEW_BRANCH_OK") {
		t.Fatalf("context=%#v", s.Conversation().Messages)
	}
	requests := recorded.snapshot()
	if len(requests) != 2 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleUser, harness.RoleUser)
	branchText := requests[1].Messages[1].Text()
	if branchText != harness.BranchSummaryPrefix+summary+harness.BranchSummarySuffix {
		t.Fatalf("branch summary context=%q", branchText)
	}
	tree := s.Tree()
	var branchRoot *harness.SessionTreeNode
	var findRoot func([]*harness.SessionTreeNode)
	findRoot = func(nodes []*harness.SessionTreeNode) {
		for _, node := range nodes {
			if node.Entry.ID == rootID {
				branchRoot = node
				return
			}
			findRoot(node.Children)
			if branchRoot != nil {
				return
			}
		}
	}
	findRoot(tree)
	if branchRoot == nil || len(branchRoot.Children) < 2 {
		t.Fatalf("tree=%#v", tree)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := h.OpenSession(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if !stringsContainAssistant(opened.Conversation().Messages, "NEW_BRANCH_OK") {
		t.Fatalf("opened context=%#v", opened.Conversation().Messages)
	}
}

type systemPromptCapture struct {
	mu       sync.Mutex
	requests []harness.Request
}

func (e *systemPromptCapture) Register(r *harness.ExtensionRegistry[harness.NoState]) error {
	r.AddRequestHook(func(_ context.Context, _ harness.Context[harness.NoState], request *harness.Request) error {
		e.mu.Lock()
		e.requests = append(e.requests, *request)
		e.mu.Unlock()
		return nil
	})
	return nil
}

func TestDeepSeekResourceReloadChangesNextRequest(t *testing.T) {
	p, selected := deepSeek(t)
	root := t.TempDir()
	agents := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("Resource version: RESOURCE_V1"), 0o644); err != nil {
		t.Fatal(err)
	}
	resources := harness.NewResourceRegistry()
	resources.Register(harness.NewFileSystemResourceLoader(root))
	capture := &systemPromptCapture{}
	models := modelRegistryWith(t, selected)
	h, err := harness.NewStateless(harness.Options{Models: models, Resources: resources})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterExtension(capture); err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("deepseek", p); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Cwd: root, Generation: nonThinking}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply RESOURCE_TURN_ONE.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(agents, []byte("Resource version: RESOURCE_V2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = s.ReloadResources(ctx); err != nil {
		t.Fatal(err)
	}
	run = startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply RESOURCE_TURN_TWO.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	requests := append([]harness.Request(nil), capture.requests...)
	capture.mu.Unlock()
	prompts := make([]string, len(requests))
	for i := range requests {
		prompts[i] = requests[i].SystemPrompt
	}
	if len(prompts) != 2 || !contains(prompts[0], "RESOURCE_V1") || contains(prompts[0], "RESOURCE_V2") || !contains(prompts[1], "RESOURCE_V2") {
		t.Fatalf("prompts=%#v", prompts)
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
}

func modelRegistryWith(t *testing.T, selected harness.Model) *harness.ModelRegistry {
	t.Helper()
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	return models
}

func TestDeepSeekCustomMessageParticipatesInContext(t *testing.T) {
	p, selected := deepSeek(t)
	recorded := &providerRecorder{base: p}
	h := newDeepSeekHarness(t, recorded, selected)
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Generation: nonThinking}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	manager, err := harness.NewSessionManager(harness.NewMemoryPersistence(), harness.PersistenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AppendCustomMessage("notice", "CUSTOM_CONTEXT_MARKER", false, map[string]any{"source": "e2e"}); err != nil {
		t.Fatal(err)
	}
	messages := manager.Context().Messages
	if len(messages) != 1 || messages[0].Text() != "CUSTOM_CONTEXT_MARKER" {
		t.Fatalf("messages=%#v", messages)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserMessageStep{Content: messages[0].Content}, harness.UserText("Repeat the marker and add CUSTOM_MESSAGE_OK.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if !stringsContainAssistant(s.Conversation().Messages, "CUSTOM_CONTEXT_MARKER") || !stringsContainAssistant(s.Conversation().Messages, "CUSTOM_MESSAGE_OK") {
		t.Fatalf("context=%#v", s.Conversation().Messages)
	}
	requests := recorded.snapshot()
	if len(requests) != 1 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser, harness.RoleUser)
	if requests[0].Messages[0].Text() != "CUSTOM_CONTEXT_MARKER" {
		t.Fatalf("custom context=%#v", requests[0].Messages)
	}
}
