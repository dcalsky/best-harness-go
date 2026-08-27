package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"github.com/dcalsky/best-harness-go"
	"github.com/dcalsky/best-harness-go/examples/run_registry/hertzrun"
)

type startRequest = hertzrun.StartRequest
type runResponse = hertzrun.RunResponse

type runControl struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newRunControl() *runControl {
	return &runControl{started: make(chan struct{}), release: make(chan struct{})}
}

func (c *runControl) markStarted() { c.startedOnce.Do(func() { close(c.started) }) }
func (c *runControl) complete()    { c.releaseOnce.Do(func() { close(c.release) }) }

type controlledProvider struct {
	mu       sync.Mutex
	controls map[string]*runControl
}

func newControlledProvider() *controlledProvider {
	return &controlledProvider{controls: make(map[string]*runControl)}
}

func (p *controlledProvider) control(key string) *runControl {
	p.mu.Lock()
	defer p.mu.Unlock()
	control := p.controls[key]
	if control == nil {
		control = newRunControl()
		p.controls[key] = control
	}
	return control
}

func (p *controlledProvider) Stream(ctx context.Context, request harness.Request) (harness.Stream, error) {
	key := ""
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == harness.RoleUser {
			key = request.Messages[i].Text()
			break
		}
	}
	if key == "" {
		return nil, errors.New("controlled provider requires a user message")
	}
	control := p.control(key)
	control.markStarted()
	return &controlledStream{ctx: ctx, control: control, text: key}, nil
}

type controlledStream struct {
	ctx     context.Context
	control *runControl
	text    string
	phase   int
}

func (s *controlledStream) Next() (harness.StreamEvent, error) {
	if s.phase == 0 {
		s.phase++
		return harness.StreamEvent{Type: harness.EventTextDelta, Text: "partial:" + s.text}, nil
	}
	if s.phase == 2 {
		return harness.StreamEvent{}, io.EOF
	}
	select {
	case <-s.control.release:
		s.phase++
		return harness.StreamEvent{Type: harness.EventDone, StopReason: harness.StopStop}, nil
	case <-s.ctx.Done():
		return harness.StreamEvent{}, s.ctx.Err()
	}
}

func (s *controlledStream) Close() error { return nil }

type apiFixture struct {
	t          *testing.T
	baseURL    string
	client     *http.Client
	server     *server.Hertz
	api        *hertzrun.Server[harness.NoState]
	provider   *controlledProvider
	runCancel  context.CancelFunc
	sessionMu  sync.Mutex
	sessions   map[string]*harness.Session[harness.NoState]
	harness    *harness.Harness[harness.NoState]
	model      harness.Model
	serverDone chan error
}

func newAPIFixture(t *testing.T, ttl time.Duration) *apiFixture {
	t.Helper()
	controlled := newControlledProvider()
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "controlled", ID: "e2e", ContextWindow: 32_000, MaxOutput: 1_000}
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("controlled", controlled); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	fixture := &apiFixture{
		t:          t,
		baseURL:    "http://" + listener.Addr().String(),
		client:     &http.Client{Transport: &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 100}},
		provider:   controlled,
		runCancel:  runCancel,
		sessions:   make(map[string]*harness.Session[harness.NoState]),
		harness:    h,
		model:      selected,
		serverDone: make(chan error, 1),
	}
	fixture.api = &hertzrun.Server[harness.NoState]{
		RunContext: runCtx,
		Registry:   hertzrun.NewRunRegistry[harness.NoState](),
		RunTTL:     ttl,
		Session:    fixture.resolveSession,
	}
	fixture.server = server.New(
		server.WithListener(listener),
		server.WithTransport(standard.NewTransporter),
		server.WithSenseClientDisconnection(true),
		server.WithExitWaitTime(200*time.Millisecond),
		server.WithDisablePrintRoute(true),
	)
	fixture.api.RegisterRoutes(fixture.server)
	go func() { fixture.serverDone <- fixture.server.Run() }()
	t.Cleanup(fixture.close)
	return fixture
}

