package core

import (
	"testing"

	"github.com/dcalsky/best-harness-go/internal/compact"
	"github.com/dcalsky/best-harness-go/internal/message"
)

type pointerTokenEstimator struct{}

func (*pointerTokenEstimator) Estimate(message.Message) int64 { return 11 }

func TestWithTokenEstimatorOverridesBaseOptions(t *testing.T) {
	base := compact.TokenEstimatorFunc(func(message.Message) int64 { return 1 })
	override := compact.TokenEstimatorFunc(func(message.Message) int64 { return 7 })

	options, err := applySessionOptions(SessionOptions{
		Compaction: compact.Options{Estimator: base},
	}, []SessionOption{WithTokenEstimator(override)})
	if err != nil {
		t.Fatal(err)
	}
	if got := options.Compaction.Estimator.Estimate(message.User("ignored")); got != 7 {
		t.Fatalf("estimate=%d, want 7", got)
	}
}

func TestWithTokenEstimatorRejectsNil(t *testing.T) {
	var typedNil *pointerTokenEstimator
	for _, option := range []SessionOption{
		WithTokenEstimator(nil),
		WithTokenEstimator(typedNil),
	} {
		if _, err := applySessionOptions(SessionOptions{}, []SessionOption{option}); err == nil {
			t.Fatal("expected nil token estimator to fail")
		}
	}
}
