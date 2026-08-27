// Package sandsessionconversion demonstrates adapting sand-api SessionEvent
// history to best-harness-go's backend-independent session Snapshot.
package sandsessionconversion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dcalsky/best-harness-go"
)

// These types mirror the persistence-relevant fields in sand-api's
// entity.SessionEvent, core.Event, dto.Message, and dto.ToolCall. Keeping them
// in this example avoids coupling the session package to an application
// protocol or importing sand-api's internal packages.
type sandSessionEvent struct {
	ID               string `json:"Id"`
	NamespaceID      string `json:"NamespaceId"`
	SessionID        string `json:"SessionId"`
	SessionMessageID string `json:"SessionMessageId"`
	UserEmail        string
	HasError         bool
	UserQuery        string
	EventBody        []byte
	DurationMS       int64 `json:"DurationMs"`
	CreatedAt        time.Time
	EventType        string
	ToolCallID       *string `json:"ToolCallId"`
}

type sandEventBody struct {
	NodeName string
	Type     string
	Message  *sandEventMessage `json:",omitempty"`
}

type sandEventMessage struct {
	Content          string
	ReasoningContent string `json:",omitempty"`
	Role             string
	ToolCall         *sandToolCall `json:",omitempty"`
}

type sandToolCall struct {
	ID        string `json:"Id"`
	Name      string
	Arguments string
	Output    string
}

type sandSessionExport struct {
	ID                     string
	Cwd                    string
	InitialState           json.RawMessage
	Events                 []sandSessionEvent
	HistoricalFileContents map[string]string
}

type sandSessionEventConverter struct{}

const (
	sandCompactionSummaryEventType = "compaction-summary"
	sandCompactionSummaryPreface   = "以下是此前对话的摘要，作为后续推理的上下文基础：\n\n"
	sandReadDatasetMaxChars        = 300
)

type sandEventGroup struct {
	messageID string
	query     string
	events    []sandSessionEvent
}

func decodeSandEventBody(event sandSessionEvent) (sandEventBody, bool) {
	var body sandEventBody
	if len(event.EventBody) == 0 || json.Unmarshal(event.EventBody, &body) != nil {
		return sandEventBody{}, false
	}
	return body, true
}

func filterSandCompacted(events []sandSessionEvent) ([]sandSessionEvent, string, time.Time) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType != sandCompactionSummaryEventType {
			continue
		}
		body, ok := decodeSandEventBody(events[i])
		if !ok || body.Message == nil || body.Message.Content == "" {
			continue
		}
		return events[i+1:], body.Message.Content, events[i].CreatedAt
	}
	return events, "", time.Time{}
}

func groupSandEvents(events []sandSessionEvent) []sandEventGroup {
	byID := make(map[string]*sandEventGroup)
	order := make([]string, 0)
	for _, event := range events {
		group := byID[event.SessionMessageID]
		if group == nil {
			group = &sandEventGroup{messageID: event.SessionMessageID}
			byID[event.SessionMessageID] = group
			order = append(order, event.SessionMessageID)
		}
		if group.query == "" && event.UserQuery != "" {
			group.query = event.UserQuery
		}
		group.events = append(group.events, event)
	}
	groups := make([]sandEventGroup, 0, len(order))
	for _, id := range order {
		groups = append(groups, *byID[id])
	}
	return groups
}

func sandGroupHasAssistantResponse(group sandEventGroup) bool {
	for _, event := range group.events {
		if _, ok := decodeSandEventBody(event); !ok {
			continue
		}
		if event.EventType == "text-delta" || event.EventType == "tool-input-available" {
			return true
		}
	}
	return false
}

func compressSandDuplicateUsers(groups []sandEventGroup, files map[string]string) []sandEventGroup {
	compressed := make([]sandEventGroup, 0, len(groups))
	for i := 0; i < len(groups); {
		current := groups[i]
		if current.query == "" || sandGroupHasAssistantResponse(current) {
			compressed = append(compressed, current)
			i++
			continue
		}
		end := i + 1
		for end < len(groups) &&
			!sandGroupHasAssistantResponse(groups[end]) &&
			groups[end].query == current.query &&
			files[groups[end].messageID] == files[current.messageID] {
			current.events = append(current.events, groups[end].events...)
			end++
		}
		compressed = append(compressed, current)
		i = end
	}
	return compressed
}

