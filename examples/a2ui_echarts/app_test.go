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

type uiStreamChunk struct {
	Type         string          `json:"type"`
	ID           string          `json:"id,omitempty"`
	Delta        string          `json:"delta,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
	FinishReason string          `json:"finishReason,omitempty"`
}

func readUIStream(t *testing.T, body io.Reader) []uiStreamChunk {
	t.Helper()
	var chunks []uiStreamChunk
	scanner := bufio.NewScanner(body)
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
			chunks = append(chunks, uiStreamChunk{Type: "[DONE]"})
			continue
		}
		var chunk uiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", data, err)
		}
		chunks = append(chunks, chunk)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return chunks
}

func TestChatStreamsAgentToolAndChartCandidate(t *testing.T) {
	t.Setenv("POC_PROVIDER", "demo")
	app, err := newApplicationFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	body := bytes.NewBufferString(`{"id":"test-chat","messages":[{"id":"user-1","role":"user","parts":[{"type":"text","text":"添加一个渠道占比饼图"}]}],"trigger":"submit-message","messageId":"user-1"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type=%q", got)
	}
	if got := recorder.Header().Get("X-Vercel-AI-UI-Message-Stream"); got != "v1" {
		t.Fatalf("stream protocol=%q", got)
	}
	chunks := readUIStream(t, recorder.Body)

	var candidate *chartCandidate
	var assistantText string
	for _, chunk := range chunks {
		if chunk.Type == "data-chart" {
			var value chartCandidate
			if err := json.Unmarshal(chunk.Data, &value); err != nil {
				t.Fatal(err)
			}
			candidate = &value
		}
		if chunk.Type == "text-delta" {
			assistantText += chunk.Delta
		}
	}
	if candidate == nil {
		t.Fatalf("missing chart candidate chunk: %#v", chunks)
	}
	chart := candidate.Chart
	if chart.ChartType != "pie" || chart.Title == "" {
		t.Fatalf("unexpected chart: %#v", chart)
	}
	if candidate.SessionID != "test-chat" {
		t.Fatalf("candidate has wrong session: %#v", candidate)
	}
	if assistantText == "" {
		t.Fatal("missing streamed assistant response")
	}
	if len(chunks) == 0 || chunks[len(chunks)-1].Type != "[DONE]" {
		t.Fatalf("missing DONE marker: %#v", chunks)
	}
	state := app.sessions["test-chat"].State()
	if len(state.Charts) != 1 || state.Charts[0].ID != chart.ID {
		t.Fatalf("candidate was not committed to session state: %#v", state)
	}

	historyRequest := httptest.NewRequest(http.MethodGet, "/api/sessions/test-chat/messages", nil)
	historyRecorder := httptest.NewRecorder()
	app.routes().ServeHTTP(historyRecorder, historyRequest)
	if historyRecorder.Code != http.StatusOK || !strings.Contains(historyRecorder.Body.String(), `"type":"data-chart"`) || !strings.Contains(historyRecorder.Body.String(), chart.ID) {
		t.Fatalf("restored history status=%d body=%s", historyRecorder.Code, historyRecorder.Body.String())
	}
}

func TestPagesAndSecurityHeaders(t *testing.T) {
	t.Setenv("POC_PROVIDER", "demo")
	app, err := newApplicationFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	for _, target := range []string{"/", "/ai/index.html"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		recorder := httptest.NewRecorder()
		app.routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d", target, recorder.Code)
		}
		if recorder.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s missing CSP", target)
		}
	}
}
