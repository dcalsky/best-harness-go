// Command otel_tracing instruments a best-harness-go session with OpenTelemetry
// using only the SDK's existing extension hooks and events — no SDK changes.
//
// Span model (mirrors sand-api's graph.message / llm.stream_chat / tool.execute):
//
//	demo.request            (user-side span, proves ctx parenting works)
//	└── harness.run         root span per Session.Start, ended on terminal RunEvent
//	    ├── llm.request     one per model call, usage + ttft recorded at end
//	    └── tool.execute    one per tool call, arguments/result recorded
//
// Run it and spans print to stdout via the stdout exporter.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dcalsky/best-harness-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// maxAttrValueLen mirrors sand-api's tracing.Truncate limit.
const maxAttrValueLen = 8192

func truncate(s string) string {
	r := []rune(s)
	if len(r) <= maxAttrValueLen {
		return s
	}
	return string(r[:maxAttrValueLen])
}

// otelExt is a harness.Extension that creates spans from hooks.
type otelExt struct {
	tracer trace.Tracer
	mu     sync.Mutex
	runs   map[string]*runSpans // key: run ID
}

type runSpans struct {
	root       trace.Span
	llm        trace.Span // in-flight llm.request span, if any
	llmStart   time.Time
	ttftMarked bool
}

func newOtelExt() *otelExt {
	return &otelExt{tracer: otel.Tracer("best-harness-go/example"), runs: map[string]*runSpans{}}
}

func (e *otelExt) run(runID string) *runSpans {
	e.mu.Lock()
	defer e.mu.Unlock()
	rs := e.runs[runID]
	if rs == nil {
		rs = &runSpans{}
		e.runs[runID] = rs
	}
	return rs
}

// parent returns ctx carrying the run root span, so llm/tool spans nest under
// harness.run. The context-enricher hooks then propagate those child spans to
// the provider and tool body.
func (e *otelExt) parent(ctx context.Context, runID string) context.Context {
	if root := e.run(runID).root; root != nil {
		return trace.ContextWithSpan(ctx, root)
	}
	return ctx
}

func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (e *otelExt) Register(r *harness.ExtensionRegistry[harness.NoState]) error {
	// Run root span starts when a run is about to begin. The hook ctx derives
	// from the user's Session.Start ctx, so a caller-side span parents the run.
	r.AddBeforeAgentHook(func(ctx context.Context, c harness.Context[harness.NoState]) error {
		_, span := e.tracer.Start(ctx, "harness.run", trace.WithAttributes(
			attribute.String("harness.session.id", c.SessionID()),
			attribute.String("harness.run.id", string(c.RunID())),
		))
		e.run(string(c.RunID())).root = span
		return nil
	})

	// llm.request opens right before the provider stream starts; it sees the
	// final request (after Context hooks).
	r.AddRequestContextHook(func(ctx context.Context, c harness.Context[harness.NoState], req *harness.Request) (context.Context, error) {
		rs := e.run(string(c.RunID()))
		spanCtx, span := e.tracer.Start(e.parent(ctx, string(c.RunID())), "llm.request", trace.WithAttributes(
			attribute.String("gen_ai.system", req.Model.Provider),
			attribute.String("gen_ai.request.model", req.Model.ID),
			attribute.Int("llm.messages_count", len(req.Messages)),
			attribute.Int64("gen_ai.request.max_tokens", req.MaxTokens),
		))
		rs.llm = span
		rs.llmStart = time.Now()
		rs.ttftMarked = false
		return spanCtx, nil
	})

	// Fires on success AND failure: error messages carry StopReason/ErrorMessage.
	r.AddResponseHook(func(ctx context.Context, c harness.Context[harness.NoState], m harness.Message) error {
		rs := e.run(string(c.RunID()))
		span := rs.llm
		if span == nil {
			return nil
		}
		rs.llm = nil
		span.SetAttributes(
			attribute.Float64("llm.op_ms", float64(time.Since(rs.llmStart).Milliseconds())),
			attribute.Int64("gen_ai.usage.input_tokens", m.Usage.InputTokens),
			attribute.Int64("gen_ai.usage.output_tokens", m.Usage.OutputTokens),
			attribute.String("llm.stop_reason", string(m.StopReason)),
		)
		// Prompt-cache metrics. Denominator differs by API family: OpenAI's
		// input tokens include the cached portion, Anthropic's does not.
		if cached := m.Usage.CacheReadTokens; cached > 0 {
			denom := m.Usage.InputTokens
			if m.API == harness.APIAnthropic {
				denom += cached
			}
			span.SetAttributes(
				attribute.Int64("llm.cached_tokens", cached),
				attribute.Float64("llm.cache_hit_ratio", float64(cached)/float64(denom)),
			)
			if m.Usage.CacheWriteTokens > 0 {
				span.SetAttributes(attribute.Int64("llm.cache_creation_tokens", m.Usage.CacheWriteTokens))
			}
		}
		var err error
		if m.ErrorMessage != "" {
			err = fmt.Errorf("%s", m.ErrorMessage)
		}
		endSpan(span, err)
		return nil
	})

	r.AddToolContextHook(func(ctx context.Context, c harness.Context[harness.NoState], call harness.ToolCall) (context.Context, error) {
		spanCtx, _ := e.tracer.Start(e.parent(ctx, string(c.RunID())), "tool.execute", trace.WithAttributes(
			attribute.String("tool.name", call.Name),
			attribute.String("tool.arguments", truncate(string(call.Arguments))),
		))
		return spanCtx, nil
	})

	r.AddAfterToolCallHook(func(ctx context.Context, _ harness.Context[harness.NoState], call harness.ToolCall, res harness.Result) (harness.Result, error) {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.Bool("tool.is_error", res.IsError))
		var err error
		if res.IsError {
			err = fmt.Errorf("tool %s failed", call.Name)
		}
		endSpan(span, err)
		return res, nil
	})
	return nil
}

