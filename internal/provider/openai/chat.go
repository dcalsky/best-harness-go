package openai

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"

	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/provider"
	"github.com/dcalsky/best-harness-go/internal/provider/internal/adapterutil"
)

var chatReservedFields = fieldSet(
	"model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens",
	"temperature", "top_p", "seed", "frequency_penalty", "presence_penalty",
	"stop", "reasoning_effort", "response_format", "parallel_tool_calls", "tools",
	"thinking_budget", "preserve_thinking",
)

func (p *Provider) streamChat(ctx context.Context, in provider.Request) (provider.Stream, error) {
	normalized, err := normalizeRequest(in)
	if err != nil {
		return nil, err
	}
	params, requestOptions, err := encodeChat(normalized)
	if err != nil {
		return nil, err
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, params, requestOptions...)
	return &chatStream{
		stream: stream, provider: in.Model.Provider,
		inputPrice: in.Model.InputPrice, outputPrice: in.Model.OutputPrice,
		pending: []message.StreamEvent{{Type: message.EventStart}},
		tools:   make(map[int]*chatToolState),
	}, nil
}

func encodeChat(in normalizedRequest) (openaisdk.ChatCompletionNewParams, []option.RequestOption, error) {
	generation := in.request.Generation
	if generation.Thinking != nil && !*generation.Thinking && in.request.ReasoningEffort != "" {
		return openaisdk.ChatCompletionNewParams{}, nil, errors.New("thinking=false conflicts with an explicit reasoning effort")
	}
	if generation.TopK != nil {
		return openaisdk.ChatCompletionNewParams{}, nil, errors.New("OpenAI Chat Completions does not support top_k")
	}
	if generation.ReasoningBudgetTokens != nil {
		return openaisdk.ChatCompletionNewParams{}, nil, errors.New("OpenAI Chat Completions does not support a numeric reasoning token budget")
	}
	if generation.ThinkingBudget < 0 {
		return openaisdk.ChatCompletionNewParams{}, nil, errors.New("thinking_budget must not be negative")
	}
	messages, err := encodeChatMessages(in)
	if err != nil {
		return openaisdk.ChatCompletionNewParams{}, nil, err
	}
	params := openaisdk.ChatCompletionNewParams{
		Model:             shared.ChatModel(in.request.Model.ID),
		Messages:          messages,
		Temperature:       optFloat(generation.Temperature),
		TopP:              optFloat(generation.TopP),
		Seed:              optInt(generation.Seed),
		FrequencyPenalty:  optFloat(generation.FrequencyPenalty),
		PresencePenalty:   optFloat(generation.PresencePenalty),
		ParallelToolCalls: optBool(generation.ParallelToolCalls),
		StreamOptions: openaisdk.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}
	if in.request.MaxTokens > 0 {
		if generation.UseMaxCompletionTokens {
			params.MaxCompletionTokens = param.NewOpt(in.request.MaxTokens)
		} else {
			params.MaxTokens = param.NewOpt(in.request.MaxTokens)
		}
	}
	if len(generation.StopSequences) > 0 {
		params.Stop.OfStringArray = append([]string(nil), generation.StopSequences...)
	}
	if in.request.ReasoningEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(in.request.ReasoningEffort)
	} else if generation.Thinking != nil && !*generation.Thinking {
		params.ReasoningEffort = shared.ReasoningEffortNone
	}
	if generation.JSONOutput {
		params.ResponseFormat.OfJSONObject = &shared.ResponseFormatJSONObjectParam{}
	}
	for _, tool := range in.tools {
		definition := shared.FunctionDefinitionParam{
			Name: tool.name, Parameters: shared.FunctionParameters(tool.parameters),
		}
		if tool.description != "" {
			definition.Description = param.NewOpt(tool.description)
		}
		params.Tools = append(params.Tools, openaisdk.ChatCompletionFunctionTool(definition))
	}
	extraDefaults := make(map[string]any, 2)
	if generation.ThinkingBudget > 0 {
		extraDefaults["thinking_budget"] = generation.ThinkingBudget
	}
	if generation.PreserveThinking {
		extraDefaults["preserve_thinking"] = true
	}
	extraBody, err := adapterutil.MergeExtraBody(generation.ExtraBody, extraDefaults)
	if err != nil {
		return openaisdk.ChatCompletionNewParams{}, nil, err
	}
	if len(extraBody) > 0 {
		params.SetExtraFields(extraBody)
	}
	extra, err := adapterutil.DecodeExtra(generation.Extra, chatReservedFields)
	if err != nil {
		return openaisdk.ChatCompletionNewParams{}, nil, err
	}
	requestOptions := make([]option.RequestOption, 0, len(extra))
	for key, value := range extra {
		requestOptions = append(requestOptions, option.WithJSONSet(key, value))
	}
	return params, requestOptions, nil
}

