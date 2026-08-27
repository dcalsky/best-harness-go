package main

import (
	"context"
	"fmt"

	"github.com/dcalsky/best-harness-go"
)

type summary struct{}

func (summary) Summarize(context.Context, []harness.Message, string) (harness.CompactionSummary, error) {
	return harness.CompactionSummary{Text: "Earlier work was summarized."}, nil
}
func main() {
	m, _ := harness.NewSessionManager(harness.NewMemoryPersistence(), harness.PersistenceOptions{})
	id, _ := m.AppendMessage(harness.User("first"))
	_, _ = m.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: []harness.Content{harness.Text("answer")}})
	fork, _ := m.Fork(context.Background(), id, harness.PersistenceOptions{})
	fmt.Println(len(fork.Context().Messages))
	_, _ = harness.RunCompaction(context.Background(), m, harness.CompactionManual, harness.CompactionOptions{KeepRecentTokens: 1}, summary{})
}
