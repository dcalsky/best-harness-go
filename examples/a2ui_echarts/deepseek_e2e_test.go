package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

func TestDeepSeekDashboardE2E(t *testing.T) {
	if os.Getenv("BEST_HARNESS_DEEPSEEK_E2E") != "1" {
		t.Skip("set BEST_HARNESS_DEEPSEEK_E2E=1 to run the paid DeepSeek dashboard test")
	}
	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		t.Fatal("DEEPSEEK_API_KEY is not set")
	}
	t.Setenv("POC_PROVIDER", "deepseek")
	t.Setenv("POC_MODEL", "deepseek-v4-flash")
	app, err := newApplicationFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(
		`{"id":"deepseek-dashboard","messages":[{"id":"user-1","role":"user","parts":[{"type":"text","text":"请生成一个展示近六个月新增用户趋势的平滑折线图，使用中文标签"}]}],"trigger":"submit-message","messageId":"user-1"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	recorder := httptest.NewRecorder()
	app.routes().ServeHTTP(recorder, request.WithContext(ctx))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var candidate *chartCandidate
	chunks := readUIStream(t, recorder.Body)
	var finish *uiStreamChunk
	for i := range chunks {
		chunk := chunks[i]
		if chunk.Type == "data-chart" {
			var value chartCandidate
			if err := json.Unmarshal(chunk.Data, &value); err != nil {
				t.Fatal(err)
			}
			candidate = &value
		}
		if chunk.Type == "finish" {
			value := chunk
			finish = &value
		}
	}
	if finish == nil || finish.FinishReason != "stop" {
		t.Fatalf("finish=%#v", finish)
	}
	if candidate == nil || candidate.Chart.Title == "" || len(candidate.Chart.Option) == 0 || candidate.SessionID != "deepseek-dashboard" {
		t.Fatalf("candidate=%#v", candidate)
	}
	if len(app.sessions["deepseek-dashboard"].State().Charts) != 1 {
		t.Fatalf("state=%#v", app.sessions["deepseek-dashboard"].State())
	}
	history := app.sessions["deepseek-dashboard"].RunHistory()
	if len(history) == 0 || history[len(history)-1].Status != harness.StatusCompleted {
		t.Fatalf("run history=%#v", history)
	}
}
