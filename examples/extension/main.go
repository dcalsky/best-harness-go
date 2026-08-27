package main

import (
	"context"

	"github.com/dcalsky/best-harness-go"
)

type ext struct{}

func (ext) Register(r *harness.ExtensionRegistry[harness.NoState]) error {
	r.AddInputHook(func(_ context.Context, _ harness.Context[harness.NoState], m harness.Message) (harness.Message, error) { return m, nil })
	return nil
}
func main() {
	h, _ := harness.NewStateless(harness.Options{})
	_ = h.RegisterExtension(ext{})
}
