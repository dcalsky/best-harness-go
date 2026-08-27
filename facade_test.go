package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go"
)

type facadeParams struct {
	Text string `json:"text"`
}

type facadeStructValidator struct {
	validate func(any) error
}

func (v *facadeStructValidator) Struct(value any) error {
	return v.validate(value)
}

func TestRegisterToolWithArgumentsValidator(t *testing.T) {
	tools := harness.NewToolRegistry()
	h, err := harness.NewStateless(harness.Options{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	errRejected := errors.New("text must not be empty")
	handlerCalled := false
	err = h.RegisterTool(
		harness.ToolSpec{Name: "validated_echo"},
		func(_ context.Context, _ harness.Context[harness.NoState], p facadeParams) (harness.ToolResult[facadeDetails], error) {
			handlerCalled = true
			return harness.ToolResult[facadeDetails]{Details: facadeDetails{Length: len(p.Text)}}, nil
		},
		harness.WithArgumentsValidator(func(p facadeParams) error {
			if strings.TrimSpace(p.Text) == "" {
				return errRejected
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.Validate(harness.ToolCall{Name: "validated_echo", Arguments: json.RawMessage(`{"text":"ok"}`)}); err != nil {
		t.Fatal(err)
	}
	_, err = tools.Execute(context.Background(), harness.ToolCall{Name: "validated_echo", Arguments: json.RawMessage(`{"text":" "}`)}, nil)
	if !errors.Is(err, errRejected) || handlerCalled {
		t.Fatalf("error=%v handler called=%v", err, handlerCalled)
	}
}

func TestRegisterToolCombinesStructAndArgumentsValidators(t *testing.T) {
	tools := harness.NewToolRegistry()
	h, err := harness.NewStateless(harness.Options{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	errStructRejected := errors.New("struct validation rejected text")
	errRejected := errors.New("text is rejected")
	var order []string
	handlerCalled := false
	structValidator := &facadeStructValidator{validate: func(value any) error {
		params, ok := value.(facadeParams)
		if !ok {
			return errors.New("unexpected struct validator value")
		}
		order = append(order, "struct:"+params.Text)
		if params.Text == "struct-reject" {
			return errStructRejected
		}
		return nil
	}}
	err = h.RegisterTool(
		harness.ToolSpec{Name: "composed_validation"},
		func(_ context.Context, _ harness.Context[harness.NoState], p facadeParams) (harness.ToolResult[facadeDetails], error) {
			handlerCalled = true
			order = append(order, "handler:"+p.Text)
			return harness.ToolResult[facadeDetails]{}, nil
		},
		harness.WithStructValidator[facadeParams](structValidator, harness.WithValidatorRetryLimit(2)),
		harness.WithArgumentsValidator(func(p facadeParams) error {
			order = append(order, "arguments:"+p.Text)
			if p.Text == "reject" {
				return errRejected
			}
			return nil
		}, harness.WithValidatorRetryLimit(1)),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tools.Execute(context.Background(), harness.ToolCall{Name: "composed_validation", Arguments: json.RawMessage(`{"text":"struct-reject"}`)}, nil)
	if !errors.Is(err, errStructRejected) || handlerCalled {
		t.Fatalf("error=%v handler called=%v", err, handlerCalled)
	}
	if got := strings.Join(order, ","); got != "struct:struct-reject" {
		t.Fatalf("fail-fast order=%q", got)
	}

	order = nil
	_, err = tools.Execute(context.Background(), harness.ToolCall{Name: "composed_validation", Arguments: json.RawMessage(`{"text":"reject"}`)}, nil)
	if !errors.Is(err, errRejected) || handlerCalled {
		t.Fatalf("error=%v handler called=%v", err, handlerCalled)
	}
	if got := strings.Join(order, ","); got != "struct:reject,arguments:reject" {
		t.Fatalf("validator order=%q", got)
	}

	order = nil
	err = tools.Validate(harness.ToolCall{Name: "composed_validation", Arguments: json.RawMessage(`{"text":"ok"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "struct:ok,arguments:ok" {
		t.Fatalf("validator order=%q", got)
	}
	if handlerCalled {
		t.Fatal("validation called the handler")
	}
}

func TestRegisterToolRejectsNilValidatorOptions(t *testing.T) {
	h, err := harness.NewStateless(harness.Options{})
	if err != nil {
		t.Fatal(err)
	}
	handler := func(context.Context, harness.Context[harness.NoState], facadeParams) (harness.ToolResult[facadeDetails], error) {
		return harness.ToolResult[facadeDetails]{}, nil
	}

	err = h.RegisterTool(harness.ToolSpec{Name: "nil_validator"}, handler, harness.WithArgumentsValidator[facadeParams](nil))
	if err == nil || !strings.Contains(err.Error(), "validator is required") {
		t.Fatalf("nil validator error=%v", err)
	}
	err = h.RegisterTool(harness.ToolSpec{Name: "nil_struct_validator"}, handler, harness.WithStructValidator[facadeParams](nil))
	if err == nil || !strings.Contains(err.Error(), "struct validator is required") {
		t.Fatalf("nil struct validator error=%v", err)
	}
	var typedNil *facadeStructValidator
	err = h.RegisterTool(
		harness.ToolSpec{Name: "typed_nil_struct_validator"},
		handler,
		harness.WithStructValidator[facadeParams](typedNil),
	)
	if err == nil || !strings.Contains(err.Error(), "struct validator is required") {
		t.Fatalf("typed nil struct validator error=%v", err)
	}
	err = h.RegisterTool(
		harness.ToolSpec{Name: "negative_validator_retry_limit"},
		handler,
		harness.WithArgumentsValidator(
			func(facadeParams) error { return nil },
			harness.WithValidatorRetryLimit(-1),
		),
	)
	if err != nil {
		t.Fatalf("negative validator retry limit error=%v", err)
	}
	err = h.RegisterTool(
		harness.ToolSpec{Name: "duplicate_validator_retry_limit"},
		handler,
		harness.WithArgumentsValidator(
			func(facadeParams) error { return nil },
			harness.WithValidatorRetryLimit(1),
			harness.WithValidatorRetryLimit(2),
		),
	)
	if err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("duplicate validator retry limit error=%v", err)
	}
}

func TestValidatorRetryLimitPublicAPIStopsAgentRun(t *testing.T) {
	tools := harness.NewToolRegistry()
	h, err := harness.NewStateless(harness.Options{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	errRejected := errors.New("text is invalid")
	handlerCalled := false
	err = h.RegisterTool(
		harness.ToolSpec{Name: "retry_limited"},
		func(context.Context, harness.Context[harness.NoState], facadeParams) (harness.ToolResult[facadeDetails], error) {
			handlerCalled = true
			return harness.ToolResult[facadeDetails]{}, nil
		},
		harness.WithArgumentsValidator(func(params facadeParams) error {
			if params.Text == "bad" {
				return errRejected
			}
			return nil
		}, harness.WithValidatorRetryLimit(0)),
	)
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	provider := &harness.Faux{StreamFunc: func(context.Context, harness.Request) (harness.Stream, error) {
		providerCalls++
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventToolCallStart, ToolCallID: "call-1", ToolName: "retry_limited", ArgumentsDelta: `{"text":"bad"}`},
			{Type: harness.EventDone, StopReason: harness.StopToolUse},
		}}, nil
	}}
	agent := harness.NewAgent(harness.AgentOptions{Provider: provider, Model: harness.Model{ID: "m"}, Tools: tools})
	run, err := agent.Start(
		context.Background(),
		harness.AgentPrompt{Steps: harness.Sequence{harness.UserText("validate")}},
		harness.AgentStartOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = run.Wait(context.Background())
	var limitErr *harness.ValidatorRetryLimitError
	if !errors.As(err, &limitErr) || !errors.Is(err, errRejected) {
		t.Fatalf("run error=%v", err)
	}
	if limitErr.Tool != "retry_limited" || limitErr.RetryLimit != 0 || limitErr.Failures != 1 {
		t.Fatalf("limit error=%#v", limitErr)
	}
	if providerCalls != 1 || handlerCalled {
		t.Fatalf("provider calls=%d handler called=%v", providerCalls, handlerCalled)
	}
}

func TestValidatorRetryLimitAllowsUnlimitedRetries(t *testing.T) {
	tests := []struct {
		name    string
		options []harness.ValidatorOption
	}{
		{name: "negative", options: []harness.ValidatorOption{harness.WithValidatorRetryLimit(-1)}},
		{name: "omitted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := harness.NewToolRegistry()
			h, err := harness.NewStateless(harness.Options{Tools: tools})
			if err != nil {
				t.Fatal(err)
			}
			handlerCalled := false
			err = h.RegisterTool(
				harness.ToolSpec{Name: "unlimited_validation_retries"},
				func(context.Context, harness.Context[harness.NoState], facadeParams) (harness.ToolResult[facadeDetails], error) {
					handlerCalled = true
					return harness.ToolResult[facadeDetails]{}, nil
				},
				harness.WithArgumentsValidator(func(facadeParams) error {
					return errors.New("always invalid")
				}, tc.options...),
			)
			if err != nil {
				t.Fatal(err)
			}
			providerCalls := 0
			provider := &harness.Faux{StreamFunc: func(context.Context, harness.Request) (harness.Stream, error) {
				providerCalls++
				if providerCalls <= 2 {
					return &harness.SliceStream{Events: []harness.StreamEvent{
						{Type: harness.EventToolCallStart, ToolCallID: "call", ToolName: "unlimited_validation_retries", ArgumentsDelta: `{"text":"bad"}`},
						{Type: harness.EventDone, StopReason: harness.StopToolUse},
					}}, nil
				}
				return &harness.SliceStream{Events: []harness.StreamEvent{
					{Type: harness.EventTextDelta, Text: "done"},
					{Type: harness.EventDone, StopReason: harness.StopStop},
				}}, nil
			}}
			agent := harness.NewAgent(harness.AgentOptions{Provider: provider, Model: harness.Model{ID: "m"}, Tools: tools})
			run, err := agent.Start(
				context.Background(),
				harness.AgentPrompt{Steps: harness.Sequence{harness.UserText("validate")}},
				harness.AgentStartOptions{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if providerCalls != 3 || handlerCalled {
				t.Fatalf("provider calls=%d handler called=%v", providerCalls, handlerCalled)
			}
		})
	}
}

type facadeDetails struct {
	Length int
}

func TestFacadeSupportsCoreSDKWithOneImport(t *testing.T) {
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "model"}
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	if got, err := models.Get("test", "model"); err != nil || got != selected {
		t.Fatalf("model=%#v err=%v", got, err)
	}

	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.Tool[facadeParams, facadeDetails]{
		Name: "echo",
		Execute: func(_ context.Context, _ harness.ToolCall, p facadeParams, update harness.Update[facadeDetails]) (harness.ToolResult[facadeDetails], error) {
			details := facadeDetails{Length: len(p.Text)}
			update(details)
			return harness.ToolResult[facadeDetails]{Content: []harness.Content{harness.Text(p.Text)}, Details: details}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	var updates []facadeDetails
	result, err := tools.Execute(context.Background(), harness.ToolCall{
		Name:      "echo",
		Arguments: json.RawMessage(`{"text":"hello"}`),
	}, func(value any) {
		updates = append(updates, value.(facadeDetails))
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content[0].Text != "hello" || result.Details != (facadeDetails{Length: 5}) || len(updates) != 1 {
		t.Fatalf("result=%#v updates=%#v", result, updates)
	}

	steps := harness.Sequence{
		harness.UserText("inspect"),
		harness.Tools(harness.PromptToolCall{Name: "echo", Arguments: json.RawMessage(`{"text":"hello"}`)}),
	}
	if normalized, err := steps.Normalize(); err != nil || len(normalized) != 2 {
		t.Fatalf("steps=%#v err=%v", normalized, err)
	}

	var _ harness.Provider = &harness.Faux{}
	if harness.ErrToolNotFound == nil {
		t.Fatal("tool sentinel error is missing")
	}
	if !harness.Terminal(harness.StatusCompleted) {
		t.Fatal("completed run must be terminal")
	}
}
