// Package tool provides typed tool definitions and a non-generic runtime adapter.
package tool

import (
	"context"
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"reflect"
	"sync"

	"github.com/dcalsky/best-harness-go/internal/invocation"
	"github.com/dcalsky/best-harness-go/internal/jsonschema"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/provider"
)

type ExecutionMode string

const (
	Sequential ExecutionMode = "sequential"
	Parallel   ExecutionMode = "parallel"
)

type ToolCall = invocation.ToolCall
type Spec struct {
	Name, Description string
	Parameters        jsonschema.Definition
	RawParameters     json.RawMessage
	ExecutionMode     ExecutionMode
}

// ArgumentsValidator validates decoded tool arguments before execution. It may
// be called more than once for a tool call and must not mutate its arguments or
// have side effects. It must also be safe for concurrent use.
type ArgumentsValidator[P any] func(P) error

// StructValidator validates decoded struct values. Implementations are
// typically tag-based validators and must be safe for concurrent use.
type StructValidator interface {
	Struct(any) error
}

// ValidatorFailure identifies the configured validator that rejected decoded
// arguments. It is used by the agent to apply per-validator retry limits.
type ValidatorFailure struct {
	ValidatorIndex int
	RetryLimit     int
	HasRetryLimit  bool
	Err            error
}

func (e *ValidatorFailure) Error() string { return e.Err.Error() }
func (e *ValidatorFailure) Unwrap() error { return e.Err }

type Update[D any] func(D)
type ToolResult[D any] struct {
	Content   []message.Content
	Details   D
	IsError   bool
	Terminate bool
}
type Tool[P, D any] struct {
	Name, Description string
	Parameters        jsonschema.Definition
	RawParameters     json.RawMessage
	ExecutionMode     ExecutionMode
	PrepareArguments  func(json.RawMessage) (P, error)
	ValidateArguments ArgumentsValidator[P]
	Execute           func(context.Context, ToolCall, P, Update[D]) (ToolResult[D], error)
}
type Result struct {
	Content   []message.Content
	Details   any
	IsError   bool
	Terminate bool
}
type BeforeHook func(context.Context, ToolCall) (ToolCall, error)
type AfterHook func(context.Context, ToolCall, Result) (Result, error)

type BlockedError struct {
	Reason    string
	Terminate bool
}

func (e *BlockedError) Error() string {
	if e.Reason == "" {
		return "tool execution was blocked"
	}
	return e.Reason
}

func Block(reason string, terminate bool) error {
	return &BlockedError{Reason: reason, Terminate: terminate}
}

var ErrNotFound = errors.New("tool not found")

type runtimeTool struct {
	info    provider.Tool
	mode    ExecutionMode
	prepare func(json.RawMessage) (func(context.Context, ToolCall, func(any)) (Result, error), error)
}
type Prepared struct {
	call  ToolCall
	run   func(context.Context, ToolCall, func(any)) (Result, error)
	after []AfterHook
}
type Registry struct {
	mu     sync.RWMutex
	tools  map[string]runtimeTool
	order  []string
	before []BeforeHook
	after  []AfterHook
}

func NewRegistry() *Registry { return &Registry{tools: make(map[string]runtimeTool)} }

func (r *Registry) Register[P, D any](def Tool[P, D]) error {
	if def.Name == "" || def.Execute == nil {
		return errors.New("tool name and execute function are required")
	}
	if !def.Parameters.IsZero() && len(def.RawParameters) > 0 {
		return errors.New("tool parameters and raw parameters cannot both be set")
	}
	var parameters json.RawMessage
	if len(def.RawParameters) > 0 {
		if !def.RawParameters.IsValid() {
			return errors.New("tool raw parameters must be valid JSON")
		}
		parameters = def.RawParameters.Clone()
	} else {
		parametersDefinition := def.Parameters
		if parametersDefinition.IsZero() {
			schema, err := SchemaOf[P]()
			if err != nil {
				return err
			}
			parametersDefinition = schema
		}
		schema, err := json.Marshal(parametersDefinition)
		if err != nil {
			return fmt.Errorf("marshal %s parameters: %w", def.Name, err)
		}
		parameters = schema
	}
	prepare := def.PrepareArguments
	if prepare == nil {
		prepare = func(raw json.RawMessage) (P, error) {
			var p P
			err := json.Unmarshal(raw, &p)
			return p, err
		}
	}
	rt := runtimeTool{info: provider.Tool{Name: def.Name, Description: def.Description, Parameters: parameters.Clone()}, mode: def.ExecutionMode, prepare: func(raw json.RawMessage) (func(context.Context, ToolCall, func(any)) (Result, error), error) {
		p, err := prepare(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s arguments: %w", def.Name, err)
		}
		if def.ValidateArguments != nil {
			if err := def.ValidateArguments(p); err != nil {
				return nil, fmt.Errorf("invalid %s arguments: %w", def.Name, err)
			}
		}
		return func(ctx context.Context, call ToolCall, update func(any)) (Result, error) {
			res, err := def.Execute(ctx, call, p, func(d D) {
				if update != nil {
					update(d)
				}
			})
			return Result{Content: res.Content, Details: res.Details, IsError: res.IsError, Terminate: res.Terminate}, err
		}, nil
	}}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[def.Name]; ok {
		return fmt.Errorf("tool %q already registered", def.Name)
	}
	r.tools[def.Name] = rt
	r.order = append(r.order, def.Name)
	return nil
}