func encodeChatMessages(in normalizedRequest) ([]openaisdk.ChatCompletionMessageParamUnion, error) {
	messages := make([]openaisdk.ChatCompletionMessageParamUnion, 0, len(in.messages)+1)
	if in.request.SystemPrompt != "" {
		messages = append(messages, openaisdk.SystemMessage(in.request.SystemPrompt))
	}
	for _, msg := range in.messages {
		switch msg.Role {
		case message.RoleUser:
			parts := make([]openaisdk.ChatCompletionContentPartUnionParam, 0, len(msg.Content))
			for _, content := range msg.Content {
				switch content.Type {
				case "text", "largeText":
					if text := content.LLMText(); text != "" {
						parts = append(parts, openaisdk.TextContentPart(text))
					}
				case "image":
					if in.request.Model.SupportsImages {
						parts = append(parts, openaisdk.ImageContentPart(openaisdk.ChatCompletionContentPartImageImageURLParam{URL: dataURL(content)}))
					}
				}
			}
			if len(parts) > 0 {
				messages = append(messages, openaisdk.UserMessage(parts))
			}
		case message.RoleAssistant:
			assistant := openaisdk.ChatCompletionAssistantMessageParam{}
			var text strings.Builder
			var reasoning strings.Builder
			for _, content := range msg.Content {
				switch content.Type {
				case "text", "largeText":
					text.WriteString(content.LLMText())
				case "thinking":
					if msg.API == "" || msg.API == in.request.Model.API {
						reasoning.WriteString(content.Thinking)
					}
				case "toolCall":
					call := openaisdk.ChatCompletionMessageFunctionToolCallParam{
						ID: content.ID,
						Function: openaisdk.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name: content.Name, Arguments: string(content.Arguments),
						},
					}
					assistant.ToolCalls = append(assistant.ToolCalls, openaisdk.ChatCompletionMessageToolCallUnionParam{OfFunction: &call})
				}
			}
			if text.Len() > 0 {
				assistant.Content.OfString = param.NewOpt(text.String())
			}
			if reasoning.Len() > 0 {
				// OpenAI-compatible reasoning models such as DeepSeek require the
				// prior reasoning_content when continuing after a tool call. The
				// official SDK deliberately permits compatible message extensions.
				assistant.SetExtraFields(map[string]any{"reasoning_content": reasoning.String()})
			}
			if text.Len() > 0 || len(assistant.ToolCalls) > 0 {
				messages = append(messages, openaisdk.ChatCompletionMessageParamUnion{OfAssistant: &assistant})
			}
		case message.RoleTool:
			messages = append(messages, encodeChatToolOutput(msg, in.request.Model.SupportsImages)...)
		}
	}
	return messages, nil
}

