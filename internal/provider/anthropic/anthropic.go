// Package anthropic adapts the official Anthropic Go SDK Messages API to the
// provider-neutral harness boundary.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/provider"
	"github.com/dcalsky/best-harness-go/internal/provider/internal/adapterutil"
)

const defaultMaxTokens int64 = 4_096

var reservedFields = fieldSet(
	"model", "messages", "system", "stream", "max_tokens", "temperature",
	"top_p", "top_k", "stop_sequences", "thinking", "output_config",
	"tool_choice", "tools",
)

type Provider struct {
	client anthropicsdk.Client
}

func New(client anthropicsdk.Client) *Provider { return &Provider{client: client} }

var _ provider.Provider = (*Provider)(nil)

func (p *Provider) Stream(ctx context.Context, in provider.Request) (provider.Stream, error) {
	if in.Model.API != "" && in.Model.API != model.APIAnthropic {
		return nil, fmt.Errorf("Anthropic SDK provider does not support model API %q", in.Model.API)
	}
	params, requestOptions, err := encodeRequest(in)
	if err != nil {
		return nil, err
	}
	sdkStream := p.client.Messages.NewStreaming(ctx, params, requestOptions...)
	return &stream{
		stream: sdkStream, provider: in.Model.Provider,
		inputPrice: in.Model.InputPrice, outputPrice: in.Model.OutputPrice,
		pending: []message.StreamEvent{{Type: message.EventStart}},
		blocks:  make(map[int]*blockState),
	}, nil
}

func encodeRequest(in provider.Request) (anthropicsdk.MessageNewParams, []option.RequestOption, error) {
	generation := in.Generation
	if generation.Thinking != nil && !*generation.Thinking && (generation.ReasoningBudgetTokens != nil || in.ReasoningEffort != "") {
		return anthropicsdk.MessageNewParams{}, nil, errors.New("thinking=false conflicts with an explicit reasoning effort or token budget")
	}
	if generation.Seed != nil || generation.FrequencyPenalty != nil || generation.PresencePenalty != nil {
		return anthropicsdk.MessageNewParams{}, nil, errors.New("Anthropic Messages does not support seed or frequency/presence penalties")
	}
	if generation.ThinkingBudget != 0 || generation.PreserveThinking {
		return anthropicsdk.MessageNewParams{}, nil, errors.New("ThinkingBudget and PreserveThinking are only supported by OpenAI-compatible Chat APIs; use ExtraBody for provider-native Anthropic fields")
	}
	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	messages, err := encodeMessages(in)
	if err != nil {
		return anthropicsdk.MessageNewParams{}, nil, err
	}
	params := anthropicsdk.MessageNewParams{
		Model:         anthropicsdk.Model(in.Model.ID),
		MaxTokens:     maxTokens,
		Messages:      messages,
		Temperature:   optFloat(generation.Temperature),
		TopP:          optFloat(generation.TopP),
		TopK:          optInt(generation.TopK),
		StopSequences: append([]string(nil), generation.StopSequences...),
	}
	if in.SystemPrompt != "" {
		params.System = []anthropicsdk.TextBlockParam{{Text: in.SystemPrompt}}
	}
	if generation.ParallelToolCalls != nil {
		params.ToolChoice.OfAuto = &anthropicsdk.ToolChoiceAutoParam{
			DisableParallelToolUse: param.NewOpt(!*generation.ParallelToolCalls),
		}
	}
	if generation.Thinking != nil && !*generation.Thinking {
		disabled := anthropicsdk.NewThinkingConfigDisabledParam()
		params.Thinking.OfDisabled = &disabled
	} else if generation.ReasoningBudgetTokens != nil {
		budget := *generation.ReasoningBudgetTokens
		if budget < 1_024 {
			return anthropicsdk.MessageNewParams{}, nil, fmt.Errorf("Anthropic reasoning token budget must be at least 1024, got %d", budget)
		}
		if budget >= maxTokens {
			return anthropicsdk.MessageNewParams{}, nil, fmt.Errorf("Anthropic reasoning token budget %d must be less than max_tokens %d", budget, maxTokens)
		}
		params.Thinking = anthropicsdk.ThinkingConfigParamOfEnabled(budget)
	} else if generation.Thinking != nil || in.ReasoningEffort != "" {
		params.Thinking.OfAdaptive = &anthropicsdk.ThinkingConfigAdaptiveParam{}
	}
	if in.ReasoningEffort != "" {
		params.OutputConfig.Effort = anthropicsdk.OutputConfigEffort(in.ReasoningEffort)
	}
	if generation.JSONOutput {
		params.OutputConfig.Format.Schema = map[string]any{"type": "object"}
	}
	for _, tool := range in.Tools {
		var schema anthropicsdk.ToolInputSchemaParam
		if len(tool.Parameters) == 0 {
			schema.Properties = map[string]any{}
		} else if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
			return anthropicsdk.MessageNewParams{}, nil, fmt.Errorf("decode tool %q parameters: %w", tool.Name, err)
		}
		definition := anthropicsdk.ToolUnionParamOfTool(schema, tool.Name)
		if tool.Description != "" {
			definition.OfTool.Description = param.NewOpt(tool.Description)
		}
		params.Tools = append(params.Tools, definition)
	}
	extraBody, err := adapterutil.MergeExtraBody(generation.ExtraBody, nil)
	if err != nil {
		return anthropicsdk.MessageNewParams{}, nil, err
	}
	if len(extraBody) > 0 {
		params.SetExtraFields(extraBody)
	}
	extra, err := adapterutil.DecodeExtra(generation.Extra, reservedFields)
	if err != nil {
		return anthropicsdk.MessageNewParams{}, nil, err
	}
	requestOptions := make([]option.RequestOption, 0, len(extra))
	for key, value := range extra {
		requestOptions = append(requestOptions, option.WithJSONSet(key, value))
	}
	return params, requestOptions, nil
}