func (f *apiFixture) resolveSession(_ context.Context, tenantID, chatID string) (*harness.Session[harness.NoState], error) {
	if tenantID == "" || chatID == "" {
		return nil, errors.New("tenant and chat are required")
	}
	key := tenantID + "\x00" + chatID
	f.sessionMu.Lock()
	defer f.sessionMu.Unlock()
	if existing := f.sessions[key]; existing != nil {
		return existing, nil
	}
	session, err := f.harness.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &f.model}, harness.NoState{})
	if err != nil {
		return nil, err
	}
	f.sessions[key] = session
	return session, nil
}

func (f *apiFixture) close() {
	f.runCancel()
	f.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = f.server.Shutdown(ctx)
	cancel()
	f.sessionMu.Lock()
	sessions := make([]*harness.Session[harness.NoState], 0, len(f.sessions))
	for _, session := range f.sessions {
		sessions = append(sessions, session)
	}
	f.sessionMu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	select {
	case <-f.serverDone:
	case <-time.After(time.Second):
	}
}

type sseFrame struct {
	Event string
	Data  []byte
}

type startedRun struct {
	response *http.Response
	reader   *bufio.Reader
	started  runResponse
}

func (f *apiFixture) start(ctx context.Context, tenantID, chatID string, body startRequest) (*startedRun, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL+"/chats/"+chatID+"/runs", strings.NewReader(string(payload)))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Tenant-ID", tenantID)
	response, err := f.client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, response.StatusCode, nil
	}
	reader := bufio.NewReader(response.Body)
	frame, err := readSSE(reader)
	if err != nil {
		response.Body.Close()
		return nil, response.StatusCode, err
	}
	if frame.Event != "run.started" {
		response.Body.Close()
		return nil, response.StatusCode, fmt.Errorf("first SSE event is %q", frame.Event)
	}
	var started runResponse
	if err := json.Unmarshal(frame.Data, &started); err != nil {
		response.Body.Close()
		return nil, response.StatusCode, err
	}
	return &startedRun{response: response, reader: reader, started: started}, response.StatusCode, nil
}

func readSSE(reader *bufio.Reader) (sseFrame, error) {
	var frame sseFrame
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return frame, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return frame, nil
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			frame.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if len(frame.Data) > 0 {
				frame.Data = append(frame.Data, '\n')
			}
			frame.Data = append(frame.Data, strings.TrimPrefix(line, "data: ")...)
		}
	}
}

func (f *apiFixture) abort(ctx context.Context, tenantID, chatID string, id harness.ID) (runResponse, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL+"/chats/"+chatID+"/runs/"+string(id)+"/abort", nil)
	if err != nil {
		return runResponse{}, 0, err
	}
	request.Header.Set("X-Tenant-ID", tenantID)
	response, err := f.client.Do(request)
	if err != nil {
		return runResponse{}, 0, err
	}
	defer response.Body.Close()
	var result runResponse
	if response.StatusCode == http.StatusAccepted {
		err = json.NewDecoder(response.Body).Decode(&result)
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return result, response.StatusCode, err
}

func (f *apiFixture) get(ctx context.Context, tenantID, chatID string, id harness.ID) (runResponse, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+"/chats/"+chatID+"/runs/"+string(id), nil)
	if err != nil {
		return runResponse{}, 0, err
	}
	request.Header.Set("X-Tenant-ID", tenantID)
	response, err := f.client.Do(request)
	if err != nil {
		return runResponse{}, 0, err
	}
	defer response.Body.Close()
	var result runResponse
	if response.StatusCode == http.StatusOK {
		err = json.NewDecoder(response.Body).Decode(&result)
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return result, response.StatusCode, err
}

func readTerminal(started *startedRun) (runResponse, error) {
	defer started.response.Body.Close()
	frame, err := readSSE(started.reader)
	if err != nil {
		return runResponse{}, err
	}
	if frame.Event != "run.terminal" {
		return runResponse{}, fmt.Errorf("terminal SSE event is %q", frame.Event)
	}
	var terminal runResponse
	err = json.Unmarshal(frame.Data, &terminal)
	return terminal, err
}