func encodeChatToolOutput(msg message.Message, supportsImages bool) []openaisdk.ChatCompletionMessageParamUnion {
	text := adapterutil.ToolOutput(msg.Content)
	parts := make([]openaisdk.ChatCompletionContentPartUnionParam, 0, len(msg.Content)+1)
	for _, content := range msg.Content {
		if content.Type == "image" && supportsImages {
			parts = append(parts, openaisdk.ImageContentPart(openaisdk.ChatCompletionContentPartImageImageURLParam{URL: dataURL(content)}))
		}
	}
	if len(parts) == 0 {
		return []openaisdk.ChatCompletionMessageParamUnion{openaisdk.ToolMessage(text, msg.ToolCallID)}
	}
	if text == "(no tool output)" {
		text = "(see attached image)"
	}
	imageMessage := make([]openaisdk.ChatCompletionContentPartUnionParam, 0, len(parts)+1)
	imageMessage = append(imageMessage, openaisdk.TextContentPart("Attached image(s) from tool result:"))
	imageMessage = append(imageMessage, parts...)
	return []openaisdk.ChatCompletionMessageParamUnion{
		openaisdk.ToolMessage(text, msg.ToolCallID),
		openaisdk.UserMessage(imageMessage),
	}
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

func optBool(value *bool) param.Opt[bool] {
	if value == nil {
		return param.Opt[bool]{}
	}
	return param.NewOpt(*value)
}

type chatToolState struct {
	id, name string
}

type chatStream struct {
	stream      *ssestream.Stream[openaisdk.ChatCompletionChunk]
	mu          sync.Mutex
	pending     []message.StreamEvent
	tools       map[int]*chatToolState
	closed      bool
	terminal    bool
	provider    string
	inputPrice  float64
	outputPrice float64
	responseID  string
	model       string
	usage       message.Usage
	stopReason  message.StopReason
}

func (s *chatStream) Next() (message.StreamEvent, error) {
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
	}
	if err := s.stream.Err(); err != nil {
		s.terminal = true
		return message.StreamEvent{Type: message.EventError, Err: convertError(s.provider, err)}, nil
	}
	if s.stopReason == "" {
		s.terminal = true
		return message.StreamEvent{}, errors.New("OpenAI Chat Completions stream ended without finish_reason")
	}
	for index := range s.tools {
		s.pending = append(s.pending, message.StreamEvent{Type: message.EventToolCallEnd, Index: index})
	}
	s.tools = nil
	metadata, err := adapterutil.Metadata(s.responseID, s.model)
	if err != nil {
		s.terminal = true
		return message.StreamEvent{}, err
	}
	s.pending = append(s.pending, message.StreamEvent{
		Type: message.EventDone, StopReason: s.stopReason, Usage: s.usage,
		ProviderMetadata: metadata,
	})
	s.terminal = true
	return s.pop(), nil
}

func (s *chatStream) append(chunk openaisdk.ChatCompletionChunk) {
	if chunk.ID != "" {
		s.responseID = chunk.ID
	}
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage.JSON.PromptTokens.Valid() || chunk.Usage.TotalTokens != 0 {
		s.usage = adapterutil.Usage(
			chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens,
			chunk.Usage.PromptTokensDetails.CachedTokens,
			chunk.Usage.PromptTokensDetails.CacheWriteTokens,
			chunk.Usage.TotalTokens, s.inputPrice, s.outputPrice,
		)
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventTextDelta, Text: choice.Delta.Content})
		}
		if choice.Delta.Refusal != "" {
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventTextDelta, Text: choice.Delta.Refusal})
		}
		if reasoning := chatReasoningDelta(choice.Delta); reasoning != "" {
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventThinkingDelta, Text: reasoning, Signature: "openai-chat-reasoning"})
		}
		for _, call := range choice.Delta.ToolCalls {
			index := int(call.Index)
			state := s.tools[index]
			if state == nil {
				state = &chatToolState{id: call.ID, name: call.Function.Name}
				s.tools[index] = state
				s.pending = append(s.pending, message.StreamEvent{
					Type: message.EventToolCallStart, Index: index,
					ToolCallID: state.id, ToolName: state.name, ArgumentsDelta: call.Function.Arguments,
				})
				continue
			}
			if call.ID != "" {
				state.id = call.ID
			}
			if call.Function.Name != "" {
				state.name = call.Function.Name
			}
			if call.ID != "" || call.Function.Name != "" || call.Function.Arguments != "" {
				s.pending = append(s.pending, message.StreamEvent{
					Type: message.EventToolCallDelta, Index: index,
					ToolCallID: call.ID, ToolName: call.Function.Name, ArgumentsDelta: call.Function.Arguments,
				})
			}
		}
		if choice.FinishReason != "" {
			s.stopReason = mapChatStopReason(choice.FinishReason)
		}
	}
}

func chatReasoningDelta(delta openaisdk.ChatCompletionChunkChoiceDelta) string {
	var compatible struct {
		Reasoning        string `json:"reasoning"`
		ReasoningContent string `json:"reasoning_content"`
	}
	if json.Unmarshal([]byte(delta.RawJSON()), &compatible) != nil {
		return ""
	}
	if compatible.Reasoning != "" {
		return compatible.Reasoning
	}
	return compatible.ReasoningContent
}

func mapChatStopReason(reason string) message.StopReason {
	switch reason {
	case "stop":
		return message.StopStop
	case "length":
		return message.StopLength
	case "tool_calls", "function_call":
		return message.StopToolUse
	default:
		return message.StopError
	}
}

func (s *chatStream) pop() message.StreamEvent {
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event
}

func (s *chatStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.stream.Close()
}