func encodeMessages(in provider.Request) ([]anthropicsdk.MessageParam, error) {
	messages := message.NormalizeForProvider(message.ExpandLargeText(in.Messages))
	out := make([]anthropicsdk.MessageParam, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		msg := messages[index]
		switch msg.Role {
		case message.RoleUser:
			blocks := anthropicUserBlocks(msg.Content, in.Model.SupportsImages)
			if len(blocks) > 0 {
				out = append(out, anthropicsdk.NewUserMessage(blocks...))
			}
		case message.RoleAssistant:
			blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(msg.Content))
			for _, content := range msg.Content {
				switch content.Type {
				case "text", "largeText":
					if text := content.LLMText(); text != "" {
						blocks = append(blocks, anthropicsdk.NewTextBlock(text))
					}
				case "thinking":
					if content.Signature != "" && (msg.API == "" || msg.API == model.APIAnthropic) {
						blocks = append(blocks, anthropicsdk.NewThinkingBlock(content.Signature, content.Thinking))
					} else if content.Thinking != "" {
						blocks = append(blocks, anthropicsdk.NewTextBlock(content.Thinking))
					}
				case "toolCall":
					input := any(map[string]any{})
					if len(content.Arguments) > 0 {
						if err := json.Unmarshal(content.Arguments, &input); err != nil {
							return nil, fmt.Errorf("decode replayed Anthropic tool arguments for %q: %w", content.Name, err)
						}
					}
					blocks = append(blocks, anthropicsdk.NewToolUseBlock(content.ID, input, content.Name))
				}
			}
			if len(blocks) > 0 {
				out = append(out, anthropicsdk.NewAssistantMessage(blocks...))
			}
		case message.RoleTool:
			blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, 1)
			for index < len(messages) && messages[index].Role == message.RoleTool {
				toolMessage := messages[index]
				blocks = append(blocks, anthropicToolResultBlock(toolMessage, in.Model.SupportsImages))
				index++
			}
			index--
			out = append(out, anthropicsdk.NewUserMessage(blocks...))
		}
	}
	return out, nil
}

