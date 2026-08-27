package openai

import (
	"fmt"

	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/provider"
)

type normalizedTool struct {
	name, description string
	parameters        map[string]any
}

type normalizedRequest struct {
	request  provider.Request
	messages []message.Message
	tools    []normalizedTool
}

func normalizeRequest(in provider.Request) (normalizedRequest, error) {
	normalized := normalizedRequest{
		request:  in,
		messages: message.NormalizeForProvider(message.ExpandLargeText(in.Messages)),
		tools:    make([]normalizedTool, 0, len(in.Tools)),
	}
	for _, tool := range in.Tools {
		parameters := map[string]any{"type": "object", "properties": map[string]any{}}
		if len(tool.Parameters) > 0 {
			if err := json.Unmarshal(tool.Parameters, &parameters); err != nil {
				return normalizedRequest{}, fmt.Errorf("decode tool %q parameters: %w", tool.Name, err)
			}
		}
		normalized.tools = append(normalized.tools, normalizedTool{
			name: tool.Name, description: tool.Description, parameters: parameters,
		})
	}
	return normalized, nil
}

func dataURL(content message.Content) string {
	return "data:" + content.MimeType + ";base64," + content.Data
}

func splitResponsesToolID(id string) (callID, itemID string) {
	for index := range id {
		if id[index] == '|' {
			return id[:index], id[index+1:]
		}
	}
	return id, ""
}
