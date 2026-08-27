package main

import (
	"context"
	"fmt"

	"github.com/dcalsky/best-harness-go"
)

type params struct {
	Text string `json:"text"`
}
type details struct {
	Runes int `json:"runes"`
}

func main() {
	h, _ := harness.NewStateless(harness.Options{})
	_ = h.RegisterTool(harness.ToolSpec{Name: "count", Description: "Count runes in text."}, func(_ context.Context, _ harness.Context[harness.NoState], p params) (harness.ToolResult[details], error) {
		d := details{Runes: len([]rune(p.Text))}
		return harness.ToolResult[details]{Content: []harness.Content{harness.Text(fmt.Sprint(d.Runes))}, Details: d}, nil
	})
}
