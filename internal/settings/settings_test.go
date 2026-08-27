package settings_test

import (
	"errors"
	"github.com/dcalsky/best-harness-go/internal/settings"
	"testing"
)

func TestTypedSettings(t *testing.T) {
	s := settings.New()
	key := settings.Setting[int]{Key: "retries", Default: 2, Validate: func(v int) error {
		if v < 0 {
			return errors.New("negative")
		}
		return nil
	}}
	if s.Get(key) != 2 {
		t.Fatal("default")
	}
	if err := s.Set(key, 4); err != nil || s.Get(key) != 4 {
		t.Fatal(err)
	}
	if err := s.Set(key, -1); err == nil {
		t.Fatal("expected validation error")
	}
}