func anthropicToolResultBlock(msg message.Message, supportsImages bool) anthropicsdk.ContentBlockParamUnion {
	contents := make([]anthropicsdk.ToolResultBlockParamContentUnion, 0, len(msg.Content))
	for _, content := range msg.Content {
		switch content.Type {
		case "text", "largeText":
			if text := content.LLMText(); text != "" {
				contents = append(contents, anthropicsdk.ToolResultBlockParamContentUnion{
					OfText: &anthropicsdk.TextBlockParam{Text: text},
				})
			}
		case "image":
			if supportsImages {
				image := anthropicsdk.NewImageBlockBase64(content.MimeType, content.Data)
				contents = append(contents, anthropicsdk.ToolResultBlockParamContentUnion{OfImage: image.OfImage})
			}
		}
	}
	if len(contents) == 0 {
		contents = append(contents, anthropicsdk.ToolResultBlockParamContentUnion{
			OfText: &anthropicsdk.TextBlockParam{Text: "(no tool output)"},
		})
	}
	return anthropicsdk.ContentBlockParamUnion{OfToolResult: &anthropicsdk.ToolResultBlockParam{
		ToolUseID: msg.ToolCallID,
		Content:   contents,
		IsError:   param.NewOpt(msg.IsError),
	}}
}

func anthropicUserBlocks(contents []message.Content, supportsImages bool) []anthropicsdk.ContentBlockParamUnion {
	blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(contents))
	for _, content := range contents {
		switch content.Type {
		case "text", "largeText":
			if text := content.LLMText(); text != "" {
				blocks = append(blocks, anthropicsdk.NewTextBlock(text))
			}
		case "image":
			if supportsImages {
				blocks = append(blocks, anthropicsdk.NewImageBlockBase64(content.MimeType, content.Data))
			}
		}
	}
	return blocks
}

func fieldSet(fields ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	return set
}

func optFloat(value *float64) param.Opt[float64] {
	if value == nil {
		return param.Opt[float64]{}
	}
	return param.NewOpt(*value)
}

func optInt(value *int64) param.Opt[int64] {
	if value == nil {
		return param.Opt[int64]{}
	}
	return param.NewOpt(*value)
}

type blockState struct {
	typeName, signature string
}

type stream struct {
	stream       *ssestream.Stream[anthropicsdk.MessageStreamEventUnion]
	mu           sync.Mutex
	pending      []message.StreamEvent
	blocks       map[int]*blockState
	closed       bool
	terminal     bool
	provider     string
	inputPrice   float64
	outputPrice  float64
	responseID   string
	model        string
	stopReason   message.StopReason
	inputTokens  int64
	outputTokens int64
	cacheRead    int64
	cacheWrite   int64
}

func (s *stream) Next() (message.StreamEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) > 0 {
		return s.pop(), nil
	}
	if s.closed || s.terminal {
		return message.StreamEvent{}, io.EOF
	}
	for s.stream.Next() {
		s.append(s.stream.Current())
		if len(s.pending) > 0 {
			return s.pop(), nil
		}
		if s.terminal {
			return message.StreamEvent{}, io.EOF
		}
	}
	if err := s.stream.Err(); err != nil {
		s.terminal = true
		return message.StreamEvent{Type: message.EventError, Err: convertError(s.provider, err)}, nil
	}
	s.terminal = true
	return message.StreamEvent{}, errors.New("Anthropic Messages stream ended without message_stop")
}

