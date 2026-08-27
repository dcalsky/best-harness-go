package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/provider"
	"github.com/dcalsky/best-harness-go/internal/provider/internal/adapterutil"
)

var responsesReservedFields = fieldSet(
	"model", "input", "instructions", "stream", "store", "include",
	"max_output_tokens", "temperature", "top_p", "parallel_tool_calls",
	"reasoning", "text", "tools",
)

func (p *Provider) streamResponses(ctx context.Context, in provider.Request) (provider.Stream, error) {
	normalized, err := normalizeRequest(in)
	if err != nil {
		return nil, err
	}
	params, requestOptions, err := encodeResponses(normalized)
	if err != nil {
		return nil, err
	}
	stream := p.client.Responses.NewStreaming(ctx, params, requestOptions...)
	return &responsesStream{
		stream: stream, provider: in.Model.Provider,
		inputPrice: in.Model.InputPrice, outputPrice: in.Model.OutputPrice,
		pending: []message.StreamEvent{{Type: message.EventStart}},
		tools:   make(map[int]*responsesToolState),
	}, nil
}

func encodeResponses(in normalizedRequest) (responses.ResponseNewParams, []option.RequestOption, error) {
	generation := in.request.Generation
	if generation.Thinking != nil && !*generation.Thinking && in.request.ReasoningEffort != "" {
		return responses.ResponseNewParams{}, nil, errors.New("thinking=false conflicts with an explicit reasoning effort")
	}
	if generation.TopK != nil || generation.Seed != nil || generation.FrequencyPenalty != nil || generation.PresencePenalty != nil || len(generation.StopSequences) > 0 {
		return responses.ResponseNewParams{}, nil, errors.New("OpenAI Responses does not support top_k, seed, frequency/presence penalties, or stop sequences")
	}
	if generation.ReasoningBudgetTokens != nil {
		return responses.ResponseNewParams{}, nil, errors.New("OpenAI Responses does not support a numeric reasoning token budget")
	}
	if generation.ThinkingBudget != 0 || generation.PreserveThinking {
		return responses.ResponseNewParams{}, nil, errors.New("ThinkingBudget and PreserveThinking are only supported by OpenAI-compatible Chat APIs; use ExtraBody for provider-native Responses fields")
	}
	input, err := encodeResponsesInput(in)
	if err != nil {
		return responses.ResponseNewParams{}, nil, err
	}
	params := responses.ResponseNewParams{
		Model:             shared.ResponsesModel(in.request.Model.ID),
		Input:             responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Store:             param.NewOpt(false),
		Temperature:       optFloat(generation.Temperature),
		TopP:              optFloat(generation.TopP),
		ParallelToolCalls: optBool(generation.ParallelToolCalls),
		Include:           []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
	}
	if in.request.SystemPrompt != "" {
		params.Instructions = param.NewOpt(in.request.SystemPrompt)
	}
	if in.request.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(in.request.MaxTokens)
	}
	if in.request.ReasoningEffort != "" || generation.Thinking != nil {
		params.Reasoning.Summary = shared.ReasoningSummaryAuto
		if in.request.ReasoningEffort != "" {
			params.Reasoning.Effort = shared.ReasoningEffort(in.request.ReasoningEffort)
		} else if generation.Thinking != nil && !*generation.Thinking {
			params.Reasoning.Effort = shared.ReasoningEffortNone
		}
	}
	if generation.JSONOutput {
		params.Text.Format.OfJSONObject = &shared.ResponseFormatJSONObjectParam{}
	}
	for _, tool := range in.tools {
		definition := responses.ToolParamOfFunction(tool.name, tool.parameters, false)
		if tool.description != "" {
			definition.OfFunction.Description = param.NewOpt(tool.description)
		}
		params.Tools = append(params.Tools, definition)
	}
	extraBody, err := adapterutil.MergeExtraBody(generation.ExtraBody, nil)
	if err != nil {
		return responses.ResponseNewParams{}, nil, err
	}
	if len(extraBody) > 0 {
		params.SetExtraFields(extraBody)
	}
	extra, err := adapterutil.DecodeExtra(generation.Extra, responsesReservedFields)
	if err != nil {
		return responses.ResponseNewParams{}, nil, err
	}
	requestOptions := make([]option.RequestOption, 0, len(extra))
	for key, value := range extra {
		requestOptions = append(requestOptions, option.WithJSONSet(key, value))
	}
	return params, requestOptions, nil
}

