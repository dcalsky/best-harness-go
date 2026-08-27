package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type sqlFrontendDetails struct {
	AffectRows int64  `json:"affect_rows"`
	Phase      string `json:"phase"`
}

type sqlFrontendEvent struct {
	Type       string              `json:"type"`
	ToolCallID string              `json:"tool_call_id"`
	Output     []harness.Content   `json:"output"`
	Data       *sqlFrontendDetails `json:"data"`
}

func TestSQLToolFrontendExampleAsAProcess(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".")
	command.Dir = filepath.Join(filepath.Dir(workingDirectory), "examples", "sql_tool_frontend")
	var output, stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("run example: %v\n%s", err, stderr.String())
	}

	var events []sqlFrontendEvent
	scanner := bufio.NewScanner(&output)
	for scanner.Scan() {
		var event sqlFrontendEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode event: %v\n%s", err, scanner.Text())
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events=%#v", events)
	}
	if events[0].Type != "tool_update" || events[0].Data == nil || events[0].Data.Phase != "running" {
		t.Fatalf("update=%#v", events[0])
	}
	result := events[1]
	if result.Type != "tool_result" || result.ToolCallID != "sql-1" || result.Data == nil {
		t.Fatalf("result=%#v", result)
	}
	if result.Data.AffectRows != 1 || result.Data.Phase != "completed" {
		t.Fatalf("details=%#v", result.Data)
	}
	if len(result.Output) != 1 || !strings.Contains(result.Output[0].Text, "affected rows: 1") {
		t.Fatalf("output=%#v", result.Output)
	}
}
