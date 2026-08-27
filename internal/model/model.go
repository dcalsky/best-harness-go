// Package model stores model capabilities without tying them to a provider implementation.
package model

import (
	"errors"
	"sort"
	"sync"
)

// API identifies the wire protocol used to invoke a model. Provider names and
// APIs are deliberately separate: one provider can expose models over more
// than one protocol, and OpenAI-compatible vendors can use the OpenAI API
// without being the "openai" provider.
type API string

const (
	APIOpenAI          API = "openai"
	APIOpenAIResponses API = "openai-responses"
	APIAnthropic       API = "anthropic"

	// Longer aliases match the names commonly used by provider SDKs.
	APIOpenAICompletions = APIOpenAI
	APIAnthropicMessages = APIAnthropic
)

type Model struct {
	Provider          string
	API               API
	ID                string
	Name              string
	ContextWindow     int64
	MaxOutput         int64
	InputPrice        float64
	OutputPrice       float64
	SupportsImages    bool
	SupportsReasoning bool
}

func (m Model) Key() string { return m.Provider + "/" + m.ID }

var ErrModelNotFound = errors.New("model not found")

type Registry struct {
	mu     sync.RWMutex
	models map[string]Model
}

func NewRegistry() *Registry { return &Registry{models: make(map[string]Model)} }
func (r *Registry) Register(m Model) error {
	if m.Provider == "" || m.ID == "" {
		return errors.New("model provider and id are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models[m.Key()] = m
	return nil
}
func (r *Registry) Get(provider, id string) (Model, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[provider+"/"+id]
	if !ok {
		return Model{}, ErrModelNotFound
	}
	return m, nil
}
func (r *Registry) List(match func(Model) bool) []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Model, 0, len(r.models))
	for _, m := range r.models {
		if match == nil || match(m) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
func (r *Registry) Next(current Model, match func(Model) bool) (Model, error) {
	ms := r.List(match)
	if len(ms) == 0 {
		return Model{}, ErrModelNotFound
	}
	for i, m := range ms {
		if m.Key() == current.Key() {
			return ms[(i+1)%len(ms)], nil
		}
	}
	return ms[0], nil
}