func encodeResponsesInput(in normalizedRequest) (responses.ResponseInputParam, error) {
	input := make(responses.ResponseInputParam, 0, len(in.messages))
	for _, msg := range in.messages {
		switch msg.Role {
		case message.RoleUser:
			parts := make(responses.ResponseInputMessageContentListParam, 0, len(msg.Content))
			for _, content := range msg.Content {
				switch content.Type {
				case "text", "largeText":
					if text := content.LLMText(); text != "" {
						parts = append(parts, responses.ResponseInputContentParamOfInputText(text))
					}
				case "image":
					if in.request.Model.SupportsImages {
						part := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
						part.OfInputImage.ImageURL = param.NewOpt(dataURL(content))
						parts = append(parts, part)
					}
				}
			}
			if len(parts) > 0 {
				input = append(input, responses.ResponseInputItemParamOfMessage(parts, responses.EasyInputMessageRoleUser))
			}
		case message.RoleAssistant:
			var text strings.Builder
			flushText := func() {
				if text.Len() == 0 {
					return
				}
				input = append(input, responses.ResponseInputItemParamOfMessage(text.String(), responses.EasyInputMessageRoleAssistant))
				text.Reset()
			}
			for _, content := range msg.Content {
				switch content.Type {
				case "thinking":
					flushText()
					if content.Signature != "" && (msg.API == "" || msg.API == in.request.Model.API) {
						var reasoning responses.ResponseReasoningItemParam
						if err := json.Unmarshal([]byte(content.Signature), &reasoning); err == nil && reasoning.ID != "" {
							input = append(input, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
						}
					}
				case "text", "largeText":
					text.WriteString(content.LLMText())
				case "toolCall":
					flushText()
					callID, itemID := splitResponsesToolID(content.ID)
					call := responses.ResponseInputItemParamOfFunctionCall(string(content.Arguments), callID, content.Name)
					if itemID != "" {
						call.OfFunctionCall.ID = param.NewOpt(itemID)
					}
					input = append(input, call)
				}
			}
			flushText()
		case message.RoleTool:
			callID, _ := splitResponsesToolID(msg.ToolCallID)
			output := responsesToolOutput(msg.Content, in.request.Model.SupportsImages)
			if output.parts == nil {
				input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(callID, output.text))
			} else {
				input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(callID, output.parts))
			}
		}
	}
	return input, nil
}

type responsesToolResult struct {
	text  string
	parts responses.ResponseFunctionCallOutputItemListParam
}

func responsesToolOutput(contents []message.Content, supportsImages bool) responsesToolResult {
	parts := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(contents))
	for _, content := range contents {
		switch content.Type {
		case "text", "largeText":
			if text := content.LLMText(); text != "" {
				parts = append(parts, responses.ResponseFunctionCallOutputItemParamOfInputText(text))
			}
		case "image":
			if supportsImages {
				parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{
					OfInputImage: &responses.ResponseInputImageContentParam{
						ImageURL: param.NewOpt(dataURL(content)),
						Detail:   responses.ResponseInputImageContentDetailAuto,
					},
				})
			}
		}
	}
	if len(parts) == 0 {
		return responsesToolResult{text: "(no tool output)"}
	}
	if len(parts) == 1 && parts[0].OfInputText != nil {
		return responsesToolResult{text: parts[0].OfInputText.Text}
	}
	return responsesToolResult{parts: parts}
}

type responsesToolState struct {
	id, name, arguments string
}

type responsesStream struct {
	stream      *ssestream.Stream[responses.ResponseStreamEventUnion]
	mu          sync.Mutex
	pending     []message.StreamEvent
	tools       map[int]*responsesToolState
	closed      bool
	terminal    bool
	provider    string
	inputPrice  float64
	outputPrice float64
	sawTool     bool
}

func (s *responsesStream) Next() (message.StreamEvent, error) {
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
	return message.StreamEvent{}, errors.New("OpenAI Responses stream ended without a terminal response event")
}

