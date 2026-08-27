// Package openai adapts the official OpenAI Go SDK to the provider-neutral
// harness boundary. A single provider supports both Chat Completions and the
// Responses API, selected by model.API.
package openai

import (
	"context"
	"errors"
	"fmt"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/provider"
	"github.com/dcalsky/best-harness-go/internal/provider/internal/adapterutil"
)

type Provider struct {
	client     openaisdk.Client
	defaultAPI model.API
}

func New(client openaisdk.Client) *Provider {
	return &Provider{client: client, defaultAPI: model.APIOpenAI}
}

func NewResponses(client openaisdk.Client) *Provider {
	return &Provider{client: client, defaultAPI: model.APIOpenAIResponses}
}

var _ provider.Provider = (*Provider)(nil)

func (p *Provider) Stream(ctx context.Context, in provider.Request) (provider.Stream, error) {
	api := in.Model.API
	if api == "" {
		api = p.defaultAPI
	}
	in.Model.API = api
	switch api {
	case model.APIOpenAI:
		return p.streamChat(ctx, in)
	case model.APIOpenAIResponses:
		return p.streamResponses(ctx, in)
	default:
		return nil, fmt.Errorf("OpenAI SDK provider does not support model API %q", api)
	}
}

func convertError(providerName string, err error) error {
	if err == nil {
		return nil
	}
	var apiError *openaisdk.Error
	if !errors.As(err, &apiError) {
		return err
	}
	messageText := apiError.Message
	if messageText == "" {
		messageText = err.Error()
	}
	return &message.ProviderError{
		Provider: providerName, StatusCode: apiError.StatusCode,
		Code: apiError.Code, Message: messageText,
		Retryable: adapterutil.RetryableStatus(apiError.StatusCode),
		Cause: adapterutil.ErrorCause(
			apiError.StatusCode, apiError.Code, messageText, err,
		),
	}
}
