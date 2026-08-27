package sandsessionconversion

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

func marshalSandEventBody(t *testing.T, eventType string, msg *sandEventMessage) []byte {
	t.Helper()
	raw, err := json.Marshal(sandEventBody{NodeName: "Supervisor", Type: eventType, Message: msg})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newSandTestEvent(t *testing.T, index int, messageID, query, eventType string, msg *sandEventMessage) sandSessionEvent {
	t.Helper()
	return sandSessionEvent{
		ID:               fmt.Sprintf("%04d", index),
		NamespaceID:      "onemesh",
		SessionID:        "sand-session-1",
		SessionMessageID: messageID,
		UserEmail:        "developer@example.com",
		UserQuery:        query,
		EventBody:        marshalSandEventBody(t, eventType, msg),
		DurationMS:       int64(index * 10),
		CreatedAt:        time.Date(2026, 8, 25, 10, 0, index, 0, time.UTC),
		EventType:        eventType,
	}
}

func newSandTestSource(events []sandSessionEvent, files map[string]string) sandSessionExport {
	return sandSessionExport{
		ID:                     "sand-session-1",
		Cwd:                    "/srv/sand-api",
		InitialState:           json.RawMessage(`{"source":"sand-api"}`),
		Events:                 events,
		HistoricalFileContents: files,
	}
}

func convertSandTestSource(t *testing.T, source sandSessionExport) *harness.SessionManager {
	t.Helper()
	snapshot, err := harness.ConvertSession(t.Context(), source, sandSessionEventConverter{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := snapshot.MarshalJSONL()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "converted.jsonl")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := harness.OpenFileSession(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func simulatedSandSessionEvents(t *testing.T) sandSessionExport {
	t.Helper()
	toolID := "call-sql-1"
	missingToolID := "call-missing-1"

	events := []sandSessionEvent{
		newSandTestEvent(t, 1, "turn-1", "统计华东区域销售额", "start-step", nil),
		newSandTestEvent(t, 2, "turn-1", "统计华东区域销售额", "reasoning-delta", &sandEventMessage{Role: "assistant", ReasoningContent: "先查询销售表。"}),
		newSandTestEvent(t, 3, "turn-1", "统计华东区域销售额", "tool-input-available", &sandEventMessage{Role: "assistant", ToolCall: &sandToolCall{ID: toolID, Name: "execute_sql", Arguments: `{"sql":"SELECT sum(sales) FROM sales WHERE region = '华东'"}`}}),
		newSandTestEvent(t, 4, "turn-1", "统计华东区域销售额", "tool-output-available", &sandEventMessage{Role: "assistant", ToolCall: &sandToolCall{ID: toolID, Name: "execute_sql", Output: `{"total":1200}`}}),
		newSandTestEvent(t, 5, "turn-1", "统计华东区域销售额", "finish-step", nil),
		newSandTestEvent(t, 6, "turn-1", "统计华东区域销售额", "text-delta", &sandEventMessage{Role: "assistant", Content: "华东区域销售额为 1200。"}),
		newSandTestEvent(t, 7, "turn-1", "统计华东区域销售额", "finish-step", nil),
		newSandTestEvent(t, 8, "turn-2", "再读取明细", "start-step", nil),
		newSandTestEvent(t, 9, "turn-2", "再读取明细", "tool-input-available", &sandEventMessage{Role: "assistant", ToolCall: &sandToolCall{ID: missingToolID, Name: "read_dataset", Arguments: `{"dataset_id":"sales-1"}`}}),
		newSandTestEvent(t, 10, "turn-2", "再读取明细", "finish-step", nil),
	}
	events[2].ToolCallID = &toolID
	events[3].ToolCallID = &toolID
	events[8].ToolCallID = &missingToolID

	return newSandTestSource(events, nil)
}

func TestConvertSandAPISessionEvents(t *testing.T) {
	source := simulatedSandSessionEvents(t)
	snapshot, err := harness.ConvertSession(t.Context(), source, sandSessionEventConverter{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.ID != source.ID || snapshot.Header.Cwd != source.Cwd {
		t.Fatalf("header=%#v", snapshot.Header)
	}

	path := filepath.Join(t.TempDir(), "sand-session.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.WriteTo(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err := harness.OpenFileSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	got := manager.Context().Messages
	wantRoles := []harness.Role{
		harness.RoleUser,
		harness.RoleAssistant,
		harness.RoleTool,
		harness.RoleAssistant,
		harness.RoleUser,
		harness.RoleAssistant,
		harness.RoleTool,
	}
	if len(got) != len(wantRoles) {
		t.Fatalf("messages=%#v", got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("message %d role=%q want=%q", i, got[i].Role, role)
		}
	}
	if got[0].Text() != "统计华东区域销售额" || got[2].Text() != `{"total":1200}` || got[3].Text() != "华东区域销售额为 1200。" {
		t.Fatalf("converted first turn=%#v", got[:4])
	}
	if len(got[1].Content) != 1 || got[1].Content[0].Type != "toolCall" || got[1].Content[0].ID != "call-sql-1" {
		t.Fatalf("assistant tool message=%#v", got[1])
	}
	if got[6].ToolCallID != "call-missing-1" || !got[6].IsError || got[6].Text() != "[Tool execution was interrupted]" {
		t.Fatalf("repaired tool result=%#v", got[6])
	}
	if normalized := harness.NormalizeMessagesForProvider(got); len(normalized) != len(got) {
		t.Fatalf("provider normalization changed repaired history: got=%d normalized=%d", len(got), len(normalized))
	}

	rawEvents, err := manager.CustomEntries[sandSessionEvent]("sand-api.session_event")
	if err != nil {
		t.Fatal(err)
	}
	if len(rawEvents) != len(source.Events) || rawEvents[0].Data.ID != source.Events[0].ID || rawEvents[len(rawEvents)-1].Data.EventType != "finish-step" {
		t.Fatalf("raw events=%#v", rawEvents)
	}
}

func TestConvertSandAPISessionEventsWithoutFinishBoundary(t *testing.T) {
	callID := "call-legacy"
	events := []sandSessionEvent{
		newSandTestEvent(t, 1, "legacy-turn", "旧数据问题", "start-step", nil),
		newSandTestEvent(t, 2, "legacy-turn", "", "tool-input-available", &sandEventMessage{ToolCall: &sandToolCall{ID: callID, Name: "lookup", Arguments: `{}`}}),
		newSandTestEvent(t, 3, "legacy-turn", "", "text-delta", &sandEventMessage{Content: "处理中"}),
		newSandTestEvent(t, 4, "legacy-turn", "", "text-delta", &sandEventMessage{Content: "完成"}),
		newSandTestEvent(t, 5, "legacy-turn", "", "tool-output-available", &sandEventMessage{ToolCall: &sandToolCall{ID: callID, Name: "lookup", Output: "result"}}),
	}
	got := convertSandTestSource(t, newSandTestSource(events, nil)).Context().Messages
	if len(got) != 3 || got[0].Text() != "旧数据问题" || got[1].Text() != "处理中完成" || len(got[1].Content) != 3 || got[2].ToolCallID != callID {
		t.Fatalf("legacy messages=%#v", got)
	}
}

func TestConvertSandAPISessionEventsSkipsMalformedUIAndEmptyEvents(t *testing.T) {
	events := []sandSessionEvent{
		newSandTestEvent(t, 1, "broken-turn", "保留用户问题", "start-step", nil),
		newSandTestEvent(t, 2, "broken-turn", "", "chat-aborted", nil),
		newSandTestEvent(t, 3, "broken-turn", "", "chat-failed", &sandEventMessage{Content: "internal failure"}),
		newSandTestEvent(t, 4, "broken-turn", "", "finish-step", nil),
	}
	events[1].EventBody = []byte(`{"Type":`)
	manager := convertSandTestSource(t, newSandTestSource(events, nil))
	got := manager.Context().Messages
	if len(got) != 1 || got[0].Role != harness.RoleUser || got[0].Text() != "保留用户问题" {
		t.Fatalf("visible messages=%#v", got)
	}
	raw, err := manager.CustomEntries[sandSessionEvent]("sand-api.session_event")
	if err != nil || len(raw) != len(events) {
		t.Fatalf("raw events=%#v err=%v", raw, err)
	}
}

func TestConvertSandAPISessionEventsCompressesOnlyEquivalentUnansweredTurns(t *testing.T) {
	events := []sandSessionEvent{
		newSandTestEvent(t, 1, "retry-1", "重试问题", "chat-failed", nil),
		newSandTestEvent(t, 2, "retry-2", "重试问题", "chat-aborted", nil),
		newSandTestEvent(t, 3, "retry-3", "重试问题", "text-delta", &sandEventMessage{Content: "最终回答"}),
	}
	got := convertSandTestSource(t, newSandTestSource(events, nil)).Context().Messages
	if len(got) != 3 || got[0].Text() != "重试问题" || got[1].Text() != "重试问题" || got[2].Text() != "最终回答" {
		t.Fatalf("compressed messages=%#v", got)
	}

	files := map[string]string{"retry-1": "file A", "retry-2": "file B"}
	got = convertSandTestSource(t, newSandTestSource(events[:2], files)).Context().Messages
	want := []string{"file A", "重试问题", "file B", "重试问题"}
	if len(got) != len(want) {
		t.Fatalf("file messages=%#v", got)
	}
	for i := range want {
		if got[i].Text() != want[i] {
			t.Fatalf("file message %d=%q want=%q", i, got[i].Text(), want[i])
		}
	}
}

func TestConvertSandAPISessionEventsUsesLatestValidCompaction(t *testing.T) {
	events := []sandSessionEvent{
		newSandTestEvent(t, 1, "old", "旧问题", "text-delta", &sandEventMessage{Content: "旧回答"}),
		newSandTestEvent(t, 2, "compact-1", "", sandCompactionSummaryEventType, &sandEventMessage{Content: "第一版摘要"}),
		newSandTestEvent(t, 3, "middle", "中间问题", "text-delta", &sandEventMessage{Content: "中间回答"}),
		newSandTestEvent(t, 4, "compact-2", "", sandCompactionSummaryEventType, &sandEventMessage{Content: "最终摘要"}),
		newSandTestEvent(t, 5, "compact-corrupt", "", sandCompactionSummaryEventType, nil),
		newSandTestEvent(t, 6, "recent", "最新问题", "text-delta", &sandEventMessage{Content: "最新回答"}),
	}
	events[4].EventBody = []byte(`not-json`)
	manager := convertSandTestSource(t, newSandTestSource(events, nil))
	got := manager.Context().Messages
	if len(got) != 3 || got[0].Role != harness.RoleAssistant || got[0].Text() != sandCompactionSummaryPreface+"最终摘要" || got[1].Text() != "最新问题" || got[2].Text() != "最新回答" {
		t.Fatalf("compacted messages=%#v", got)
	}
	raw, err := manager.CustomEntries[sandSessionEvent]("sand-api.session_event")
	if err != nil || len(raw) != len(events) {
		t.Fatalf("raw events=%#v err=%v", raw, err)
	}
}

func TestConvertSandAPISessionEventsOrdersRepairsAndTruncatesToolResults(t *testing.T) {
	longOutput := ""
	for range sandReadDatasetMaxChars + 1 {
		longOutput += "界"
	}
	events := []sandSessionEvent{
		newSandTestEvent(t, 1, "tools", "工具问题", "tool-input-available", &sandEventMessage{ToolCall: &sandToolCall{ID: "read", Name: "read_dataset", Arguments: `{"id":1}`}}),
		newSandTestEvent(t, 2, "tools", "", "tool-input-available", &sandEventMessage{ToolCall: &sandToolCall{ID: "other", Name: "lookup", Arguments: `{"id":2}`}}),
		newSandTestEvent(t, 3, "tools", "", "tool-input-available", &sandEventMessage{ToolCall: &sandToolCall{ID: "failed", Name: "lookup", Arguments: `not-json`}}),
		newSandTestEvent(t, 4, "tools", "", "tool-output-available", &sandEventMessage{ToolCall: &sandToolCall{ID: "other", Name: "lookup", Output: "other-result"}}),
		newSandTestEvent(t, 5, "tools", "", "tool-output-available", &sandEventMessage{ToolCall: &sandToolCall{ID: "read", Name: "read_dataset", Output: longOutput}}),
		newSandTestEvent(t, 6, "tools", "", "tool-output-available", &sandEventMessage{ToolCall: &sandToolCall{ID: "failed", Name: "lookup"}}),
		newSandTestEvent(t, 7, "tools", "", "finish-step", nil),
	}
	events[5].HasError = true
	got := convertSandTestSource(t, newSandTestSource(events, nil)).Context().Messages
	if len(got) != 5 || got[1].Role != harness.RoleAssistant {
		t.Fatalf("tool messages=%#v", got)
	}
	if got[2].ToolCallID != "read" || got[3].ToolCallID != "other" || got[4].ToolCallID != "failed" {
		t.Fatalf("tool result order=%#v", got[2:])
	}
	wantLong := longOutput[:sandReadDatasetMaxChars*len("界")] + "...\n数据过长已被截断"
	if got[2].Text() != wantLong || got[3].Text() != "other-result" || !got[4].IsError || got[4].Text() != "[Tool execution was interrupted]" {
		t.Fatalf("tool results=%#v", got[2:])
	}
	if string(got[1].Content[2].Arguments) != `{}` {
		t.Fatalf("repaired arguments=%s", got[1].Content[2].Arguments)
	}
}
