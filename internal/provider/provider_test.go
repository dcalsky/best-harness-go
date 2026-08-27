package provider_test

import (
	"encoding/json"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/provider"
)

func TestGenerationConfigCloneIsRequestLocal(t *testing.T) {
	temperature := provider.Ptr(0.0)
	config := provider.GenerationConfig{
		Temperature:           temperature,
		TopK:                  provider.Ptr(int64(40)),
		StopSequences:         []string{"END"},
		ReasoningBudgetTokens: provider.Ptr(int64(2_048)),
		ThinkingBudget:        16_384,
		PreserveThinking:      true,
		ExtraBody: map[string]any{
			"metadata": map[string]any{"user_id": "original"},
			"include":  []any{"reasoning", map[string]any{"type": "summary"}},
		},
		Extra: map[string]json.RawMessage{
			"reasoning": json.RawMessage(`{"budget_tokens":2048}`),
		},
	}

	clone := config.Clone()
	*clone.Temperature = 0.8
	*clone.ReasoningBudgetTokens = 4_096
	clone.StopSequences[0] = "DONE"
	clone.ExtraBody["metadata"].(map[string]any)["user_id"] = "clone"
	clone.ExtraBody["include"].([]any)[1].(map[string]any)["type"] = "details"
	clone.Extra["reasoning"][0] = '['

	if *config.Temperature != 0.0 || config.Temperature != temperature {
		t.Fatalf("temperature changed through clone: %v", *config.Temperature)
	}
	if config.StopSequences[0] != "END" {
		t.Fatalf("stop sequences changed through clone: %v", config.StopSequences)
	}
	if *config.ReasoningBudgetTokens != 2_048 {
		t.Fatalf("reasoning budget changed through clone: %v", *config.ReasoningBudgetTokens)
	}
	if got := config.ExtraBody["metadata"].(map[string]any)["user_id"]; got != "original" {
		t.Fatalf("nested extra body map changed through clone: %v", got)
	}
	if got := config.ExtraBody["include"].([]any)[1].(map[string]any)["type"]; got != "summary" {
		t.Fatalf("nested extra body slice changed through clone: %v", got)
	}
	if got := string(config.Extra["reasoning"]); got != `{"budget_tokens":2048}` {
		t.Fatalf("extra changed through clone: %s", got)
	}
}

func TestPtrPreservesNamedType(t *testing.T) {
	type temperature float64

	value := temperature(0.25)
	pointer := provider.Ptr(value)
	if *pointer != value {
		t.Fatalf("pointer value=%v", *pointer)
	}
}
