# DeepSeek E2E coverage

These tests call the paid DeepSeek API. They are skipped unless `BEST_HARNESS_DEEPSEEK_E2E=1` is set. `DEEPSEEK_API_KEY` is read from the environment first and then from the repository `.env` file.

Coverage is split by behavior rather than by package file:

| Area | Scenarios |
| --- | --- |
| Provider and message | SSE text, Unicode, thinking deltas, usage, stop reasons, max tokens, JSON output, extra request fields, cancellation |
| Protocol matrix | OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages text streaming and tool-result replay |
| Vision matrix | `deepseek-v4-flash-vision-exp` base64 PNG recognition through all three protocols |
| Large text | Exact-size 1 MB, 6 MB, and 10 MB CSV fixtures, Unicode-safe head/tail truncation, separate user messages, original-history retention |
| Model | registration, lookup, Flash capabilities |
| Tool and agent | typed arguments/details/updates, hooks, thinking replay, parallel calls, sequential calls, result ordering, termination |
| Queue and retry | busy rejection, steer, follow-up, one-at-a-time drain, retryable provider failure |
| Session | built-in JSONL and application-defined SQLite `Persistence`, recovery, custom entries, navigation, labels, fork, stale extension context |
| Compaction | manual summary, threshold compaction, overflow recovery, tool-call boundary |
| Resource and extension | explicit fsloader, SQLite RULES and SKILLS, merged system prompt, reload, lifecycle and request hooks |
| Builtins | model-driven write, read, edit, grep, find, ls, and bash |

Run with:

```text
BEST_HARNESS_DEEPSEEK_E2E=1 go test -v ./e2e/... -count=1 -timeout 30m
```

To run only the three-protocol matrix:

```text
cd e2e
BEST_HARNESS_DEEPSEEK_E2E=1 go test -v ./... -run '^TestDeepSeekProtocol' -count=1 -timeout 15m
```

## pi SDK context parity

`TestPiSDKProviderContextParity` is an offline cross-SDK test. Each fixture is
run once by this project and once by the local pi SDK; every captured provider
context is then compared message by message. Dynamic timestamps and fields that
exist in only one SDK are excluded from the canonical comparison.

The test uses `$PI_REPO` when set and otherwise looks for pi at `~/www/pi`. The
pi checkout must have its npm dependencies installed so that `tsx` is present.

```text
BEST_HARNESS_PI_PARITY_E2E=1 go test -v ./e2e/... -run TestPiSDKProviderContextParity -count=1
```

## FFF native search

The `TestFFFExampleRepo*` suite indexes `testdata/example-repo` with the real
FFF C library through purego. It checks constrained and fuzzy find, plain and regex
grep, native file-boundary cursor pagination, context line numbering, smart-case, and live
watcher updates against filesystem-derived expectations.

Use the pinned release asset for the current platform:

```text
cd e2e
CGO_ENABLED=0 BEST_HARNESS_FFF_INTEGRATION=1 go test -v -count=1 -run '^TestFFFExampleRepo' .
```

For offline runs, set `BEST_HARNESS_FFF_LIBRARY` to a compatible FFF v0.10.6
shared library instead.
