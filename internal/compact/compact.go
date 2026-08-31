// Package compact selects safe context boundaries and records summaries.
package compact

import (
	"context"
	"errors"
	"fmt"
	"strings"

	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/session"
	novocab "github.com/dcalsky/novocab-go"
)

type Reason string

const (
	Manual    Reason = "manual"
	Threshold Reason = "threshold"
	Overflow  Reason = "overflow"
)

// TokenEstimator estimates the number of tokens occupied by one message.
// Implementations must return a non-negative value.
type TokenEstimator interface{ Estimate(message.Message) int64 }

// TokenEstimatorFunc adapts a function to TokenEstimator.
type TokenEstimatorFunc func(message.Message) int64

func (f TokenEstimatorFunc) Estimate(m message.Message) int64 { return f(m) }

// Estimator and EstimatorFunc preserve the original internal names.
type Estimator = TokenEstimator
type EstimatorFunc = TokenEstimatorFunc

// NovocabEstimator is the default vocabulary-free token estimator.
type NovocabEstimator struct{}

func (NovocabEstimator) Estimate(m message.Message) int64 {
	var text strings.Builder
	appendText := func(value string) {
		if value == "" {
			return
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(value)
	}
	for _, c := range m.Content {
		appendText(c.LLMText())
		appendText(c.Thinking)
		appendText(string(c.Arguments))
	}
	if text.Len() == 0 {
		return 1
	}
	// v0.2.0 replaces malformed UTF-8 before counting. The zero-value options
	// are valid, so the fallback only protects the no-error estimator contract.
	tokens, err := novocab.Estimate(text.String(), novocab.Options{})
	if err != nil || tokens < 1 {
		return 1
	}
	return tokens
}

// ApproximateEstimator is retained for source compatibility.
// Deprecated: use NovocabEstimator.
type ApproximateEstimator = NovocabEstimator

type Summarizer interface {
	Summarize(context.Context, []message.Message, string) (Summary, error)
}
type Summary struct {
	Text    string
	Usage   *message.Usage
	Details any
}
type Preparation struct {
	Reason           Reason
	Messages         []message.Message
	Kept             []message.Message
	FirstKeptEntryID session.EntryID
	TokensBefore     int64
	Instructions     string
	Cancel           bool
	Summary          string
	Details          any
}
type PrepareHook func(context.Context, *Preparation) error
type Options struct {
	ContextWindow, ReserveTokens, KeepRecentTokens int64
	Estimator                                      TokenEstimator
	Instructions                                   string
	Hook                                           PrepareHook
}
type Result struct {
	Summary          string
	FirstKeptEntryID session.EntryID
	TokensBefore     int64
	Usage            *message.Usage
	Details          any
}

var ErrNothingToCompact = errors.New("not enough context to compact")
var ErrCancelled = errors.New("compaction cancelled")

func Tokens(messages []message.Message, est TokenEstimator) int64 {
	if est == nil {
		est = NovocabEstimator{}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == message.RoleAssistant && messages[i].Usage.TotalTokens > 0 {
			return messages[i].Usage.TotalTokens
		}
	}
	var n int64
	for _, m := range messages {
		n += est.Estimate(m)
	}
	return n
}
func ShouldCompact(messages []message.Message, opts Options) bool {
	return opts.ContextWindow > 0 && Tokens(messages, opts.Estimator) > opts.ContextWindow-opts.ReserveTokens
}
func Prepare(entries []session.Entry, reason Reason, opts Options) (Preparation, error) {
	if len(entries) > 0 && entries[len(entries)-1].Type == "compaction" {
		return Preparation{}, ErrNothingToCompact
	}
	boundaryStart := 0
	var previousSummary string
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type != "compaction" {
			continue
		}
		previousSummary = entries[i].Summary
		boundaryStart = i + 1
		for j := 0; j < i; j++ {
			if entries[j].ID == entries[i].FirstKeptEntryID {
				boundaryStart = j
				break
			}
		}
		break
	}
	var ms []message.Message
	var ids []session.EntryID
	for _, e := range entries[boundaryStart:] {
		if e.Type == "message" && e.Message != nil {
			ms = append(ms, *e.Message)
			ids = append(ids, e.ID)
		}
	}
	if len(ms) < 2 {
		return Preparation{}, ErrNothingToCompact
	}
	total := Tokens(ms, opts.Estimator)
	keep := opts.KeepRecentTokens
	if keep <= 0 {
		keep = 16_000
	}
	est := opts.Estimator
	if est == nil {
		est = NovocabEstimator{}
	}
	sum := int64(0)
	cut := len(ms)
	for cut > 0 {
		next := est.Estimate(ms[cut-1])
		if sum+next > keep && cut < len(ms) {
			break
		}
		sum += next
		cut--
	}
	if cut <= 0 || cut >= len(ms) {
		return Preparation{}, ErrNothingToCompact
	}
	cut = completeToolBoundary(ms, cut)
	if cut <= 0 {
		return Preparation{}, ErrNothingToCompact
	}
	toSummarize := append([]message.Message(nil), ms[:cut]...)
	if previousSummary != "" {
		toSummarize = append([]message.Message{message.User(session.CompactionSummaryPrefix + previousSummary + session.CompactionSummarySuffix)}, toSummarize...)
	}
	return Preparation{Reason: reason, Messages: toSummarize, Kept: append([]message.Message(nil), ms[cut:]...), FirstKeptEntryID: ids[cut], TokensBefore: total, Instructions: opts.Instructions}, nil
}
func completeToolBoundary(ms []message.Message, cut int) int {
	if cut >= len(ms) || ms[cut].Role != message.RoleTool {
		return cut
	}
	wanted := ms[cut].ToolCallID
	for i := cut - 1; i >= 0; i-- {
		if ms[i].Role != message.RoleAssistant {
			continue
		}
		for _, c := range ms[i].Content {
			if c.Type == "toolCall" && c.ID == wanted {
				return i
			}
		}
	}
	return cut
}
func Run(ctx context.Context, manager *session.Manager, reason Reason, opts Options, s Summarizer) (Result, error) {
	p, err := Prepare(manager.CurrentBranch(), reason, opts)
	if err != nil {
		return Result{}, err
	}
	if opts.Hook != nil {
		if err = opts.Hook(ctx, &p); err != nil {
			return Result{}, err
		}
	}
	if p.Cancel {
		return Result{}, ErrCancelled
	}
	summary := Summary{Text: p.Summary, Details: p.Details}
	if summary.Text == "" {
		if s == nil {
			return Result{}, errors.New("summarizer is required")
		}
		summary, err = s.Summarize(ctx, p.Messages, p.Instructions)
		if err != nil {
			return Result{}, err
		}
	}
	details := summary.Details
	if details == nil {
		details = p.Details
	}
	var raw []byte
	if details != nil {
		raw, err = json.Marshal(details)
		if err != nil {
			return Result{}, fmt.Errorf("encode compaction details: %w", err)
		}
	}
	_, err = manager.AppendCompaction(summary.Text, p.FirstKeptEntryID, p.TokensBefore, raw, summary.Usage, p.Summary != "")
	if err != nil {
		return Result{}, err
	}
	return Result{Summary: summary.Text, FirstKeptEntryID: p.FirstKeptEntryID, TokensBefore: p.TokensBefore, Usage: summary.Usage, Details: details}, nil
}
