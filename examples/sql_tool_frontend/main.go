// sql_tool_frontend demonstrates how one tool result can have two consumers:
// Content becomes the tool message sent to the model, while Details and update
// values are converted into application events suitable for SSE or WebSocket.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/dcalsky/best-harness-go"
	_ "modernc.org/sqlite"
)

type executeSQLParams struct {
	SQL string `json:"sql"`
}

type executeSQLDetails struct {
	SQL        string `json:"sql"`
	AffectRows int64  `json:"affect_rows"`
	DurationMS int64  `json:"duration_ms"`
	Phase      string `json:"phase,omitempty"`
	Error      string `json:"error,omitempty"`
}

// frontendEvent is an application-owned wire format. It deliberately does not
// expose agent.Event directly, so the browser protocol can evolve independently
// from the SDK's in-process event type.
type frontendEvent struct {
	Type       string             `json:"type"`
	Tool       string             `json:"tool"`
	ToolCallID string             `json:"tool_call_id"`
	Output     []harness.Content  `json:"output,omitempty"`
	Data       *executeSQLDetails `json:"data,omitempty"`
	IsError    bool               `json:"is_error,omitempty"`
	Error      string             `json:"error,omitempty"`
}

func registerExecuteSQL(h *harness.Harness[harness.NoState], db *sql.DB) error {
	return h.RegisterTool(harness.ToolSpec{
		Name:          "execute_sql",
		Description:   "Execute a SQL statement.",
		ExecutionMode: harness.Sequential,
	}, func(ctx context.Context, c harness.Context[harness.NoState], params executeSQLParams) (harness.ToolResult[executeSQLDetails], error) {
		started := time.Now()
		_ = c.Report(executeSQLDetails{SQL: params.SQL, Phase: "running"})

		result, err := db.ExecContext(ctx, params.SQL)
		if err != nil {
			details := executeSQLDetails{SQL: params.SQL, DurationMS: time.Since(started).Milliseconds(), Phase: "failed", Error: err.Error()}
			return harness.ToolResult[executeSQLDetails]{
				Content: []harness.Content{harness.Text("SQL execution failed: " + err.Error())},
				Details: details,
				IsError: true,
			}, nil
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return harness.ToolResult[executeSQLDetails]{}, fmt.Errorf("read affected rows: %w", err)
		}
		details := executeSQLDetails{SQL: params.SQL, AffectRows: affected, DurationMS: time.Since(started).Milliseconds(), Phase: "completed"}
		return harness.ToolResult[executeSQLDetails]{
			// Content is the Tool Message visible to the LLM.
			Content: []harness.Content{harness.Text(fmt.Sprintf("SQL executed successfully; affected rows: %d", affected))},
			// Details is application data visible on EventToolEnd.
			Details: details,
		}, nil
	})
}

func subscribeFrontendEvents(ctx context.Context, session *harness.Session[harness.NoState], events chan<- frontendEvent) harness.Unsubscribe {
	push := func(event frontendEvent) {
		select {
		case events <- event:
		case <-ctx.Done():
		}
	}
	return session.On(func(_ context.Context, _ harness.Context[harness.NoState], event harness.AgentEvent) {
		e := event.Event
		if e.Call == nil || e.Call.Name != "execute_sql" {
			return
		}
		switch e.Type {
		case harness.AgentEventToolUpdate:
			details, ok := e.Update.(executeSQLDetails)
			if ok {
				push(frontendEvent{Type: "tool_update", Tool: e.Call.Name, ToolCallID: e.Call.ID, Data: &details})
			}
		case harness.AgentEventToolEnd:
			out := frontendEvent{Type: "tool_result", Tool: e.Call.Name, ToolCallID: e.Call.ID}
			if e.Result != nil {
				out.Output = append([]harness.Content(nil), e.Result.Content...)
				out.IsError = e.Result.IsError
				if details, ok := e.Result.Details.(executeSQLDetails); ok {
					out.Data = &details
				}
			}
			if e.Err != nil {
				out.Error = e.Err.Error()
			}
			push(out)
		}
	})
}

func runExample(ctx context.Context, output io.Writer) error {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer db.Close()
	// A single connection keeps SQLite's in-memory database stable.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		return err
	}

	selected := harness.Model{Provider: "faux", ID: "sql-example"}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		return err
	}

	turn := 0
	fake := &harness.Faux{StreamFunc: func(_ context.Context, _ harness.Request) (harness.Stream, error) {
		turn++
		if turn == 1 {
			return &harness.SliceStream{Events: []harness.StreamEvent{
				{Type: harness.EventToolCallStart, Index: 0, ToolCallID: "sql-1", ToolName: "execute_sql", ArgumentsDelta: `{"sql":"INSERT INTO users(name) VALUES ('Ada')"}`},
				{Type: harness.EventDone, StopReason: harness.StopToolUse},
			}}, nil
		}
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventTextDelta, Text: "The row was inserted."},
			{Type: harness.EventDone, StopReason: harness.StopStop},
		}}, nil
	}}

	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		return err
	}
	if err := h.RegisterProvider("faux", fake); err != nil {
		return err
	}
	if err := registerExecuteSQL(h, db); err != nil {
		return err
	}

	session, err := h.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	if err != nil {
		return err
	}
	defer session.Close()

	// The event callback only enqueues copied data. An HTTP application can
	// replace this JSON-lines writer with one SSE or WebSocket writer goroutine.
	frontendEvents := make(chan frontendEvent, 16)
	writerDone := make(chan error, 1)
	go func() {
		encoder := json.NewEncoder(output)
		for event := range frontendEvents {
			if err := encoder.Encode(event); err != nil {
				writerDone <- err
				return
			}
		}
		writerDone <- nil
	}()

	unsubscribe := subscribeFrontendEvents(ctx, session, frontendEvents)
	run, err := session.Start(ctx, harness.Prompt{Steps: harness.Sequence{harness.UserText("Insert Ada into the users table.")}}, harness.StartOptions{})
	if err != nil {
		unsubscribe()
		close(frontendEvents)
		<-writerDone
		return err
	}
	err = run.Wait(ctx)
	unsubscribe()
	close(frontendEvents)
	if writerErr := <-writerDone; err == nil {
		err = writerErr
	}
	return err
}

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}
