package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/dcalsky/best-harness-go"
	openaisdk "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
)

const agentID = "chart-agent"

//go:embed web/*
var webFiles embed.FS

type ChartSpec struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	ChartType   string         `json:"chartType"`
	Description string         `json:"description,omitempty"`
	Option      map[string]any `json:"option"`
}

type DashboardState struct {
	Charts []ChartSpec `json:"charts"`
}

type renderChartParams struct {
	Title       string         `json:"title"`
	ChartType   string         `json:"chart_type"`
	Description string         `json:"description,omitempty"`
	Option      map[string]any `json:"option"`
}

type chartToolDetails struct {
	Operation string    `json:"operation"`
	Chart     ChartSpec `json:"chart"`
}

type application struct {
	harness      *harness.Harness[DashboardState]
	model        harness.Model
	providerName string

	mu       sync.Mutex
	sessions map[string]*harness.Session[DashboardState]
}

func newOpenAIProvider(providerName string) *harness.OpenAIProvider {
	prefix := strings.ToUpper(providerName)
	defaults := map[string]string{"openai": "https://api.openai.com/v1", "deepseek": "https://api.deepseek.com"}
	client := openaisdk.NewClient(
		openaioption.WithAPIKey(os.Getenv(prefix+"_API_KEY")),
		openaioption.WithBaseURL(envOr(prefix+"_BASE_URL", defaults[providerName])),
	)
	return harness.NewOpenAIProvider(client)
}

func newApplicationFromEnv() (*application, error) {
	providerName := strings.ToLower(envOr("POC_PROVIDER", "demo"))
	modelID := os.Getenv("POC_MODEL")
	if modelID == "" {
		switch providerName {
		case "deepseek":
			modelID = "deepseek-v4-flash"
		case "openai":
			modelID = envOr("OPENAI_MODEL", "gpt-5-mini")
		default:
			modelID = "chart-agent-demo"
		}
	}
	selected := harness.Model{Provider: providerName, API: harness.APIOpenAI, ID: modelID, ContextWindow: 128_000, MaxOutput: 4_096}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		return nil, err
	}
	h, err := harness.New[DashboardState](harness.Options{Models: models})
	if err != nil {
		return nil, err
	}

	switch providerName {
	case "demo":
		err = h.RegisterProvider(providerName, newDemoProvider())
	case "openai", "deepseek":
		err = h.RegisterProvider(providerName, newOpenAIProvider(providerName))
	default:
		return nil, fmt.Errorf("unsupported POC_PROVIDER %q (want demo, deepseek, or openai)", providerName)
	}
	if err != nil {
		return nil, err
	}

	app := &application{harness: h, model: selected, providerName: providerName, sessions: make(map[string]*harness.Session[DashboardState])}
	if err := app.registerTools(); err != nil {
		return nil, err
	}
	return app, nil
}

