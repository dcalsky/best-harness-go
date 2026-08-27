package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/jsonschema"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

type params struct {
	Value string `json:"value"`
}
type details struct {
	Length int `json:"length"`
}

func TestTypedToolAllowsUnknownArguments(t *testing.T) {
	r := tool.NewRegistry()
	var update details
	err := r.Register(tool.Tool[params, details]{Name: "length", Description: "Count bytes.", Execute: func(_ context.Context, _ tool.ToolCall, p params, u tool.Update[details]) (tool.ToolResult[details], error) {
		update = details{Length: len(p.Value)}
		u(update)
		return tool.ToolResult[details]{Content: []message.Content{message.Text(p.Value)}, Details: update}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Execute(context.Background(), tool.ToolCall{ID: "1", Name: "length", Arguments: json.RawMessage(`{"value":"abc"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Details.(details).Length != 3 || update.Length != 3 {
		t.Fatalf("details=%#v update=%#v", res.Details, update)
	}
	res, err = r.Execute(context.Background(), tool.ToolCall{Name: "length", Arguments: json.RawMessage(`{"value":"abc","extra":true}`)}, nil)
	if err != nil || res.Details.(details).Length != 3 {
		t.Fatalf("unknown argument should be ignored: result=%#v error=%v", res, err)
	}
}

func TestValidateArgumentsRejectsDecodedArguments(t *testing.T) {
	r := tool.NewRegistry()
	errRejected := errors.New("value must not be empty")
	validatorCalls := 0
	executed := false
	err := r.Register(tool.Tool[params, details]{
		Name: "length",
		ValidateArguments: func(p params) error {
			validatorCalls++
			if p.Value == "" {
				return errRejected
			}
			return nil
		},
		Execute: func(_ context.Context, _ tool.ToolCall, p params, _ tool.Update[details]) (tool.ToolResult[details], error) {
			executed = true
			return tool.ToolResult[details]{Details: details{Length: len(p.Value)}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	call := tool.ToolCall{Name: "length", Arguments: json.RawMessage(`{"value":""}`)}
	if err := r.Validate(call); !errors.Is(err, errRejected) || !strings.Contains(err.Error(), "invalid length arguments") {
		t.Fatalf("validation error=%v", err)
	}
	if _, err := r.Execute(context.Background(), call, nil); !errors.Is(err, errRejected) {
		t.Fatalf("execution validation error=%v", err)
	}
	if executed || validatorCalls != 2 {
		t.Fatalf("executed=%v validator calls=%d", executed, validatorCalls)
	}

	result, err := r.Execute(context.Background(), tool.ToolCall{Name: "length", Arguments: json.RawMessage(`{"value":"abc"}`)}, nil)
	if err != nil || !executed || result.Details.(details).Length != 3 {
		t.Fatalf("result=%#v executed=%v error=%v", result, executed, err)
	}
}

func TestValidateArgumentsRunsAgainAfterBeforeHookChangesArguments(t *testing.T) {
	r := tool.NewRegistry()
	validatorCalls := 0
	executed := false
	err := r.Register(tool.Tool[params, struct{}]{
		Name: "change",
		ValidateArguments: func(p params) error {
			validatorCalls++
			if p.Value == "changed" {
				return errors.New("changed value is invalid")
			}
			return nil
		},
		Execute: func(context.Context, tool.ToolCall, params, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
			executed = true
			return tool.ToolResult[struct{}]{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.AddBeforeHook(func(_ context.Context, call tool.ToolCall) (tool.ToolCall, error) {
		call.Arguments = json.RawMessage(`{"value":"changed"}`)
		return call, nil
	})

	_, err = r.Execute(context.Background(), tool.ToolCall{Name: "change", Arguments: json.RawMessage(`{"value":"original"}`)}, nil)
	if err == nil || !strings.Contains(err.Error(), "changed value is invalid") {
		t.Fatalf("error=%v", err)
	}
	if executed || validatorCalls != 2 {
		t.Fatalf("executed=%v validator calls=%d", executed, validatorCalls)
	}
}

func TestDefinitionsPreserveRegistrationAndSelectionOrder(t *testing.T) {
	r := tool.NewRegistry()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		if err := r.Register(tool.Tool[struct{}, struct{}]{Name: name, Execute: func(context.Context, tool.ToolCall, struct{}, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
			return tool.ToolResult[struct{}]{}, nil
		}}); err != nil {
			t.Fatal(err)
		}
	}
	definitions := r.Definitions(nil)
	if len(definitions) != 3 {
		t.Fatalf("definitions=%#v", definitions)
	}
	if got := definitions[0].Name + "," + definitions[1].Name + "," + definitions[2].Name; got != "zeta,alpha,middle" {
		t.Fatalf("registration order=%s", got)
	}
	definitions = r.Definitions([]string{"middle", "missing", "zeta"})
	if len(definitions) != 2 || definitions[0].Name != "middle" || definitions[1].Name != "zeta" {
		t.Fatalf("selection order=%#v", definitions)
	}
}
func TestSchemaOf(t *testing.T) {
	schema, err := tool.SchemaOf[params]()
	if err != nil {
		t.Fatal(err)
	}
	if schema.Type != jsonschema.Object || len(schema.PropertyOrder) != 1 || schema.PropertyOrder[0] != "value" {
		t.Fatalf("schema=%#v", schema)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"additionalProperties":false`) || !strings.Contains(text, `"required":["value"]`) {
		t.Fatal(text)
	}
}

func TestSchemaOfPreservesStructFieldOrderAndOptionalFields(t *testing.T) {
	type nested struct {
		Rows   []string       `json:"rows"`
		Labels map[string]int `json:"labels,omitempty"`
	}
	type orderedParams struct {
		Task    string  `json:"task"`
		Nested  nested  `json:"nested"`
		Comment *string `json:"comment,omitempty"`
	}
	schema, err := tool.SchemaOf[orderedParams]()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(schema.PropertyOrder, ","); got != "task,nested,comment" {
		t.Fatalf("property order=%s", got)
	}
	if got := strings.Join(schema.Required, ","); got != "task,nested" {
		t.Fatalf("required=%s", got)
	}
	nestedSchema := schema.Properties["nested"]
	if got := strings.Join(nestedSchema.PropertyOrder, ","); got != "rows,labels" {
		t.Fatalf("nested property order=%s", got)
	}
	labels := nestedSchema.Properties["labels"]
	additional, ok := labels.AdditionalProperties.(jsonschema.Definition)
	if !ok || additional.Type != jsonschema.Integer {
		t.Fatalf("map schema=%#v", labels)
	}
}

func TestRegisterStructuredAndRawParameters(t *testing.T) {
	execute := func(context.Context, tool.ToolCall, params, tool.Update[details]) (tool.ToolResult[details], error) {
		return tool.ToolResult[details]{}, nil
	}
	t.Run("structured", func(t *testing.T) {
		r := tool.NewRegistry()
		err := r.Register(tool.Tool[params, details]{
			Name: "length",
			Parameters: jsonschema.Definition{
				Type:                 jsonschema.Object,
				PropertyOrder:        []string{"value"},
				Properties:           map[string]jsonschema.Definition{"value": {Type: jsonschema.String}},
				Required:             []string{"value"},
				AdditionalProperties: false,
			},
			Execute: execute,
		})
		if err != nil {
			t.Fatal(err)
		}
		definitions := r.Definitions(nil)
		if len(definitions) != 1 || string(definitions[0].Parameters) != `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}` {
			t.Fatalf("definitions=%#v", definitions)
		}
	})

	t.Run("raw", func(t *testing.T) {
		r := tool.NewRegistry()
		raw := json.RawMessage(`{"type":"object","x-custom":true}`)
		if err := r.Register(tool.Tool[params, details]{Name: "length", RawParameters: raw, Execute: execute}); err != nil {
			t.Fatal(err)
		}
		raw[0] = '['
		if got := string(r.Definitions(nil)[0].Parameters); got != `{"type":"object","x-custom":true}` {
			t.Fatalf("parameters=%s", got)
		}
	})

	t.Run("both", func(t *testing.T) {
		r := tool.NewRegistry()
		err := r.Register(tool.Tool[params, details]{
			Name:          "length",
			Parameters:    jsonschema.Definition{Type: jsonschema.Object},
			RawParameters: json.RawMessage(`{}`),
			Execute:       execute,
		})
		if err == nil || !strings.Contains(err.Error(), "cannot both be set") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("invalid raw", func(t *testing.T) {
		r := tool.NewRegistry()
		err := r.Register(tool.Tool[params, details]{Name: "length", RawParameters: json.RawMessage(`{`), Execute: execute})
		if err == nil || !strings.Contains(err.Error(), "valid JSON") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestSchemaIsNotUsedForRuntimeValidation(t *testing.T) {
	r := tool.NewRegistry()
	called := false
	err := r.Register(tool.Tool[params, details]{
		Name: "length",
		Parameters: jsonschema.Definition{
			Type:       jsonschema.Object,
			Properties: map[string]jsonschema.Definition{"value": {Type: jsonschema.String}},
			Required:   []string{"value"},
		},
		Execute: func(_ context.Context, _ tool.ToolCall, p params, _ tool.Update[details]) (tool.ToolResult[details], error) {
			called = true
			return tool.ToolResult[details]{Details: details{Length: len(p.Value)}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Execute(context.Background(), tool.ToolCall{Name: "length", Arguments: json.RawMessage(`{}`)}, nil)
	if err != nil || !called || result.Details.(details).Length != 0 {
		t.Fatalf("result=%#v called=%v error=%v", result, called, err)
	}
}

func TestPrepareArgumentsStillOverridesStrictDecoding(t *testing.T) {
	r := tool.NewRegistry()
	validated := false
	err := r.Register(tool.Tool[params, details]{
		Name: "length",
		PrepareArguments: func(raw json.RawMessage) (params, error) {
			if string(raw) != `{"custom":true}` {
				t.Fatalf("raw=%s", raw)
			}
			return params{Value: "prepared"}, nil
		},
		ValidateArguments: func(p params) error {
			validated = p.Value == "prepared"
			return nil
		},
		Execute: func(_ context.Context, _ tool.ToolCall, p params, _ tool.Update[details]) (tool.ToolResult[details], error) {
			return tool.ToolResult[details]{Details: details{Length: len(p.Value)}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Execute(context.Background(), tool.ToolCall{Name: "length", Arguments: json.RawMessage(`{"custom":true}`)}, nil)
	if err != nil || !validated || result.Details.(details).Length != len("prepared") {
		t.Fatalf("result=%#v validated=%v error=%v", result, validated, err)
	}
}