func (r *Registry) AddBeforeHook(h BeforeHook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before = append(r.before, h)
}
func (r *Registry) AddAfterHook(h AfterHook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.after = append(r.after, h)
}
func (r *Registry) Definitions(names []string) []provider.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	orderedNames := names
	if names == nil {
		orderedNames = r.order
	}
	out := make([]provider.Tool, 0, len(orderedNames))
	for _, name := range orderedNames {
		if t, ok := r.tools[name]; ok {
			out = append(out, t.info)
		}
	}
	return out
}
func (r *Registry) Mode(name string) (ExecutionMode, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t.mode, ok
}
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}
func (r *Registry) Execute(ctx context.Context, call ToolCall, update func(any)) (Result, error) {
	p, err := r.Prepare(ctx, call)
	if err != nil {
		return Result{}, err
	}
	return p.Execute(ctx, update)
}

// Validate checks that a tool exists and that its original arguments can be
// decoded and validated. It does not run before hooks or execute the tool.
func (r *Registry) Validate(call ToolCall) error {
	r.mu.RLock()
	t, ok := r.tools[call.Name]
	r.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	_, err := t.prepare(call.Arguments)
	return err
}

// Prepare validates a tool call and runs before hooks. Agent uses this to keep
// parallel-call preflight deterministic, matching pi's execution contract.
func (r *Registry) Prepare(ctx context.Context, call ToolCall) (*Prepared, error) {
	r.mu.RLock()
	t, ok := r.tools[call.Name]
	before := append([]BeforeHook(nil), r.before...)
	after := append([]AfterHook(nil), r.after...)
	r.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	// Validate the original arguments before allowing hooks to observe the call.
	run, err := t.prepare(call.Arguments)
	if err != nil {
		return nil, err
	}
	for _, h := range before {
		previous := call.Arguments.Clone()
		call, err = h(ctx, call)
		if err != nil {
			return nil, err
		}
		if string(previous) != string(call.Arguments) {
			run, err = t.prepare(call.Arguments)
			if err != nil {
				return nil, err
			}
		}
	}
	return &Prepared{call: call, run: run, after: after}, nil
}

func (p *Prepared) Execute(ctx context.Context, update func(any)) (Result, error) {
	res, runErr := p.run(ctx, p.call, update)
	if runErr != nil {
		if len(res.Content) == 0 {
			res.Content = []message.Content{message.Text(runErr.Error())}
		}
		res.IsError = true
	}
	var err error
	for _, h := range p.after {
		res, err = h(ctx, p.call, res)
		if err != nil {
			return res, err
		}
	}
	return res, runErr
}

func (p *Prepared) Call() ToolCall { return p.call }

func SchemaOf[T any]() (jsonschema.Definition, error) { return schemaFor(reflect.TypeFor[T]()) }
func schemaFor(t reflect.Type) (jsonschema.Definition, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return jsonschema.Definition{}, errors.New("automatic schema requires a struct type")
	}
	return schemaValue(t, make(map[reflect.Type]bool))
}

func schemaValue(t reflect.Type, stack map[reflect.Type]bool) (jsonschema.Definition, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return jsonschema.Definition{Type: jsonschema.Boolean}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return jsonschema.Definition{Type: jsonschema.Integer}, nil
	case reflect.Float32, reflect.Float64:
		return jsonschema.Definition{Type: jsonschema.Number}, nil
	case reflect.String:
		return jsonschema.Definition{Type: jsonschema.String}, nil
	case reflect.Slice, reflect.Array:
		item, err := schemaValue(t.Elem(), stack)
		if err != nil {
			return jsonschema.Definition{}, err
		}
		return jsonschema.Definition{Type: jsonschema.Array, Items: &item}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return jsonschema.Definition{}, errors.New("automatic schema requires string map keys")
		}
		value, err := schemaValue(t.Elem(), stack)
		if err != nil {
			return jsonschema.Definition{}, err
		}
		return jsonschema.Definition{Type: jsonschema.Object, AdditionalProperties: value}, nil
	case reflect.Struct:
		if stack[t] {
			return jsonschema.Definition{}, fmt.Errorf("automatic schema cannot describe recursive type %s", t)
		}
		stack[t] = true
		defer delete(stack, t)
	default:
		return jsonschema.Definition{}, fmt.Errorf("automatic schema cannot describe %s", t)
	}
	props := map[string]jsonschema.Definition{}
	propertyOrder := []string{}
	required := []string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		optional := false
		if tag != "" {
			parts := splitComma(tag)
			if parts[0] != "" {
				name = parts[0]
			}
			optional = contains(parts[1:], "omitempty")
		}
		if !optional {
			required = append(required, name)
		}
		fieldSchema, err := schemaValue(f.Type, stack)
		if err != nil {
			return jsonschema.Definition{}, fmt.Errorf("field %s: %w", f.Name, err)
		}
		props[name] = fieldSchema
		propertyOrder = append(propertyOrder, name)
	}
	return jsonschema.Definition{
		Type:                 jsonschema.Object,
		Properties:           props,
		PropertyOrder:        propertyOrder,
		Required:             required,
		AdditionalProperties: false,
	}, nil
}
func splitComma(s string) []string {
	var out []string
	for {
		i := -1
		for j, c := range s {
			if c == ',' {
				i = j
				break
			}
		}
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i])
		s = s[i+1:]
	}
}
func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
