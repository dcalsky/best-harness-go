// Package extension registers compile-time additions and lifecycle hooks.
package extension

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dcalsky/best-harness-go/internal/invocation"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/provider"
	"github.com/dcalsky/best-harness-go/internal/resource"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

type Extension[S any] interface{ Register(*Registry[S]) error }
type InputHook[S any] func(context.Context, invocation.Context[S], message.Message) (message.Message, error)
type ContextHook[S any] func(context.Context, invocation.Context[S], []message.Message) ([]message.Message, error)
type BeforeAgentHook[S any] func(context.Context, invocation.Context[S]) error
type RequestHook[S any] func(context.Context, invocation.Context[S], *provider.Request) error
type ResponseHook[S any] func(context.Context, invocation.Context[S], message.Message) error
type LifecycleHook[S any] func(context.Context, invocation.Context[S]) error
type TreeHook[S any] func(context.Context, invocation.Context[S], string) error
type UserBashHook[S any] func(context.Context, invocation.Context[S], string) (string, error)
type BeforeToolCallHook[S any] func(context.Context, invocation.Context[S], tool.ToolCall) (tool.ToolCall, error)
type AfterToolCallHook[S any] func(context.Context, invocation.Context[S], tool.ToolCall, tool.Result) (tool.Result, error)

type Registry[S any] struct {
	Tools        *tool.Registry
	Models       *model.Registry
	Resources    *resource.Registry
	mu           sync.RWMutex
	providers    map[string]provider.Provider
	Input        []InputHook[S]
	Context      []ContextHook[S]
	BeforeAgent  []BeforeAgentHook[S]
	Request      []RequestHook[S]
	Response     []ResponseHook[S]
	BeforeTool   []BeforeToolCallHook[S]
	AfterTool    []AfterToolCallHook[S]
	SessionStart []LifecycleHook[S]
	Shutdown     []LifecycleHook[S]
	Compact      []LifecycleHook[S]
	Tree         []TreeHook[S]
	UserBash     []UserBashHook[S]
}

func NewRegistry[S any](tools *tool.Registry, models *model.Registry, resources *resource.Registry) *Registry[S] {
	if tools == nil {
		tools = tool.NewRegistry()
	}
	if models == nil {
		models = model.NewRegistry()
	}
	if resources == nil {
		resources = resource.NewRegistry()
	}
	return &Registry[S]{Tools: tools, Models: models, Resources: resources, providers: make(map[string]provider.Provider)}
}
func (r *Registry[S]) RegisterTool[P, D any](def tool.Tool[P, D]) error { return r.Tools.Register(def) }
func (r *Registry[S]) RegisterProvider(name string, p provider.Provider) error {
	if name == "" || p == nil {
		return errors.New("provider name and implementation are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; ok {
		return fmt.Errorf("provider %q already registered", name)
	}
	r.providers[name] = p
	return nil
}
func (r *Registry[S]) Provider(name string) (provider.Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}
func (r *Registry[S]) Providers() map[string]provider.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]provider.Provider, len(r.providers))
	for k, v := range r.providers {
		out[k] = v
	}
	return out
}
func (r *Registry[S]) RegisterModel(m model.Model) error { return r.Models.Register(m) }
func (r *Registry[S]) RegisterLoader(l resource.Loader)  { r.Resources.Register(l) }
func (r *Registry[S]) AddInputHook(h InputHook[S])       { r.Input = append(r.Input, h) }
func (r *Registry[S]) AddContextHook(h ContextHook[S])   { r.Context = append(r.Context, h) }
func (r *Registry[S]) AddBeforeAgentHook(h BeforeAgentHook[S]) {
	r.BeforeAgent = append(r.BeforeAgent, h)
}
func (r *Registry[S]) AddRequestHook(h RequestHook[S])   { r.Request = append(r.Request, h) }
func (r *Registry[S]) AddResponseHook(h ResponseHook[S]) { r.Response = append(r.Response, h) }
func (r *Registry[S]) AddBeforeToolCallHook(h BeforeToolCallHook[S]) {
	r.BeforeTool = append(r.BeforeTool, h)
}
func (r *Registry[S]) AddAfterToolCallHook(h AfterToolCallHook[S]) {
	r.AfterTool = append(r.AfterTool, h)
}
func (r *Registry[S]) AddSessionStartHook(h LifecycleHook[S]) {
	r.SessionStart = append(r.SessionStart, h)
}
func (r *Registry[S]) AddShutdownHook(h LifecycleHook[S]) { r.Shutdown = append(r.Shutdown, h) }
func (r *Registry[S]) AddCompactHook(h LifecycleHook[S])  { r.Compact = append(r.Compact, h) }
func (r *Registry[S]) AddTreeHook(h TreeHook[S])          { r.Tree = append(r.Tree, h) }
func (r *Registry[S]) AddUserBashHook(h UserBashHook[S])  { r.UserBash = append(r.UserBash, h) }
