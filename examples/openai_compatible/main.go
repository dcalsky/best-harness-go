package main

import (
	"os"

	"github.com/dcalsky/best-harness-go"
	openaisdk "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
)

func main() {
	selected := harness.Model{Provider: "openai", API: harness.APIOpenAI, ID: "model", ContextWindow: 128000, MaxOutput: 4096}
	models := harness.NewModelRegistry()
	_ = models.Register(selected)
	h, _ := harness.NewStateless(harness.Options{Models: models})
	client := openaisdk.NewClient(
		openaioption.WithAPIKey(os.Getenv("MODEL_API_KEY")),
		openaioption.WithBaseURL("http://127.0.0.1:8000/v1"),
	)
	_ = h.RegisterProvider("openai", harness.NewOpenAIProvider(client))
}
