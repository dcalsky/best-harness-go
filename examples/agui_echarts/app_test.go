package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type wireEvent struct {
	Type            string          `json:"type"`
	ThreadID        string          `json:"threadId,omitempty"`
	RunID           string          `json:"runId,omitempty"`
	MessageID       string          `json:"messageId,omitempty"`
	ParentMessageID string          `json:"parentMessageId,omitempty"`
	ToolCallID      string          `json:"toolCallId,omitempty"`
	ToolCallName    string          `json:"toolCallName,omitempty"`
	Delta           string          `json:"delta,omitempty"`
	Content         string          `json:"content,omitempty"`
	Messages        json.RawMessage `json:"messages,omitempty"`
}

func readAGUIStream(t *testing.T, body io.Reader) []wireEvent {
	t.Helper()
	var events []wireEvent
	scanner := bufio.NewScanner(body)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("invalid SSE line %q", line)
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			t.Fatal("AG-UI must terminate by closing the stream, not [DONE]")
		}
		var event wireEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("decode event %q: %v", data, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func runInput(threadID, runID, prompt string) string {
	return `{"threadId":"` + threadID + `","runId":"` + runID + `","state":{},"messages":[{"id":"user-1","role":"user","content":"` + prompt + `"}],"tools":[],"context":[],"forwardedProps":{}}`
}

func TestCopilotKitRuntimeStreamsVerifiedAGUIToolLifecycle(t *testing.T) {
	t.Setenv("POC_PROVIDER", "demo")
	app, err := newApplicationFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/copilotkit/agent/chart-agent/run", bytes.NewBufferString(runInput("thread-test", "run-test", "生成渠道占比饼图")))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type=%q", got)
	}
	events := readAGUIStream(t, recorder.Body)
	if len(events) == 0 || events[0].Type != "RUN_STARTED" || events[0].ThreadID != "thread-test" || events[0].RunID != "run-test" {
		t.Fatalf("bad start event: %#v", events)
	}
	if events[len(events)-1].Type != "RUN_FINISHED" || events[len(events)-1].ThreadID != "thread-test" || events[len(events)-1].RunID != "run-test" {
		t.Fatalf("bad terminal event: %#v", events[len(events)-1])
	}

	var messageID, toolID, args string
	indices := map[string]int{}
	counts := map[string]int{}
	var result string
	for index, event := range events {
		counts[event.Type]++
		if _, exists := indices[event.Type]; !exists {
			indices[event.Type] = index
		}
		switch event.Type {
		case "TEXT_MESSAGE_START":
			if messageID == "" {
				messageID = event.MessageID
			}
		case "TOOL_CALL_START":
			toolID = event.ToolCallID
			if event.ToolCallName != "render_chart" || event.ParentMessageID == "" || event.ParentMessageID != messageID {
				t.Fatalf("tool/message association is invalid: %#v messageID=%q", event, messageID)
			}
		case "TOOL_CALL_ARGS":
			if event.ToolCallID == toolID {
				args += event.Delta
			}
		case "TOOL_CALL_RESULT":
			if event.ToolCallID != toolID || event.MessageID == "" {
				t.Fatalf("bad result association: %#v toolID=%q", event, toolID)
			}
			result = event.Content
		}
	}
	for _, required := range []string{"REASONING_START", "REASONING_END", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT", "TEXT_MESSAGE_CONTENT"} {
		if counts[required] == 0 {
			t.Fatalf("missing %s: %#v", required, events)
		}
	}
	if !(indices["TOOL_CALL_START"] < indices["TOOL_CALL_ARGS"] && indices["TOOL_CALL_ARGS"] < indices["TOOL_CALL_END"] && indices["TOOL_CALL_END"] < indices["TOOL_CALL_RESULT"]) {
		t.Fatalf("bad tool lifecycle ordering: %#v", indices)
	}
	if counts["RUN_FINISHED"] != 1 || counts["RUN_ERROR"] != 0 {
		t.Fatalf("terminal counts: %#v", counts)
	}
	var parsedArgs map[string]any
	if json.Unmarshal([]byte(args), &parsedArgs) != nil || parsedArgs["chart_type"] != "pie" {
		t.Fatalf("invalid streamed arguments %q", args)
	}
	var parsedResult struct {
		Details chartToolDetails `json:"details"`
	}
	if json.Unmarshal([]byte(result), &parsedResult) != nil || parsedResult.Details.Chart.ID == "" || parsedResult.Details.Chart.ChartType != "pie" {
		t.Fatalf("invalid structured tool result %q", result)
	}
}

func TestRuntimeInfoAndConnectSnapshot(t *testing.T) {
	t.Setenv("POC_PROVIDER", "demo")
	app, err := newApplicationFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	infoRequest := httptest.NewRequest(http.MethodGet, "/api/copilotkit/info", nil)
	infoRecorder := httptest.NewRecorder()
	app.routes().ServeHTTP(infoRecorder, infoRequest)
	if infoRecorder.Code != http.StatusOK || !strings.Contains(infoRecorder.Body.String(), `"chart-agent"`) || !strings.Contains(infoRecorder.Body.String(), `"mode":"sse"`) {
		t.Fatalf("info status=%d body=%s", infoRecorder.Code, infoRecorder.Body.String())
	}

	runRequest := httptest.NewRequest(http.MethodPost, "/api/copilotkit/agent/chart-agent/run", bytes.NewBufferString(runInput("restore-thread", "restore-run", "生成柱状图")))
	runRecorder := httptest.NewRecorder()
	app.routes().ServeHTTP(runRecorder, runRequest)
	if runRecorder.Code != http.StatusOK {
		t.Fatalf("run status=%d body=%s", runRecorder.Code, runRecorder.Body.String())
	}

	connectRequest := httptest.NewRequest(http.MethodPost, "/api/copilotkit/agent/chart-agent/connect", bytes.NewBufferString(runInput("restore-thread", "connect-run", "ignored")))
	connectRecorder := httptest.NewRecorder()
	app.routes().ServeHTTP(connectRecorder, connectRequest)
	if connectRecorder.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", connectRecorder.Code, connectRecorder.Body.String())
	}
	events := readAGUIStream(t, connectRecorder.Body)
	if len(events) != 1 || events[0].Type != "MESSAGES_SNAPSHOT" || !bytes.Contains(events[0].Messages, []byte(`"toolCalls"`)) || !bytes.Contains(events[0].Messages, []byte(`"role":"tool"`)) {
		t.Fatalf("bad snapshot: %#v body=%s", events, connectRecorder.Body.String())
	}
}