// endRun closes the root span on the terminal RunEvent. There is no run-end
// hook, so subscribe per session.
func (e *otelExt) endRun(ev harness.RunEvent) {
	if ev.Status == harness.StatusRunning || ev.Status == harness.StatusCancelling {
		return
	}
	runID := string(ev.RunID)
	rs := e.run(runID)
	if rs.root == nil {
		return
	}
	rs.root.SetAttributes(attribute.String("harness.run.status", string(ev.Status)))
	endSpan(rs.root, ev.Err)
	e.mu.Lock()
	delete(e.runs, runID)
	e.mu.Unlock()
}

// markTTFT records time-to-first-token on the in-flight llm span: the first
// message_update event of a model turn is the first streamed token.
func (e *otelExt) markTTFT(runID string) {
	rs := e.run(runID)
	if rs.llm == nil || rs.ttftMarked {
		return
	}
	rs.ttftMarked = true
	rs.llm.SetAttributes(attribute.Float64("llm.ttft_ms", float64(time.Since(rs.llmStart).Milliseconds())))
}

type countParams struct {
	Text string `json:"text"`
}
type countDetails struct{}

func main() {
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		log.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	selected := harness.Model{Provider: "faux", ID: "demo", ContextWindow: 128_000}
	models := harness.NewModelRegistry()
	_ = models.Register(selected)

	turn := 0
	fake := &harness.Faux{StreamFunc: func(_ context.Context, _ harness.Request) (harness.Stream, error) {
		turn++
		if turn == 1 {
			return &harness.SliceStream{Events: []harness.StreamEvent{
				{Type: harness.EventToolCallStart, Index: 0, ToolCallID: "call-1", ToolName: "count", ArgumentsDelta: `{"text":"hello world"}`},
				{Type: harness.EventDone, StopReason: harness.StopToolUse, Usage: harness.Usage{InputTokens: 42, OutputTokens: 8, CacheReadTokens: 30, TotalTokens: 50}},
			}}, nil
		}
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventTextDelta, Text: "That text has 11 runes."},
			{Type: harness.EventDone, StopReason: harness.StopStop, Usage: harness.Usage{InputTokens: 60, OutputTokens: 7, TotalTokens: 67}},
		}}, nil
	}}

	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		log.Fatal(err)
	}
	if err := h.RegisterProvider("faux", fake); err != nil {
		log.Fatal(err)
	}
	if err := h.RegisterTool(harness.ToolSpec{Name: "count", Description: "Count runes in text."}, func(_ context.Context, _ harness.Context[harness.NoState], p countParams) (harness.ToolResult[countDetails], error) {
		return harness.ToolResult[countDetails]{Content: []harness.Content{harness.Text(fmt.Sprint(len([]rune(p.Text))))}}, nil
	}); err != nil {
		log.Fatal(err)
	}

	ext := newOtelExt()
	if err := h.RegisterExtension(ext); err != nil {
		log.Fatal(err)
	}

	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	s.On[harness.RunEvent](func(_ context.Context, _ harness.Context[harness.NoState], ev harness.RunEvent) { ext.endRun(ev) })
	s.On[harness.AgentEvent](func(_ context.Context, _ harness.Context[harness.NoState], ev harness.AgentEvent) {
		if ev.Event.Type == harness.AgentEventMessageUpdate {
			ext.markTTFT(string(ev.RunID))
		}
	})

	// A caller-side span: harness.run will appear as its child in the trace.
	ctx, span := otel.Tracer("demo").Start(context.Background(), "demo.request")
	defer span.End()

	r, err := s.Start(ctx, harness.Prompt{Steps: harness.Sequence{harness.UserText("How many runes in 'hello world'?")}}, harness.StartOptions{})
	if err != nil {
		log.Fatal(err)
	}
	if err := r.Wait(ctx); err != nil {
		log.Fatal(err)
	}
}
