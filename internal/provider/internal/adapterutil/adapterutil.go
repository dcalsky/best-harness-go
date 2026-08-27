// Package adapterutil contains small protocol-neutral helpers shared by the
// official SDK provider adapters.
package adapterutil

import (
	"errors"
	"fmt"
	"strings"

	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
)

// DecodeExtra validates and decodes provider-native request fields.
func DecodeExtra(extra map[string]json.RawMessage, reserved map[string]struct{}) (map[string]any, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	decoded := make(map[string]any, len(extra))
	for key, raw := range extra {
		if _, found := reserved[key]; found {
			return nil, fmt.Errorf("provider-native generation field %q conflicts with a normalized field", key)
		}
		if key == "" {
			return nil, fmt.Errorf("provider-native generation field name is empty")
		}
		var value any
		if len(raw) == 0 {
			return nil, fmt.Errorf("provider-native generation field %q has an empty JSON value", key)
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode provider-native generation field %q: %w", key, err)
		}
		decoded[key] = value
	}
	return decoded, nil
}

// MergeExtraBody builds an SDK extra-fields map without mutating caller-owned
// configuration. Defaults are applied first; explicitly configured ExtraBody
// values intentionally win on key collisions, matching SDK extra_body behavior.
func MergeExtraBody(extraBody, defaults map[string]any) (map[string]any, error) {
	if len(extraBody) == 0 && len(defaults) == 0 {
		return nil, nil
	}
	merged := make(map[string]any, len(extraBody)+len(defaults))
	for key, value := range defaults {
		merged[key] = value
	}
	for key, value := range extraBody {
		if key == "" {
			return nil, fmt.Errorf("extra_body field name is empty")
		}
		merged[key] = value
	}
	if _, err := json.Marshal(merged); err != nil {
		return nil, fmt.Errorf("encode extra_body: %w", err)
	}
	return merged, nil
}

func Metadata(responseID, responseModel string) (map[string]json.RawMessage, error) {
	values := map[string]string{"responseId": responseID, "responseModel": responseModel}
	metadata := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		if value == "" {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode provider metadata %q: %w", key, err)
		}
		metadata[key] = raw
	}
	return metadata, nil
}

func Usage(input, output, cacheRead, cacheWrite, total int64, inputPrice, outputPrice float64) message.Usage {
	uncachedInput := input - cacheRead - cacheWrite
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	if total == 0 {
		total = uncachedInput + output + cacheRead + cacheWrite
	}
	cost := &message.Cost{
		Input:      float64(uncachedInput) * inputPrice / 1_000_000,
		Output:     float64(output) * outputPrice / 1_000_000,
		CacheRead:  float64(cacheRead) * inputPrice / 1_000_000,
		CacheWrite: float64(cacheWrite) * inputPrice / 1_000_000,
	}
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	return message.Usage{
		InputTokens: uncachedInput, OutputTokens: output,
		CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
		TotalTokens: total, Cost: cost,
	}
}

func ToolOutput(contents []message.Content) string {
	var parts []string
	for _, content := range contents {
		if content.Type == "text" || content.Type == "largeText" {
			if text := content.LLMText(); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		return "(no tool output)"
	}
	return strings.Join(parts, "\n")
}

func RetryableStatus(status int) bool {
	return status == 408 || status == 409 || status == 425 || status == 429 || status >= 500
}

// ErrorCause preserves the provider's original error while classifying context
// window exhaustion for the harness' automatic overflow-compaction path. A zero
// status is accepted for errors delivered inside an otherwise successful stream.
func ErrorCause(status int, code, text string, cause error) error {
	if status != 0 && status != 400 {
		return cause
	}
	value := strings.ToLower(code + " " + text)
	contextOverflow := strings.Contains(value, "context_length") ||
		strings.Contains(value, "context length") ||
		strings.Contains(value, "context_window") ||
		strings.Contains(value, "context window") ||
		strings.Contains(value, "maximum context") ||
		strings.Contains(value, "too many tokens") ||
		strings.Contains(value, "prompt is too long")
	if !contextOverflow {
		return cause
	}
	if cause == nil {
		return message.ErrContextOverflow
	}
	return errors.Join(message.ErrContextOverflow, cause)
}