func (s *responsesStream) append(event responses.ResponseStreamEventUnion) {
	switch event.Type {
	case "response.output_text.delta", "response.refusal.delta":
		s.pending = append(s.pending, message.StreamEvent{Type: message.EventTextDelta, Text: event.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		s.pending = append(s.pending, message.StreamEvent{Type: message.EventThinkingDelta, Text: event.Delta})
	case "response.output_item.added":
		s.startResponsesItem(int(event.OutputIndex), event.Item)
	case "response.function_call_arguments.delta":
		index := int(event.OutputIndex)
		state := s.tools[index]
		if state == nil {
			state = &responsesToolState{}
			s.tools[index] = state
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventToolCallStart, Index: index})
		}
		state.arguments += event.Delta
		s.pending = append(s.pending, message.StreamEvent{Type: message.EventToolCallDelta, Index: index, ArgumentsDelta: event.Delta})
	case "response.output_item.done":
		s.finishResponsesItem(int(event.OutputIndex), event.Item)
	case "response.completed":
		s.finishResponse(event.Response, false)
	case "response.incomplete":
		s.finishResponse(event.Response, true)
	case "response.failed":
		code, text := string(event.Response.Error.Code), event.Response.Error.Message
		if text == "" {
			text = "OpenAI Responses request failed"
		}
		s.fail(&message.ProviderError{
			Provider: s.provider, Code: code, Message: text,
			Retryable: strings.Contains(code, "rate_limit") || strings.Contains(code, "server"),
			Cause:     adapterutil.ErrorCause(0, code, text, nil),
		})
	case "error":
		s.fail(&message.ProviderError{
			Provider: s.provider, Code: event.Code, Message: event.Message,
			Retryable: strings.Contains(event.Code, "rate_limit") || strings.Contains(event.Code, "server"),
			Cause:     adapterutil.ErrorCause(0, event.Code, event.Message, nil),
		})
	}
}

func (s *responsesStream) startResponsesItem(index int, item responses.ResponseOutputItemUnion) {
	if item.Type != "function_call" {
		return
	}
	id := item.CallID
	if item.ID != "" {
		id += "|" + item.ID
	}
	state := &responsesToolState{id: id, name: item.Name, arguments: item.Arguments.OfString}
	s.tools[index] = state
	s.sawTool = true
	s.pending = append(s.pending, message.StreamEvent{
		Type: message.EventToolCallStart, Index: index,
		ToolCallID: id, ToolName: item.Name, ArgumentsDelta: state.arguments,
	})
}

func (s *responsesStream) finishResponsesItem(index int, item responses.ResponseOutputItemUnion) {
	switch item.Type {
	case "reasoning":
		if raw := item.RawJSON(); raw != "" {
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventThinkingDelta, Signature: raw})
		}
	case "function_call":
		state := s.tools[index]
		if state == nil {
			s.startResponsesItem(index, item)
			state = s.tools[index]
		}
		if state != nil {
			complete := item.Arguments.OfString
			if strings.HasPrefix(complete, state.arguments) {
				if suffix := strings.TrimPrefix(complete, state.arguments); suffix != "" {
					s.pending = append(s.pending, message.StreamEvent{Type: message.EventToolCallDelta, Index: index, ArgumentsDelta: suffix})
				}
			}
			s.pending = append(s.pending, message.StreamEvent{Type: message.EventToolCallEnd, Index: index})
			delete(s.tools, index)
		}
	}
}

func (s *responsesStream) finishResponse(response responses.Response, incomplete bool) {
	reason := message.StopStop
	if incomplete {
		if response.IncompleteDetails.Reason == "max_output_tokens" {
			reason = message.StopLength
		} else {
			s.fail(fmt.Errorf("OpenAI Responses response was incomplete: %s", response.IncompleteDetails.Reason))
			return
		}
	}
	if s.sawTool {
		reason = message.StopToolUse
	}
	usage := adapterutil.Usage(
		response.Usage.InputTokens, response.Usage.OutputTokens,
		response.Usage.InputTokensDetails.CachedTokens,
		response.Usage.InputTokensDetails.CacheWriteTokens,
		response.Usage.TotalTokens, s.inputPrice, s.outputPrice,
	)
	metadata, err := adapterutil.Metadata(response.ID, string(response.Model))
	if err != nil {
		s.fail(err)
		return
	}
	s.terminal = true
	s.pending = append(s.pending, message.StreamEvent{
		Type: message.EventDone, StopReason: reason, Usage: usage,
		ProviderMetadata: metadata,
	})
}

func (s *responsesStream) fail(err error) {
	s.terminal = true
	s.pending = append(s.pending, message.StreamEvent{Type: message.EventError, Err: err})
}

func (s *responsesStream) pop() message.StreamEvent {
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event
}

func (s *responsesStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.stream.Close()
}
