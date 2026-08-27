package e2e_test

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dcalsky/best-harness-go"
)

func TestDeepSeekLargeTextCSVFiles(t *testing.T) {
	p, selected := deepSeek(t)
	selected.MaxOutput = 256
	recorded := &providerRecorder{base: p}

	tests := []struct {
		name, label string
		size, limit int
		useDefault  bool
	}{
		{name: "large_text_1mb.csv", label: "1MB", size: 1_000_000, limit: harness.DefaultLargeTextMaxChars, useDefault: true},
		{name: "large_text_6mb.csv", label: "6MB", size: 6_000_000, limit: 8_000},
		{name: "large_text_10mb.csv", label: "10MB", size: 10_000_000, limit: 16_000},
	}

	contents := []harness.Content{harness.Text("Read the three CSV excerpts. Reply with exactly this one line and nothing else: LARGE_TEXT_E2E_OK LARGE_TEXT_1MB_HEAD LARGE_TEXT_1MB_TAIL LARGE_TEXT_6MB_HEAD LARGE_TEXT_6MB_TAIL LARGE_TEXT_10MB_HEAD LARGE_TEXT_10MB_TAIL")}
	originals := make([]string, 0, len(tests))
	for _, tc := range tests {
		data, err := os.ReadFile(filepath.Join("testdata", tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != tc.size || !utf8.Valid(data) {
			t.Fatalf("%s bytes=%d valid_utf8=%t", tc.name, len(data), utf8.Valid(data))
		}
		reader := csv.NewReader(strings.NewReader(string(data)))
		for {
			if _, err = reader.Read(); err != nil {
				break
			}
		}
		if err != io.EOF {
			t.Fatalf("parse %s: %v", tc.name, err)
		}
		text := string(data)
		originals = append(originals, text)
		if tc.useDefault {
			contents = append(contents, harness.LargeText(text))
		} else {
			contents = append(contents, harness.LargeText(text, tc.limit))
		}
	}

	a := harness.NewAgent(harness.AgentOptions{Provider: recorded, Model: selected, Generation: nonThinking})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startAgentRun(t, ctx, a, harness.AgentPrompt{Steps: harness.Sequence{harness.UserMessageStep{Content: contents}}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	requests := recorded.snapshot()
	if len(requests) != 1 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	request := requests[0]
	assertContextRoles(t, request, harness.RoleUser, harness.RoleUser, harness.RoleUser, harness.RoleUser)
	for i, tc := range tests {
		got := request.Messages[i+1]
		if len(got.Content) != 1 || got.Content[0].Type != "text" {
			t.Fatalf("%s provider content=%#v", tc.name, got.Content)
		}
		rendered := got.Text()
		originalChars := utf8.RuneCountInString(originals[i])
		marker := fmt.Sprintf("[truncated: text exceeded %d chars; kept head and tail from %d chars]", tc.limit, originalChars)
		for _, want := range []string{"LARGE_TEXT_" + tc.label + "_HEAD", "LARGE_TEXT_" + tc.label + "_TAIL", marker} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("%s rendered text is missing %q", tc.name, want)
			}
		}
	}

	history := a.Messages()
	if len(history) != 2 || len(history[0].Content) != 4 {
		t.Fatalf("history shape=%#v", history)
	}
	for i, original := range originals {
		got := history[0].Content[i+1]
		if got.Type != "largeText" || got.Text != original {
			t.Fatalf("history did not retain original %s", tests[i].name)
		}
	}
	answer := history[len(history)-1]
	if answer.Usage.TotalTokens == 0 || !strings.Contains(answer.Text(), "LARGE_TEXT_E2E_OK") {
		t.Fatalf("answer=%q usage=%#v", answer.Text(), answer.Usage)
	}
	for _, tc := range tests {
		for _, suffix := range []string{"HEAD", "TAIL"} {
			if marker := "LARGE_TEXT_" + tc.label + "_" + suffix; !strings.Contains(answer.Text(), marker) {
				t.Fatalf("answer is missing %s: %q", marker, answer.Text())
			}
		}
	}
}