func (a *application) registerTools() error {
	return a.harness.RegisterTool(harness.ToolSpec{
		Name: "render_chart", Description: "Generate one ECharts chart for the user to preview and optionally add to the dashboard.",
		ExecutionMode: harness.Sequential,
		RawParameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"title":{"type":"string","description":"Short title displayed above the chart."},
				"chart_type":{"type":"string","enum":["line","bar","pie","scatter","radar","gauge","area"]},
				"description":{"type":"string","description":"One sentence explaining the chart."},
				"option":{"type":"object","description":"A complete, data-only Apache ECharts option object."}
			},
			"required":["title","chart_type","option"],
			"additionalProperties":false
		}`),
	}, func(ctx context.Context, c harness.Context[DashboardState], params renderChartParams) (harness.ToolResult[chartToolDetails], error) {
		params.Title = strings.TrimSpace(params.Title)
		params.ChartType = strings.ToLower(strings.TrimSpace(params.ChartType))
		if params.Title == "" || params.ChartType == "" || len(params.Option) == 0 {
			return harness.ToolResult[chartToolDetails]{Content: []harness.Content{harness.Text("title, chart_type and option are required")}, IsError: true}, nil
		}
		if !allowedChartType(params.ChartType) {
			return harness.ToolResult[chartToolDetails]{Content: []harness.Content{harness.Text("unsupported chart type: " + params.ChartType)}, IsError: true}, nil
		}

		id := "chart-" + strings.ReplaceAll(string(harness.NewID()), "-", "")[:12]
		chart := ChartSpec{ID: id, Title: params.Title, ChartType: params.ChartType, Description: strings.TrimSpace(params.Description), Option: params.Option}
		if err := c.UpdateState(func(state *DashboardState) { state.Charts = append(state.Charts, chart) }); err != nil {
			return harness.ToolResult[chartToolDetails]{}, err
		}
		details := chartToolDetails{Operation: "upsert", Chart: chart}
		return harness.ToolResult[chartToolDetails]{
			Content: []harness.Content{harness.Text(fmt.Sprintf("Chart %q was generated with id %s and is ready for preview.", chart.Title, chart.ID))},
			Details: details,
		}, nil
	})
}

func allowedChartType(value string) bool {
	switch value {
	case "line", "bar", "pie", "scatter", "radar", "gauge", "area":
		return true
	default:
		return false
	}
}

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "provider": a.providerName, "protocol": "ag-ui"})
	})
	mux.HandleFunc("GET /api/copilotkit/info", a.handleRuntimeInfo)
	mux.HandleFunc("POST /api/copilotkit/agent/{agentID}/run", a.handleRun)
	mux.HandleFunc("POST /api/copilotkit/agent/{agentID}/connect", a.handleConnect)
	mux.HandleFunc("POST /api/copilotkit/agent/{agentID}/stop/{threadID}", a.handleStop)
	mux.HandleFunc("GET /assets/{asset...}", a.serveAsset)
	mux.HandleFunc("GET /", a.serveIndex)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *application) serveIndex(w http.ResponseWriter, _ *http.Request) {
	data, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "frontend build is missing; run npm run build in frontend", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (a *application) serveAsset(w http.ResponseWriter, r *http.Request) {
	asset := r.PathValue("asset")
	if !fs.ValidPath(asset) {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	data, err := webFiles.ReadFile("web/assets/" + asset)
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(asset))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

type runtimeInfo struct {
	Version                       string                      `json:"version"`
	Agents                        map[string]agentDescription `json:"agents"`
	AudioFileTranscriptionEnabled bool                        `json:"audioFileTranscriptionEnabled"`
	Mode                          string                      `json:"mode"`
	Suggestions                   bool                        `json:"suggestions"`
	TelemetryDisabled             bool                        `json:"telemetryDisabled"`
}

type agentDescription struct {
	Name         string         `json:"name"`
	ClassName    string         `json:"className"`
	Description  string         `json:"description"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
}

func (a *application) handleRuntimeInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, runtimeInfo{
		Version: "1.69.0-compatible", Mode: "sse", AudioFileTranscriptionEnabled: false, Suggestions: false, TelemetryDisabled: true,
		Agents: map[string]agentDescription{agentID: {
			Name: agentID, ClassName: "BestHarnessAGUIAgent", Description: "Analytics dashboard agent backed by best-harness-go AG-UI conversion.", Capabilities: map[string]any{},
		}},
	})
}

type runAgentInput struct {
	ThreadID        string            `json:"threadId"`
	RunID           string            `json:"runId"`
	State           json.RawMessage   `json:"state"`
	Messages        []aguiMessage     `json:"messages"`
	Tools           []json.RawMessage `json:"tools"`
	Context         []json.RawMessage `json:"context"`
	ForwardedProps  json.RawMessage   `json:"forwardedProps"`
	LastSeenEventID *string           `json:"lastSeenEventId,omitempty"`
}

type aguiMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId,omitempty"`
}

func (input runAgentInput) prompt() string {
	for i := len(input.Messages) - 1; i >= 0; i-- {
		if input.Messages[i].Role == "user" {
			return messageText(input.Messages[i].Content)
		}
	}
	return ""
}

func messageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var values []string
	for _, part := range parts {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			values = append(values, part.Text)
		}
	}
	return strings.TrimSpace(strings.Join(values, "\n"))
}

