package main

import (
	"context"
	"fmt"

	"github.com/dcalsky/best-harness-go"
)

func main() {
	ctx := context.Background()
	selected := harness.Model{Provider: "faux", ID: "test"}
	models := harness.NewModelRegistry()
	_ = models.Register(selected)
	fake := &harness.Faux{StreamFunc: func(context.Context, harness.Request) (harness.Stream, error) {
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventTextDelta, Text: "hello"}, {Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	h, _ := harness.NewStateless(harness.Options{Models: models})
	_ = h.RegisterProvider("faux", fake)
	s, _ := h.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	defer s.Close()
	r, _ := s.Start(ctx, harness.Prompt{Steps: harness.Sequence{harness.UserText("Say hello.")}}, harness.StartOptions{})
	_ = r.Wait(ctx)
	fmt.Println(s.Conversation().Messages[1].Text())
}
