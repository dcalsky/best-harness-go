// Package hertzrun demonstrates process-local Agent Run control through Hertz.
// Run handles remain process-local; multi-instance applications must route
// control requests to the instance that owns the handle.
package hertzrun

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/dcalsky/best-harness-go"
)

var ErrRunNotFound = errors.New("registered run not found")

type RunRegistry[S any] struct {
	mu   sync.RWMutex
	runs map[harness.ID]*RegisteredRun[S]
}

type RegisteredRun[S any] struct {
	TenantID string
	ChatID   string
	Run      *harness.Run[S]
	Session  *harness.Session[S]
}

// NewRunRegistry creates a process-local registry. Multi-instance
// applications must route control requests to the instance owning the run.
func NewRunRegistry[S any]() *RunRegistry[S] {
	return &RunRegistry[S]{runs: make(map[harness.ID]*RegisteredRun[S])}
}

func (r *RunRegistry[S]) Register(registered *RegisteredRun[S], terminalTTL time.Duration) error {
	r.mu.Lock()
	if _, exists := r.runs[registered.Run.ID()]; exists {
		r.mu.Unlock()
		return harness.ErrDuplicateID
	}
	r.runs[registered.Run.ID()] = registered
	r.mu.Unlock()

	go func(id harness.ID, done <-chan struct{}) {
		<-done
		timer := time.NewTimer(terminalTTL)
		defer timer.Stop()
		<-timer.C
		r.mu.Lock()
		delete(r.runs, id)
		r.mu.Unlock()
	}(registered.Run.ID(), registered.Run.Done())
	return nil
}

func (r *RunRegistry[S]) Lookup(tenantID, chatID string, id harness.ID) (*RegisteredRun[S], error) {
	r.mu.RLock()
	registered := r.runs[id]
	r.mu.RUnlock()
	if registered == nil || registered.TenantID != tenantID || registered.ChatID != chatID {
		return nil, ErrRunNotFound
	}
	return registered, nil
}

type SessionResolver[S any] func(ctx context.Context, tenantID, chatID string) (*harness.Session[S], error)

type Server[S any] struct {
	// RunContext belongs to the server process, not an individual HTTP request.
	// Cancel it during graceful shutdown to cancel all child runs.
	RunContext context.Context
	Registry   *RunRegistry[S]
	Session    SessionResolver[S]
	RunTTL     time.Duration
}

type StartRequest struct {
	RunID harness.ID `json:"run_id"`
	Text  string     `json:"text"`
}

type RunResponse struct {
	RunID     harness.ID     `json:"run_id"`
	Status    harness.Status `json:"status"`
	Cause     harness.Cause  `json:"cause,omitempty"`
	Error     string         `json:"error,omitempty"`
	StartedAt time.Time      `json:"started_at,omitempty"`
	EndedAt   time.Time      `json:"ended_at,omitempty"`
}

func (s *Server[S]) RegisterRoutes(h *server.Hertz) {
	h.POST("/chats/:chatID/runs", s.StartRun)
	h.POST("/chats/:chatID/runs/:runID/abort", s.AbortRun)
	h.GET("/chats/:chatID/runs/:runID", s.GetRun)
}

func (s *Server[S]) StartRun(reqCtx context.Context, c *app.RequestContext) {
	tenantID := string(c.Request.Header.Peek("X-Tenant-ID"))
	chatID := c.Param("chatID")
	var body StartRequest
	if err := json.Unmarshal(c.Request.Body(), &body); err != nil || body.Text == "" {
		writeError(c, consts.StatusBadRequest, "invalid request")
		return
	}
	if body.RunID == "" {
		body.RunID = harness.NewID()
	}
	session, err := s.Session(reqCtx, tenantID, chatID)
	if err != nil {
		writeError(c, consts.StatusNotFound, "chat not found")
		return
	}

	// Request cancellation ends only this SSE observer. RunContext determines
	// the lifetime of server-side Agent work.
	run, err := session.Start(s.RunContext, harness.Prompt{Steps: harness.Sequence{
		harness.UserText(body.Text),
	}}, harness.StartOptions{ID: body.RunID})
	if err != nil {
		writeError(c, consts.StatusConflict, err.Error())
		return
	}
	if err := s.Registry.Register(&RegisteredRun[S]{
		TenantID: tenantID,
		ChatID:   chatID,
		Run:      run,
		Session:  session,
	}, s.RunTTL); err != nil {
		run.Abort()
		writeError(c, consts.StatusConflict, err.Error())
		return
	}

	w := sse.NewWriter(c)
	defer w.Close()
	if err := writeSSE(w, "run.started", RunResponse{RunID: run.ID(), Status: run.Status()}); err != nil {
		return
	}

	select {
	case <-reqCtx.Done():
		return
	case <-run.Done():
		info, err := session.RunInfo(run.ID())
		if err != nil {
			return
		}
		_ = writeSSE(w, "run.terminal", responseFromInfo(info))
	}
}

func (s *Server[S]) AbortRun(_ context.Context, c *app.RequestContext) {
	id := harness.ID(c.Param("runID"))
	registered, err := s.Registry.Lookup(
		string(c.Request.Header.Peek("X-Tenant-ID")),
		c.Param("chatID"),
		id,
	)
	if err != nil {
		writeError(c, consts.StatusNotFound, "run not found")
		return
	}
	registered.Run.Abort()
	c.JSON(consts.StatusAccepted, RunResponse{RunID: id, Status: registered.Run.Status()})
}

func (s *Server[S]) GetRun(reqCtx context.Context, c *app.RequestContext) {
	id := harness.ID(c.Param("runID"))
	tenantID := string(c.Request.Header.Peek("X-Tenant-ID"))
	chatID := c.Param("chatID")
	registered, err := s.Registry.Lookup(tenantID, chatID, id)
	var session *harness.Session[S]
	if err == nil {
		session = registered.Session
	} else {
		session, err = s.Session(reqCtx, tenantID, chatID)
		if err != nil {
			writeError(c, consts.StatusNotFound, "run not found")
			return
		}
	}
	info, err := session.RunInfo(id)
	if err != nil {
		writeError(c, consts.StatusNotFound, "run not found")
		return
	}
	c.JSON(consts.StatusOK, responseFromInfo(info))
}

func responseFromInfo(info harness.Info) RunResponse {
	return RunResponse{
		RunID:     info.ID,
		Status:    info.Status,
		Cause:     info.Cause,
		Error:     info.Error,
		StartedAt: info.StartedAt,
		EndedAt:   info.EndedAt,
	}
}

func writeSSE(w *sse.Writer, event string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.WriteEvent("", event, data)
}

func writeError(c *app.RequestContext, status int, text string) {
	c.JSON(status, map[string]string{"error": text})
}