func TestHertzRunAPILifecyclePersistenceAndValidation(t *testing.T) {
	fixture := newAPIFixture(t, 40*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, status, err := fixture.start(ctx, "", "chat", startRequest{Text: "missing-tenant"}); err != nil || status != http.StatusNotFound {
		t.Fatalf("missing tenant status=%d error=%v", status, err)
	}
	if _, status, err := fixture.start(ctx, "tenant", "chat", startRequest{}); err != nil || status != http.StatusBadRequest {
		t.Fatalf("invalid body status=%d error=%v", status, err)
	}

	control := fixture.provider.control("complete-run")
	started, status, err := fixture.start(ctx, "tenant", "chat", startRequest{RunID: "complete-1", Text: "complete-run"})
	if err != nil || status != http.StatusOK || started.started.RunID != "complete-1" || started.started.Status != harness.StatusRunning {
		t.Fatalf("start=%#v status=%d error=%v", started, status, err)
	}
	control.complete()
	terminal, err := readTerminal(started)
	if err != nil || terminal.Status != harness.StatusCompleted || terminal.Cause != harness.CauseNone {
		t.Fatalf("terminal=%#v error=%v", terminal, err)
	}
	info, status, err := fixture.get(ctx, "tenant", "chat", "complete-1")
	if err != nil || status != http.StatusOK || info.Status != harness.StatusCompleted || info.StartedAt.IsZero() || info.EndedAt.IsZero() {
		t.Fatalf("get=%#v status=%d error=%v", info, status, err)
	}
	if _, status, err = fixture.start(ctx, "tenant", "chat", startRequest{RunID: "complete-1", Text: "duplicate"}); err != nil || status != http.StatusConflict {
		t.Fatalf("duplicate status=%d error=%v", status, err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err = fixture.api.Registry.Lookup("tenant", "chat", "complete-1"); !errors.Is(err, hertzrun.ErrRunNotFound) {
		t.Fatalf("terminal handle was not evicted: %v", err)
	}
	info, status, err = fixture.get(ctx, "tenant", "chat", "complete-1")
	if err != nil || status != http.StatusOK || info.Status != harness.StatusCompleted {
		t.Fatalf("persisted get=%#v status=%d error=%v", info, status, err)
	}
}

func TestHertzRunAPIConcurrentStartsOnOneChat(t *testing.T) {
	fixture := newAPIFixture(t, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const workers = 12
	type result struct {
		index   int
		started *startedRun
		status  int
		err     error
	}
	results := make(chan result, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	startGate := make(chan struct{})
	for i := range workers {
		go func() {
			ready.Done()
			<-startGate
			text := fmt.Sprintf("same-chat-%d", i)
			fixture.provider.control(text)
			started, status, err := fixture.start(ctx, "tenant", "one-chat", startRequest{RunID: harness.ID(fmt.Sprintf("same-%d", i)), Text: text})
			results <- result{index: i, started: started, status: status, err: err}
		}()
	}
	ready.Wait()
	close(startGate)
	var winner result
	winners := 0
	conflicts := 0
	for range workers {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		switch result.status {
		case http.StatusOK:
			winner = result
			winners++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("worker %d status=%d", result.index, result.status)
		}
	}
	if winners != 1 || conflicts != workers-1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
	const aborters = 16
	var abortWG sync.WaitGroup
	abortWG.Add(aborters)
	abortResults := make(chan error, aborters)
	for range aborters {
		go func() {
			defer abortWG.Done()
			response, status, err := fixture.abort(ctx, "tenant", "one-chat", winner.started.started.RunID)
			if err != nil || status != http.StatusAccepted || (response.Status != harness.StatusCancelling && response.Status != harness.StatusAborted) {
				abortResults <- fmt.Errorf("concurrent abort=%#v status=%d error=%w", response, status, err)
			}
		}()
	}
	abortWG.Wait()
	close(abortResults)
	for err := range abortResults {
		if err != nil {
			t.Fatal(err)
		}
	}
	terminal, err := readTerminal(winner.started)
	if err != nil || terminal.Status != harness.StatusAborted || terminal.Cause != harness.CauseUserAbort {
		t.Fatalf("terminal=%#v error=%v", terminal, err)
	}
}

func TestHertzRunAPIMultipleUsersConcurrentAndIsolated(t *testing.T) {
	fixture := newAPIFixture(t, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const users = 16
	type userRun struct {
		tenant  string
		id      harness.ID
		text    string
		started *startedRun
	}
	runs := make([]userRun, users)
	var startWG sync.WaitGroup
	startWG.Add(users)
	errs := make(chan error, users)
	for i := range users {
		runs[i].tenant = fmt.Sprintf("tenant-%02d", i)
		runs[i].id = harness.ID(fmt.Sprintf("multi-%02d", i))
		runs[i].text = fmt.Sprintf("multi-user-%02d", i)
		fixture.provider.control(runs[i].text)
		go func(i int) {
			defer startWG.Done()
			started, status, err := fixture.start(ctx, runs[i].tenant, "shared-chat", startRequest{RunID: runs[i].id, Text: runs[i].text})
			if err != nil || status != http.StatusOK {
				errs <- fmt.Errorf("start user %d: status=%d error=%w", i, status, err)
				return
			}
			runs[i].started = started
		}(i)
	}
	startWG.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, status, err := fixture.abort(ctx, runs[0].tenant, "shared-chat", runs[1].id); err != nil || status != http.StatusNotFound {
		t.Fatalf("cross-tenant abort status=%d error=%v", status, err)
	}
	if _, status, err := fixture.abort(ctx, runs[0].tenant, "wrong-chat", runs[0].id); err != nil || status != http.StatusNotFound {
		t.Fatalf("cross-chat abort status=%d error=%v", status, err)
	}

	var abortWG sync.WaitGroup
	abortWG.Add(users)
	abortErrors := make(chan error, users)
	for i := range users {
		go func(i int) {
			defer abortWG.Done()
			_, status, err := fixture.abort(ctx, runs[i].tenant, "shared-chat", runs[i].id)
			if err != nil || status != http.StatusAccepted {
				abortErrors <- fmt.Errorf("abort user %d: status=%d error=%w", i, status, err)
			}
		}(i)
	}
	abortWG.Wait()
	close(abortErrors)
	for err := range abortErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range users {
		terminal, err := readTerminal(runs[i].started)
		if err != nil || terminal.RunID != runs[i].id || terminal.Status != harness.StatusAborted || terminal.Cause != harness.CauseUserAbort {
			t.Fatalf("terminal user %d=%#v error=%v", i, terminal, err)
		}
		info, status, err := fixture.get(ctx, runs[i].tenant, "shared-chat", runs[i].id)
		if err != nil || status != http.StatusOK || info.Status != harness.StatusAborted || info.Cause != harness.CauseUserAbort {
			t.Fatalf("get user %d=%#v status=%d error=%v", i, info, status, err)
		}
		registered, err := fixture.api.Registry.Lookup(runs[i].tenant, "shared-chat", runs[i].id)
		if err != nil {
			t.Fatal(err)
		}
		messages := registered.Session.Conversation().Messages
		if len(messages) != 2 || messages[1].StopReason != harness.StopAborted || !strings.Contains(messages[1].Text(), runs[i].text) {
			t.Fatalf("partial history user %d=%#v", i, messages)
		}
	}
}

func TestHertzSSEDisconnectDoesNotAbortRun(t *testing.T) {
	fixture := newAPIFixture(t, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture.provider.control("disconnect-run")
	started, status, err := fixture.start(ctx, "tenant", "chat", startRequest{RunID: "disconnect-1", Text: "disconnect-run"})
	if err != nil || status != http.StatusOK {
		t.Fatalf("start status=%d error=%v", status, err)
	}
	if err := started.response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	registered, err := fixture.api.Registry.Lookup("tenant", "chat", "disconnect-1")
	if err != nil || registered.Run.Status() != harness.StatusRunning {
		t.Fatalf("SSE disconnect changed run: status=%q error=%v", registered.Run.Status(), err)
	}
	if _, status, err = fixture.abort(ctx, "tenant", "chat", "disconnect-1"); err != nil || status != http.StatusAccepted {
		t.Fatalf("abort status=%d error=%v", status, err)
	}
	if err = registered.Run.Wait(ctx); !errors.Is(err, harness.ErrAborted) {
		t.Fatalf("wait error=%v", err)
	}
}
