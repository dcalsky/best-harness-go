package vercelai

import (
	"errors"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"io"
	"net/http"
)

// SetHeaders applies the headers required by Vercel AI SDK's default chat
// transport for a UI message stream.
func SetHeaders(header http.Header) {
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Vercel-AI-UI-Message-Stream", "v1")
	header.Set("X-Accel-Buffering", "no")
}

// SSEEncoder serializes Chunks as Server-Sent Events and writes the terminal
// [DONE] marker expected by DefaultChatTransport.
type SSEEncoder struct {
	w      io.Writer
	closed bool
}

func NewSSEEncoder(w io.Writer) *SSEEncoder { return &SSEEncoder{w: w} }

func (e *SSEEncoder) Encode(chunk Chunk) error {
	if e.closed {
		return errors.New("Vercel AI SDK SSE encoder is closed")
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(e.w, "data: "); err != nil {
		return err
	}
	if _, err := e.w.Write(data); err != nil {
		return err
	}
	_, err = io.WriteString(e.w, "\n\n")
	return err
}

func (e *SSEEncoder) EncodeAll(chunks []Chunk) error {
	for _, chunk := range chunks {
		if err := e.Encode(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (e *SSEEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	_, err := io.WriteString(e.w, "data: [DONE]\n\n")
	return err
}