func (s *stream) append(event anthropicsdk.MessageStreamEventUnion) {
	switch event.Type {
	case "message_start":
		s.responseID = event.Message.ID
		s.model = string(event.Message.Model)
		s.mergeUsage(event.Message.Usage.InputTokens, event.Message.Usage.OutputTokens, event.Message.Usage.CacheReadInputTokens, event.Message.Usage.CacheCreationInputTokens)
	case "content_block_start":
		index := int(event.Index)
		block := event.ContentBlock
		state := &blockState{typeName: block.Type, signature: block.Signature}
		s.blocks[index] = state
		switch block.Type {
		case "text":
			if block.Text != "" {
				s.pending = append(s.pending, message.StreamEvent{Type: message.EventTextDelta, Text: block.Text})
			}
		case "thinking":
			if block.Thinking != "" {
				s.pending = append(s.pending, message.StreamEvent{Type: message.EventThinkingDelta, Text: block.Thinking})
			}
		case "redacted_thinking":
			state.signature = block.Data
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventThinkingDelta, Text: "[Reasoning redacted]"})
		case "tool_use":
			arguments := ""
			if block.Input != nil {
				if raw, err := json.Marshal(block.Input); err == nil && string(raw) != "{}" && string(raw) != "null" {
					arguments = string(raw)
				}
			}
			s.pending = append(s.pending, message.StreamEvent{
				Type: message.EventToolCallStart, Index: index,
				ToolCallID: block.ID, ToolName: block.Name, ArgumentsDelta: arguments,
			})
		}
	case "content_block_delta":
		index := int(event.Index)
		switch event.Delta.Type {
		case "text_delta":
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventTextDelta, Text: event.Delta.Text})
		case "thinking_delta":
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventThinkingDelta, Text: event.Delta.Thinking})
		case "signature_delta":
			if state := s.blocks[index]; state != nil {
				state.signature += event.Delta.Signature
			}
		case "input_json_delta":
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventToolCallDelta, Index: index, ArgumentsDelta: event.Delta.PartialJSON})
		}
	case "content_block_stop":
		index := int(event.Index)
		if state := s.blocks[index]; state != nil {
			switch state.typeName {
			case "thinking", "redacted_thinking":
				if state.signature != "" {
					s.pending = append(s.pending, message.StreamEvent{Type: message.EventThinkingDelta, Signature: state.signature})
				}
			case "tool_use":
				s.pending = append(s.pending, message.StreamEvent{Type: message.EventToolCallEnd, Index: index})
			}
			delete(s.blocks, index)
		}
	case "message_delta":
		if event.Delta.StopReason != "" {
			s.stopReason = mapStopReason(event.Delta.StopReason)
		}
		s.mergeUsage(event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CacheReadInputTokens, event.Usage.CacheCreationInputTokens)
	case "message_stop":
		if s.stopReason == "" || s.stopReason == message.StopError {
			s.fail(errors.New("Anthropic stream ended without a supported stop_reason"))
			return
		}
		metadata, err := adapterutil.Metadata(s.responseID, s.model)
		if err != nil {
			s.fail(err)
			return
		}
		s.terminal = true
		s.pending = append(s.pending, message.StreamEvent{
			Type: message.EventDone, StopReason: s.stopReason,
			Usage:            adapterutil.Usage(s.inputTokens+s.cacheRead+s.cacheWrite, s.outputTokens, s.cacheRead, s.cacheWrite, 0, s.inputPrice, s.outputPrice),
			ProviderMetadata: metadata,
		})
	}
}

func (s *stream) mergeUsage(input, output, cacheRead, cacheWrite int64) {
	if input != 0 {
		s.inputTokens = input
	}
	if output != 0 {
		s.outputTokens = output
	}
	if cacheRead != 0 {
		s.cacheRead = cacheRead
	}
	if cacheWrite != 0 {
		s.cacheWrite = cacheWrite
	}
}

func mapStopReason(reason anthropicsdk.StopReason) message.StopReason {
	switch reason {
	case anthropicsdk.StopReasonEndTurn, anthropicsdk.StopReasonStopSequence, anthropicsdk.StopReasonPauseTurn:
		return message.StopStop
	case anthropicsdk.StopReasonMaxTokens, anthropicsdk.StopReasonModelContextWindowExceeded:
		return message.StopLength
	case anthropicsdk.StopReasonToolUse:
		return message.StopToolUse
	default:
		return message.StopError
	}
}

func convertError(providerName string, err error) error {
	var apiError *anthropicsdk.Error
	if !errors.As(err, &apiError) {
		return err
	}
	var envelope struct {
		Error struct {
			Type, Message string
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(apiError.RawJSON()), &envelope)
	code := envelope.Error.Type
	if code == "" {
		code = string(apiError.Type())
	}
	text := envelope.Error.Message
	if text == "" {
		text = err.Error()
	}
	return &message.ProviderError{
		Provider: providerName, StatusCode: apiError.StatusCode,
		Code: code, Message: text,
		Retryable: adapterutil.RetryableStatus(apiError.StatusCode) || strings.Contains(code, "overloaded"),
		Cause:     adapterutil.ErrorCause(apiError.StatusCode, code, text, err),
	}
}

func (s *stream) fail(err error) {
	s.terminal = true
	s.pending = append(s.pending, message.StreamEvent{Type: message.EventError, Err: err})
}

func (s *stream) pop() message.StreamEvent {
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event
}

func (s *stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.stream.Close()
}