func (a *application) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("agentID") != agentID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}
	var input runAgentInput
	if err := decodeBody(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body", "details": err.Error()})
		return
	}
	prompt := input.prompt()
	if !validProtocolID(input.ThreadID) || !validProtocolID(input.RunID) || prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "threadId, runId, and a user message are required"})
		return
	}
	session, err := a.session(r.Context(), input.ThreadID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is not supported"})
		return
	}
	events := make(chan harness.AgentEvent, 64)
	unsubscribe := session.On(func(_ context.Context, _ harness.Context[DashboardState], event harness.AgentEvent) {
		select {
		case events <- event:
		case <-r.Context().Done():
		}
	})
	defer unsubscribe()

	run, err := session.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText(prompt)}}, harness.StartOptions{ID: harness.ID(input.RunID)})
	if err != nil {
		status := http.StatusConflict
		if !errors.Is(err, harness.ErrAgentBusy) {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	harness.SetAGUIHeaders(w.Header())
	encoder := harness.NewAGUISSEEncoder(w)
	adapter := harness.NewAGUIAdapter(harness.AGUIOptions{ThreadID: input.ThreadID, Input: input})
	start, err := adapter.Start(harness.RunEvent{RunID: run.ID(), Status: run.Status()})
	if err != nil || encoder.EncodeAll(start) != nil {
		return
	}
	flusher.Flush()
	emit := func(event harness.AgentEvent) bool {
		frames, encodeErr := adapter.Encode(event)
		if encodeErr != nil || encoder.EncodeAll(frames) != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case event := <-events:
			if !emit(event) {
				return
			}
		case <-run.Done():
			for {
				select {
				case event := <-events:
					if !emit(event) {
						return
					}
				default:
					finishEvent := harness.RunEvent{RunID: run.ID(), Status: run.Status(), Err: run.Err()}
					if info, infoErr := session.RunInfo(run.ID()); infoErr == nil {
						finishEvent.Cause = info.Cause
					}
					finish, finishErr := adapter.Finish(finishEvent)
					if finishErr != nil || encoder.EncodeAll(finish) != nil || encoder.Close() != nil {
						return
					}
					flusher.Flush()
					return
				}
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (a *application) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("agentID") != agentID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}
	var input runAgentInput
	if err := decodeBody(r, &input); err != nil || !validProtocolID(input.ThreadID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid threadId is required"})
		return
	}
	a.mu.Lock()
	session := a.sessions[input.ThreadID]
	a.mu.Unlock()

	harness.SetAGUIHeaders(w.Header())
	encoder := harness.NewAGUISSEEncoder(w)
	var messages []any
	if session != nil {
		messages = snapshotMessages(session.Conversation().Messages)
	}
	_ = encoder.Encode(harness.AGUIMessagesSnapshotEvent{BaseEvent: harness.AGUIBaseEvent{Type: harness.AGUIEventMessagesSnapshot}, Messages: messages})
	_ = encoder.Close()
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (a *application) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("agentID") != agentID || !validProtocolID(r.PathValue("threadID")) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent or thread not found"})
		return
	}
	a.mu.Lock()
	session := a.sessions[r.PathValue("threadID")]
	a.mu.Unlock()
	stopped := false
	if session != nil {
		if run := session.ActiveRun(); run != nil {
			run.Abort()
			stopped = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": stopped})
}

func snapshotMessages(messages []harness.Message) []any {
	out := make([]any, 0, len(messages))
	for index, message := range messages {
		id := fmt.Sprintf("history-%d-%d", message.Timestamp, index)
		switch message.Role {
		case harness.RoleUser:
			out = append(out, map[string]any{"id": id, "role": "user", "content": message.Text()})
		case harness.RoleAssistant:
			value := map[string]any{"id": id, "role": "assistant", "content": message.Text()}
			var calls []any
			for _, content := range message.Content {
				if content.Type == "toolCall" {
					calls = append(calls, map[string]any{"id": content.ID, "type": "function", "function": map[string]any{"name": content.Name, "arguments": string(content.Arguments)}})
				}
			}
			if len(calls) > 0 {
				value["toolCalls"] = calls
			}
			out = append(out, value)
		case harness.RoleTool:
			out = append(out, map[string]any{"id": id, "role": "tool", "toolCallId": message.ToolCallID, "content": message.Text()})
		}
	}
	return out
}

func (a *application) session(ctx context.Context, threadID string) (*harness.Session[DashboardState], error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if session := a.sessions[threadID]; session != nil {
		return session, nil
	}
	var generation harness.GenerationConfig
	if a.providerName == "deepseek" {
		generation.Thinking = harness.Ptr(false)
	}
	session, err := a.harness.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{
		ID: "agui-" + threadID, Model: &a.model, ActiveTools: []string{"render_chart"}, Generation: generation,
		SystemPrompt: `You are an analytics dashboard copilot. Convert every chart request into one render_chart call.
Choose a useful chart type, realistic sample data, concise Chinese labels when the user writes Chinese, and a complete ECharts option object.
After the tool succeeds, briefly explain that the chart is ready to preview and can be added by the user. Do not claim it was already added. Do not emit HTML, JavaScript, or markdown code fences.`,
	}, DashboardState{})
	if err != nil {
		return nil, err
	}
	a.sessions[threadID] = session
	return session, nil
}

func (a *application) Close() {
	a.mu.Lock()
	sessions := make([]*harness.Session[DashboardState], 0, len(a.sessions))
	for _, session := range a.sessions {
		sessions = append(sessions, session)
	}
	a.sessions = make(map[string]*harness.Session[DashboardState])
	a.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
}

var protocolIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func validProtocolID(value string) bool { return protocolIDPattern.MatchString(value) }

func decodeBody(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
