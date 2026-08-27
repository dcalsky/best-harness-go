// run_registry starts a multi-chat Hertz service with process-local Run
// handles. Replace the X-Tenant-ID header with the application's real
// authentication middleware and persist run-to-instance routing when running
// more than one server instance.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/dcalsky/best-harness-go"
	"github.com/dcalsky/best-harness-go/examples/run_registry/hertzrun"
	openaisdk "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
)

type sessionStore struct {
	mu       sync.Mutex
	harness  *harness.Harness[harness.NoState]
	model    harness.Model
	sessions map[string]*harness.Session[harness.NoState]
}

func newSessionStore(h *harness.Harness[harness.NoState], selected harness.Model) *sessionStore {
	return &sessionStore{harness: h, model: selected, sessions: make(map[string]*harness.Session[harness.NoState])}
}

func (s *sessionStore) Resolve(ctx context.Context, tenantID, chatID string) (*harness.Session[harness.NoState], error) {
	if tenantID == "" || chatID == "" {
		return nil, errors.New("tenant and chat are required")
	}
	key := tenantID + "\x00" + chatID
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.sessions[key]; existing != nil {
		return existing, nil
	}
	session, err := s.harness.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{Model: &s.model}, harness.NoState{})
	if err != nil {
		return nil, err
	}
	s.sessions[key] = session
	return session, nil
}

func (s *sessionStore) Close() {
	s.mu.Lock()
	sessions := make([]*harness.Session[harness.NoState], 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	for _, session := range sessions {
		if err := session.Close(); err != nil {
			log.Printf("close session: %v", err)
		}
	}
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func main() {
	selected := harness.Model{
		Provider:      "openai",
		API:           harness.APIOpenAI,
		ID:            requiredEnv("MODEL_ID"),
		ContextWindow: 128_000,
		MaxOutput:     8_000,
	}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		log.Fatal(err)
	}
	agentHarness, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		log.Fatal(err)
	}
	client := openaisdk.NewClient(
		openaioption.WithAPIKey(requiredEnv("MODEL_API_KEY")),
		openaioption.WithBaseURL(requiredEnv("MODEL_BASE_URL")),
	)
	if err := agentHarness.RegisterProvider("openai", harness.NewOpenAIProvider(client)); err != nil {
		log.Fatal(err)
	}

	runCtx, cancelRuns := context.WithCancel(context.Background())
	sessions := newSessionStore(agentHarness, selected)
	api := &hertzrun.Server[harness.NoState]{
		RunContext: runCtx,
		Registry:   hertzrun.NewRunRegistry[harness.NoState](),
		Session:    sessions.Resolve,
		RunTTL:     10 * time.Minute,
	}
	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	h := server.Default(
		server.WithHostPorts(address),
		server.WithSenseClientDisconnection(true),
	)
	api.RegisterRoutes(h)
	h.OnShutdown = append(h.OnShutdown, func(context.Context) {
		cancelRuns()
		sessions.Close()
	})
	defer func() {
		cancelRuns()
		sessions.Close()
	}()
	h.Spin()
}