func (sandSessionEventConverter) ConvertSession(ctx context.Context, source sandSessionExport) (harness.SessionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return harness.SessionSnapshot{}, err
	}
	if len(source.Events) == 0 {
		return harness.SessionSnapshot{}, fmt.Errorf("sand-api session %q has no events", source.ID)
	}

	header := harness.SessionHeader{
		Type:         "session",
		Version:      harness.SessionFormatVersion,
		ID:           source.ID,
		Timestamp:    source.Events[0].CreatedAt.UTC().Format(time.RFC3339Nano),
		Cwd:          source.Cwd,
		InitialState: source.InitialState.Clone(),
	}
	entries := make([]harness.SessionEntry, 0, len(source.Events)*2)
	var parent *harness.SessionEntryID
	nextMessageID := 0
	appendEntry := func(entry harness.SessionEntry) {
		entry.ParentID = parent
		entries = append(entries, entry)
		id := entry.ID
		parent = &id
	}
	appendMessage := func(msg harness.Message, at time.Time) {
		nextMessageID++
		msg.Timestamp = at.UnixMilli()
		appendEntry(harness.SessionEntry{
			Type:      "message",
			ID:        harness.SessionEntryID(fmt.Sprintf("sand-message-%04d", nextMessageID)),
			Timestamp: at.UTC().Format(time.RFC3339Nano),
			Message:   &msg,
		})
	}

	// Preserve every source row as audit data, including compacted, malformed,
	// and UI-only events. Only the reconstructed model context is filtered.
	for _, event := range source.Events {
		raw, err := json.Marshal(event)
		if err != nil {
			return harness.SessionSnapshot{}, fmt.Errorf("encode sand-api event %q: %w", event.ID, err)
		}
		appendEntry(harness.SessionEntry{
			Type:       "custom",
			ID:         harness.SessionEntryID("sand-event-" + event.ID),
			Timestamp:  event.CreatedAt.UTC().Format(time.RFC3339Nano),
			CustomType: "sand-api.session_event",
			Data:       raw,
		})
	}

	visible, summary, summaryAt := filterSandCompacted(source.Events)
	groups := compressSandDuplicateUsers(groupSandEvents(visible), source.HistoricalFileContents)
	if summary != "" {
		appendMessage(harness.Message{
			Role:       harness.RoleAssistant,
			Origin:     harness.OriginModel,
			Content:    []harness.Content{harness.Text(sandCompactionSummaryPreface + summary)},
			StopReason: harness.StopStop,
		}, summaryAt)
	}

	for _, group := range groups {
		firstAt := group.events[0].CreatedAt
		if fileContent := source.HistoricalFileContents[group.messageID]; fileContent != "" {
			appendMessage(harness.User(fileContent), firstAt)
		}
		if group.query != "" {
			appendMessage(harness.User(group.query), firstAt)
		}

		type toolResult struct {
			call    sandToolCall
			isError bool
		}
		var contents []harness.Content
		var calls []sandToolCall
		results := make(map[string]toolResult)
		stepAt := firstAt
		resetStep := func() {
			contents = nil
			calls = nil
			results = make(map[string]toolResult)
		}
		flushStep := func() {
			if len(contents) == 0 && len(calls) == 0 {
				resetStep()
				return
			}
			stopReason := harness.StopStop
			if len(calls) > 0 {
				stopReason = harness.StopToolUse
			}
			appendMessage(harness.Message{
				Role:       harness.RoleAssistant,
				Origin:     harness.OriginModel,
				Content:    contents,
				StopReason: stopReason,
			}, stepAt)
			for _, call := range calls {
				result, found := results[call.ID]
				text := result.call.Output
				if !found || text == "" && result.isError {
					text = "[Tool execution was interrupted]"
				}
				if call.Name == "read_dataset" {
					runes := []rune(text)
					if len(runes) > sandReadDatasetMaxChars {
						text = string(runes[:sandReadDatasetMaxChars]) + "...\n数据过长已被截断"
					}
				}
				appendMessage(harness.Message{
					Role:       harness.RoleTool,
					Origin:     harness.OriginTool,
					Content:    []harness.Content{harness.Text(text)},
					ToolCallID: call.ID,
					ToolName:   call.Name,
					IsError:    !found || result.isError,
				}, stepAt)
			}
			resetStep()
		}

		hasFinishBoundary := false
		for _, event := range group.events {
			if event.EventType == "finish-step" {
				hasFinishBoundary = true
				break
			}
		}
		for _, event := range group.events {
			body, ok := decodeSandEventBody(event)
			if !ok {
				continue
			}
			stepAt = event.CreatedAt
			if hasFinishBoundary {
				switch event.EventType {
				case "start-step":
					// Match sand-api: a persisted start-step begins a fresh step.
					resetStep()
					continue
				case "finish-step":
					flushStep()
					continue
				}
			}
			switch event.EventType {
			case "text-delta":
				if body.Message != nil && body.Message.Content != "" {
					contents = append(contents, harness.Text(body.Message.Content))
				}
			case "tool-input-available":
				if body.Message == nil || body.Message.ToolCall == nil {
					continue
				}
				call := *body.Message.ToolCall
				arguments := json.RawMessage(call.Arguments)
				if !arguments.IsValid() {
					arguments = json.RawMessage(`{}`)
				}
				contents = append(contents, harness.NewToolCallContent(call.ID, call.Name, arguments))
				calls = append(calls, call)
			case "tool-output-available":
				if body.Message != nil && body.Message.ToolCall != nil {
					results[body.Message.ToolCall.ID] = toolResult{call: *body.Message.ToolCall, isError: event.HasError}
				}
			}
		}
		flushStep()
	}

	return harness.SessionSnapshot{Header: header, Entries: entries}, nil
}
