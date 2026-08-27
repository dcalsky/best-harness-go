package adapterutil_test

import (
	"errors"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/provider/internal/adapterutil"
)

func TestErrorCauseClassifiesOnlyContextOverflow(t *testing.T) {
	original := errors.New("provider error")
	tests := []struct {
		name   string
		status int
		code   string
		text   string
		want   bool
	}{
		{name: "HTTP context code", status: 400, code: "context_length_exceeded", want: true},
		{name: "stream context message", text: "prompt is too long", want: true},
		{name: "Anthropic context window", status: 400, text: "too many tokens for the context window", want: true},
		{name: "same message on unrelated status", status: 429, text: "context window", want: false},
		{name: "ordinary bad request", status: 400, code: "invalid_request_error", text: "invalid temperature", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := adapterutil.ErrorCause(test.status, test.code, test.text, original)
			if got := errors.Is(cause, message.ErrContextOverflow); got != test.want {
				t.Fatalf("context overflow=%v want=%v cause=%v", got, test.want, cause)
			}
			if !errors.Is(cause, original) {
				t.Fatalf("original provider error was not preserved: %v", cause)
			}
		})
	}
}

func TestToolOutputPreservesTextBlockBoundaries(t *testing.T) {
	got := adapterutil.ToolOutput([]message.Content{
		message.Text("first"), message.Image("AAAA", "image/png"), message.Text("second"),
	})
	if got != "first\nsecond" {
		t.Fatalf("tool output=%q", got)
	}
	if got := adapterutil.ToolOutput([]message.Content{message.Image("AAAA", "image/png")}); got != "(no tool output)" {
		t.Fatalf("image-only text fallback=%q", got)
	}
}
