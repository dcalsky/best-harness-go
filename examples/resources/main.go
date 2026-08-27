package main

import (
	"context"
	"fmt"

	"github.com/dcalsky/best-harness-go"
)

func main() {
	registry := harness.NewResourceRegistry()
	registry.Register(harness.ProgramResourceLoader{Snapshot: harness.ResourceSnapshot{ProjectInstructions: []harness.ResourceSource{{Name: "project", Path: "memory:project", Content: "Run package tests after edits."}}}})
	snapshot, _ := registry.Load(context.Background(), harness.ResourceLoadRequest{Cwd: "/work"})
	fmt.Println(harness.BuildSystemPrompt(harness.ResourcePromptOptions{Cwd: "/work", Snapshot: snapshot}))
}
