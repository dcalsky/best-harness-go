package compact_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go"
	"github.com/dcalsky/best-harness-go/internal/compact"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/session"
	novocab "github.com/dcalsky/novocab-go"
)

type oneToken struct{}

func (oneToken) Estimate(message.Message) int64 { return 1 }

type summarizer struct{}

func (summarizer) Summarize(_ context.Context, ms []message.Message, _ string) (compact.Summary, error) {
	return compact.Summary{Text: "summary"}, nil
}

type captureSummarizer struct{ messages []message.Message }

func (s *captureSummarizer) Summarize(_ context.Context, messages []message.Message, _ string) (compact.Summary, error) {
	s.messages = append([]message.Message(nil), messages...)
	return compact.Summary{Text: "summary"}, nil
}

func TestNovocabEstimator(t *testing.T) {
	estimator := compact.NovocabEstimator{}
	if got := estimator.Estimate(message.User("hello world")); got != 3 {
		t.Fatalf("estimate=%d, want 3", got)
	}
	if got := estimator.Estimate(message.Message{}); got != 1 {
		t.Fatalf("empty estimate=%d, want 1", got)
	}
	invalid := message.User(string([]byte{'a', 0xff, 'b'}))
	if got := estimator.Estimate(invalid); got < 1 {
		t.Fatalf("invalid UTF-8 estimate=%d", got)
	}
}

func TestNovocabEstimatorIncludesImages(t *testing.T) {
	want, err := novocab.EstimateImageTokens(256, 512, novocab.ImageAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"png", "jpeg", "gif", "webp"} {
		t.Run(format, func(t *testing.T) {
			content := message.Image(testImageData(t, format), "image/"+format)
			got := (compact.NovocabEstimator{}).Estimate(message.Message{Content: []message.Content{content}})
			if got != want {
				t.Fatalf("estimate=%d, want %d", got, want)
			}
		})
	}
}

func TestNovocabEstimatorUsesConfiguredImageGeneration(t *testing.T) {
	data := testImageData(t, "png")
	imageMessage := message.Message{Content: []message.Content{message.Image(data, "image/png")}}
	got := (compact.NovocabEstimator{ImageGeneration: novocab.ImageOpenAI}).Estimate(imageMessage)
	want, err := novocab.EstimateImageTokens(256, 512, novocab.ImageOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("estimate=%d, want %d", got, want)
	}
}

func testImageData(t *testing.T, format string) string {
	t.Helper()
	if format == "webp" {
		// A minimal VP8X header is sufficient for DecodeConfig and keeps this
		// test independent of a WebP encoder.
		raw := []byte{
			'R', 'I', 'F', 'F', 22, 0, 0, 0, 'W', 'E', 'B', 'P',
			'V', 'P', '8', 'X', 10, 0, 0, 0, 1 << 4, 0, 0, 0,
			0xff, 0x00, 0x00, 0xff, 0x01, 0x00,
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	img := image.NewRGBA(image.Rect(0, 0, 256, 512))
	var out bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&out, img)
	case "jpeg":
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 90})
	case "gif":
		err = gif.Encode(&out, img, nil)
	default:
		t.Fatalf("unsupported test image format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out.Bytes())
}

func TestCustomTokenEstimator(t *testing.T) {
	var _ compact.TokenEstimator = compact.TokenEstimatorFunc(func(message.Message) int64 { return 7 })
	got := compact.Tokens([]message.Message{message.User("ignored")}, compact.TokenEstimatorFunc(func(message.Message) int64 { return 7 }))
	if got != 7 {
		t.Fatalf("custom estimate=%d, want 7", got)
	}
}

func TestToolBoundaryAndRun(t *testing.T) {
	m, _ := session.New(harness.NewMemoryPersistence(), session.Options{})
	_, _ = m.AppendMessage(message.User("old"))
	call := message.Message{Role: message.RoleAssistant, Content: []message.Content{message.ToolCall("c", "x", []byte(`{}`))}}
	_, _ = m.AppendMessage(call)
	kept, _ := m.AppendMessage(message.Message{Role: message.RoleTool, ToolCallID: "c", Content: []message.Content{message.Text("result")}})
	_, _ = m.AppendMessage(message.User("recent"))
	p, err := compact.Prepare(m.Entries(), compact.Manual, compact.Options{KeepRecentTokens: 2, Estimator: oneToken{}})
	if err != nil {
		t.Fatal(err)
	}
	if p.FirstKeptEntryID == kept {
		t.Fatal("cut separated a tool call from its result")
	}
	res, err := compact.Run(context.Background(), m, compact.Manual, compact.Options{KeepRecentTokens: 1, Estimator: oneToken{}}, summarizer{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "summary" {
		t.Fatalf("result=%#v", res)
	}
}

func TestRunOnlySummarizesCurrentBranch(t *testing.T) {
	m, err := session.New(harness.NewMemoryPersistence(), session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := m.AppendMessage(message.User("root"))
	_, _ = m.AppendMessage(message.User("abandoned-one"))
	_, _ = m.AppendMessage(message.User("abandoned-two"))
	if err = m.Navigate(&root); err != nil {
		t.Fatal(err)
	}
	_, _ = m.AppendMessage(message.User("active-one"))
	_, _ = m.AppendMessage(message.User("active-two"))
	_, _ = m.AppendMessage(message.User("active-three"))
	summarizer := &captureSummarizer{}
	if _, err = compact.Run(context.Background(), m, compact.Manual, compact.Options{KeepRecentTokens: 1, Estimator: oneToken{}}, summarizer); err != nil {
		t.Fatal(err)
	}
	for _, msg := range summarizer.messages {
		if strings.Contains(msg.Text(), "abandoned") {
			t.Fatalf("summarized inactive branch: %#v", summarizer.messages)
		}
	}
}
