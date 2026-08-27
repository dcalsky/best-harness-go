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
	selected := harness.Model{
		Provider:      providerName,
		API:           harness.APIOpenAI,
		ID:            modelID,
		ContextWindow: 128_000,
		MaxOutput:     4_096,
	}
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
		if err := h.RegisterProvider(providerName, newDemoProvider()); err != nil {
			return nil, err
		}
	case "openai", "deepseek":
		if err := h.RegisterProvider(providerName, newOpenAIProvider(providerName)); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported POC_PROVIDER %q (want demo, deepseek, or openai)", providerName)
	}

	app := &application{
		harness:      h,
		model:        selected,
		providerName: providerName,
		sessions:     make(map[string]*harness.Session[DashboardState]),
	}
	if err := app.registerTools(); err != nil {
		return nil, err
	}
	return app, nil
}

func (a *application) registerTools() error {
	return a.harness.RegisterTool(harness.ToolSpec{
		Name:          "render_chart",
		Description:   "Generate one ECharts chart candidate for the user to preview and optionally add to the analytics dashboard.",
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
			return harness.ToolResult[chartToolDetails]{
				Content: []harness.Content{harness.Text("title, chart_type and option are required")},
				IsError: true,
			}, nil
		}
		if !allowedChartType(params.ChartType) {
			return harness.ToolResult[chartToolDetails]{
				Content: []harness.Content{harness.Text("unsupported chart type: " + params.ChartType)},
				IsError: true,
			}, nil
		}

		id := "chart-" + strings.ReplaceAll(string(harness.NewID()), "-", "")[:12]
		chart := ChartSpec{
			ID:          id,
			Title:       params.Title,
			ChartType:   params.ChartType,
			Description: strings.TrimSpace(params.Description),
			Option:      params.Option,
		}
		if err := c.UpdateState(func(state *DashboardState) {
			state.Charts = append(state.Charts, chart)
		}); err != nil {
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
	mux.HandleFunc("GET /{$}", a.serveAIIndex)
	mux.HandleFunc("GET /ai/{asset...}", a.serveAIBuildAsset)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "provider": a.providerName})
	})
	mux.HandleFunc("GET /api/sessions/{chatID}/messages", a.handleSessionMessages)
	mux.HandleFunc("POST /api/chat", a.handleChat)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (a *application) serveAIIndex(w http.ResponseWriter, _ *http.Request) {
	data, err := webFiles.ReadFile("web/ai/index.html")
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (a *application) serveAIBuildAsset(w http.ResponseWriter, r *http.Request) {
	asset := r.PathValue("asset")
	if !fs.ValidPath(asset) {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	data, err := webFiles.ReadFile("web/ai/" + asset)
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

type chatRequest struct {
	ID        string            `json:"id,omitempty"`
	Messages  []json.RawMessage `json:"messages,omitempty"`
	Trigger   string            `json:"trigger,omitempty"`
	MessageID *string           `json:"messageId,omitempty"`
}

func (request chatRequest) prompt() (chatID, text string) {
	chatID = request.ID
	for i := len(request.Messages) - 1; i >= 0; i-- {
		var message struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		}
		if json.Unmarshal(request.Messages[i], &message) != nil || message.Role != "user" {
			continue
		}
		var parts []string
		for _, raw := range message.Parts {
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &part) == nil && part.Type == "text" && part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
		return chatID, strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return chatID, ""
}

func (a *application) handleChat(w http.ResponseWriter, r *http.Request) {
	var request chatRequest
	if err := decodeBody(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid AI SDK chat request is required"})
		return
	}
	chatID, text := request.prompt()
	if !validChatID(chatID) || text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid chat id and user text are required"})
		return
	}
	session, err := a.session(r.Context(), chatID)
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

	run, err := session.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{
		harness.UserText(text),
	}}, harness.StartOptions{})
	if err != nil {
		status := http.StatusConflict
		if !errors.Is(err, harness.ErrAgentBusy) {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	harness.SetVercelAIHeaders(w.Header())
	encoder := harness.NewVercelAISSEEncoder(w)
	adapter := harness.NewVercelAIAdapter(harness.VercelAIOptions{MapEvent: func(event harness.AgentEvent) ([]harness.VercelAIChunk, error) {
		return chartCandidateChunksFromAgentEvent(chatID, event)
	}})
	start, err := adapter.Start(harness.RunEvent{RunID: run.ID(), Status: run.Status()})
	if err != nil || encoder.EncodeAll(start) != nil {
		return
	}
	flusher.Flush()
	emit := func(event harness.AgentEvent) bool {
		chunks, encodeErr := adapter.Encode(event)
		if encodeErr != nil || encoder.EncodeAll(chunks) != nil {
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
					finish, finishErr := adapter.Finish(harness.RunEvent{RunID: run.ID(), Status: run.Status(), Err: run.Err()})
					if finishErr != nil || encoder.EncodeAll(finish) != nil || encoder.Close() != nil {
						return
					}
					flusher.Flush()
					return
				}
			}
		case <-r.Context().Done():
			// The SDK Run intentionally outlives a disconnected stream observer.
			return
		}
	}
}

