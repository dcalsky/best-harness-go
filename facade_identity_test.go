package harness_test

import (
	"testing"

	"github.com/dcalsky/best-harness-go"
)

type structValidatorStub struct{}

func (structValidatorStub) Struct(any) error { return nil }

var (
	_ harness.Message                 = harness.Message{}
	_ harness.Model                   = harness.Model{}
	_ harness.PromptToolCall          = harness.PromptToolCall{}
	_ harness.ProviderTool            = harness.ProviderTool{}
	_ harness.ID                      = harness.ID("")
	_ harness.ToolCall                = harness.ToolCall{}
	_ harness.Tool[int, string]       = harness.Tool[int, string]{}
	_ harness.ArgumentsValidator[int] = func(int) error { return nil }
	_ harness.StructValidator         = structValidatorStub{}
	_ harness.SessionOption           = harness.WithTokenEstimator(harness.TokenEstimatorFunc(func(harness.Message) int64 { return 1 }))
	_ harness.ValidatorOption         = harness.WithValidatorRetryLimit(2)
	_ harness.ToolOption[int]         = harness.WithArgumentsValidator(func(int) error { return nil })
	_ harness.ToolOption[int]         = harness.WithArgumentsValidator(func(int) error { return nil }, harness.WithValidatorRetryLimit(2))
	_ harness.ToolOption[int]         = harness.WithStructValidator[int](structValidatorStub{})
	_ harness.ToolResult[string]      = harness.ToolResult[string]{}
	_ harness.Provider                = &harness.Faux{}
)

func TestFacadeExposesSentinelErrors(t *testing.T) {
	errors := []error{
		harness.ErrContextOverflow,
		harness.ErrModelNotFound,
		harness.ErrAborted,
		harness.ErrNotFound,
		harness.ErrToolNotFound,
	}
	for i, err := range errors {
		if err == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
	}
}
