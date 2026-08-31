package compact_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go"
	"github.com/dcalsky/best-harness-go/internal/compact"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/session"
)

type oneToken struct{}

func (oneToken) Estimate(message.Message) int64 { return 1 }

type summarizer struct{}

func (summarizer) Summarize(_ context.Context, ms []message.Message, _ string) (compact.Summary, error) {
	return compact.Summary{Text: "summary"}, nil
}

type captureSummarizer struct{ messages []message.Message }

func (s *captureSummarizer) Summarize(_ context.Context, messages []message.Message, _ string) (compact.Summary, error) {
	s.messages = append([]message.Message(nil), messages...)
	return compact.Summary{Text: "summary"}, nil
}

func TestNovocabEstimator(t *testing.T) {
	estimator := compact.NovocabEstimator{}
	if got := estimator.Estimate(message.User("hello world")); got != 3 {
		t.Fatalf("estimate=%d, want 3", got)
	}
	if got := estimator.Estimate(message.Message{}); got != 1 {
		t.Fatalf("empty estimate=%d, want 1", got)
	}
	invalid := message.User(string([]byte{'a', 0xff, 'b'}))
	if got := estimator.Estimate(invalid); got < 1 {
		t.Fatalf("invalid UTF-8 estimate=%d", got)
	}
}

func TestCustomTokenEstimator(t *testing.T) {
	var _ compact.TokenEstimator = compact.TokenEstimatorFunc(func(message.Message) int64 { return 7 })
	got := compact.Tokens([]message.Message{message.User("ignored")}, compact.TokenEstimatorFunc(func(message.Message) int64 { return 7 }))
	if got != 7 {
		t.Fatalf("custom estimate=%d, want 7", got)
	}
}

func TestToolBoundaryAndRun(t *testing.T) {
	m, _ := session.New(harness.NewMemoryPersistence(), session.Options{})
	_, _ = m.AppendMessage(message.User("old"))
	call := message.Message{Role: message.RoleAssistant, Content: []message.Content{message.ToolCall("c", "x", []byte(`{}`))}}
	_, _ = m.AppendMessage(call)
	kept, _ := m.AppendMessage(message.Message{Role: message.RoleTool, ToolCallID: "c", Content: []message.Content{message.Text("result")}})
	_, _ = m.AppendMessage(message.User("recent"))
	p, err := compact.Prepare(m.Entries(), compact.Manual, compact.Options{KeepRecentTokens: 2, Estimator: oneToken{}})
	if err != nil {
		t.Fatal(err)
	}
	if p.FirstKeptEntryID == kept {
		t.Fatal("cut separated a tool call from its result")
	}
	res, err := compact.Run(context.Background(), m, compact.Manual, compact.Options{KeepRecentTokens: 1, Estimator: oneToken{}}, summarizer{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "summary" {
		t.Fatalf("result=%#v", res)
	}
}

func TestRunOnlySummarizesCurrentBranch(t *testing.T) {
	m, err := session.New(harness.NewMemoryPersistence(), session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := m.AppendMessage(message.User("root"))
	_, _ = m.AppendMessage(message.User("abandoned-one"))
	_, _ = m.AppendMessage(message.User("abandoned-two"))
	if err = m.Navigate(&root); err != nil {
		t.Fatal(err)
	}
	_, _ = m.AppendMessage(message.User("active-one"))
	_, _ = m.AppendMessage(message.User("active-two"))
	_, _ = m.AppendMessage(message.User("active-three"))
	summarizer := &captureSummarizer{}
	if _, err = compact.Run(context.Background(), m, compact.Manual, compact.Options{KeepRecentTokens: 1, Estimator: oneToken{}}, summarizer); err != nil {
		t.Fatal(err)
	}
	for _, msg := range summarizer.messages {
		if strings.Contains(msg.Text(), "abandoned") {
			t.Fatalf("summarized inactive branch: %#v", summarizer.messages)
		}
	}
}