type chartCandidate struct {
	SessionID string    `json:"sessionId"`
	Chart     ChartSpec `json:"chart"`
}

func chartCandidateChunksFromAgentEvent(chatID string, event harness.AgentEvent) ([]harness.VercelAIChunk, error) {
	e := event.Event
	if e.Type != harness.AgentEventToolEnd || e.Call == nil || e.Result == nil || e.Result.IsError {
		return nil, nil
	}
	details, ok := e.Result.Details.(chartToolDetails)
	if !ok {
		return nil, nil
	}
	chunk, err := harness.VercelAIData("chart", chartCandidate{SessionID: chatID, Chart: details.Chart}, false)
	if err != nil {
		return nil, err
	}
	return []harness.VercelAIChunk{chunk}, nil
}

type uiHistoryMessage struct {
	ID    string          `json:"id"`
	Role  string          `json:"role"`
	Parts []uiHistoryPart `json:"parts"`
}

type uiHistoryPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Data any    `json:"data,omitempty"`
}

func (a *application) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatID")
	if !validChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid chat id is required"})
		return
	}
	a.mu.Lock()
	session := a.sessions[chatID]
	a.mu.Unlock()
	if session == nil {
		writeJSON(w, http.StatusOK, []uiHistoryMessage{})
		return
	}

	candidates := session.State().Charts
	candidateIndex := 0
	var history []uiHistoryMessage
	for index, message := range session.Conversation().Messages {
		parts := make([]uiHistoryPart, 0, len(message.Content))
		for _, content := range message.Content {
			switch content.Type {
			case "text", "largeText":
				if content.Text != "" {
					parts = append(parts, uiHistoryPart{Type: "text", Text: content.Text})
				}
			case "toolCall":
				if content.Name == "render_chart" && candidateIndex < len(candidates) {
					chart := candidates[candidateIndex]
					candidateIndex++
					parts = append(parts, uiHistoryPart{Type: "data-chart", Data: chartCandidate{SessionID: chatID, Chart: chart}})
				}
			}
		}
		role := string(message.Role)
		if role == "toolResult" || len(parts) == 0 {
			continue
		}
		history = append(history, uiHistoryMessage{
			ID: fmt.Sprintf("history-%s-%d", chatID, index), Role: role, Parts: parts,
		})
	}
	writeJSON(w, http.StatusOK, history)
}

func (a *application) session(ctx context.Context, chatID string) (*harness.Session[DashboardState], error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if session := a.sessions[chatID]; session != nil {
		return session, nil
	}
	var generation harness.GenerationConfig
	if a.providerName == "deepseek" {
		generation.Thinking = harness.Ptr(false)
	}
	session, err := a.harness.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{
		ID:          "chat-" + chatID,
		Model:       &a.model,
		ActiveTools: []string{"render_chart"},
		Generation:  generation,
		SystemPrompt: `You are an analytics dashboard copilot. Convert every chart request into one render_chart call.
Choose a useful chart type, realistic sample data, concise Chinese labels when the user writes Chinese, and a complete ECharts option object.
After the tool succeeds, briefly explain that the chart is ready to preview and can be added by the user. Do not claim it was already added. Do not emit HTML, JavaScript, or markdown code fences.`,
	}, DashboardState{})
	if err != nil {
		return nil, err
	}
	a.sessions[chatID] = session
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

func validChatID(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(value)
}

func decodeBody(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
