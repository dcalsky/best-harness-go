# sand-api SessionEvent conversion

This example keeps application-specific migration code outside the `session`
package. It mirrors the persistence-relevant fields from sand-api because Go's
`internal` package boundary prevents this standalone module from importing
`sand-api/internal/model/entity` directly. A converter implemented inside
sand-api can use `entity.SessionEvent` and `core.Event` without the mirror
types.

The example demonstrates:

- grouping rows by `SessionMessageId` and rebuilding user, assistant, and tool
  messages;
- preserving every original row as a `sand-api.session_event` custom entry;
- splitting agent iterations at `finish-step` while supporting older histories
  without step boundaries;
- repairing interrupted tool calls, ordering parallel results, and truncating
  oversized `read_dataset` results;
- applying the latest valid compaction summary and compressing equivalent
  unanswered user turns;
- writing a validated JSONL v4 snapshot that `harness.OpenFileSession` can read.

Run the example's compatibility suite with:

```text
go test ./examples/sand_session_conversion
```
