// Package settings stores typed SDK settings.
package settings

import (
	"errors"
	"sync"
)

type Setting[T any] struct {
	Key      string
	Default  T
	Validate func(T) error
}
type Settings struct {
	mu     sync.RWMutex
	values map[string]any
}

func New() *Settings { return &Settings{values: make(map[string]any)} }
func (s *Settings) Set[T any](key Setting[T], value T) error {
	if key.Key == "" {
		return errors.New("setting key is required")
	}
	if key.Validate != nil {
		if err := key.Validate(value); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key.Key] = value
	return nil
}
func (s *Settings) Get[T any](key Setting[T]) T {
	s.mu.RLock()
	v, ok := s.values[key.Key]
	s.mu.RUnlock()
	if !ok {
		return key.Default
	}
	typed, ok := v.(T)
	if !ok {
		return key.Default
	}
	return typed
}
