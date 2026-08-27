package agui

import (
	"errors"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"io"
	"net/http"
)

// SetHeaders applies the standard headers for AG-UI over Server-Sent Events.
func SetHeaders(header http.Header) {
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
}

// SSEEncoder writes one AG-UI JSON event per SSE data frame. AG-UI streams end
// when the connection closes and do not use a protocol-specific [DONE] frame.
type SSEEncoder struct {
	w      io.Writer
	closed bool
}

func NewSSEEncoder(w io.Writer) *SSEEncoder { return &SSEEncoder{w: w} }

func (e *SSEEncoder) Encode(event Event) error {
	if e.closed {
		return errors.New("AG-UI SSE encoder is closed")
	}
	if event == nil {
		return errors.New("AG-UI event is required")
	}
	if event.Kind() == "" {
		return errors.New("AG-UI event type is required")
	}
	data, err := json.Marshal(event)
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

func (e *SSEEncoder) EncodeAll(events []Event) error {
	for _, event := range events {
		if err := e.Encode(event); err != nil {
			return err
		}
	}
	return nil
}

func (e *SSEEncoder) Close() error {
	e.closed = true
	return nil
}
