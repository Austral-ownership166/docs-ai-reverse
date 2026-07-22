package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claude-code-chat/internal/stripeai"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const stripeProviderKey = "stripe"

// StripeExecutor implements auth.ProviderExecutor for Stripe docs AI.
type StripeExecutor struct {
	proxyDefaults
}

// NewStripeExecutor constructs a Stripe docs executor.
func NewStripeExecutor() *StripeExecutor {
	return &StripeExecutor{}
}

func (e *StripeExecutor) Identifier() string { return stripeProviderKey }

func (e *StripeExecutor) RequestToFormat(_ clipexec.Request, _ clipexec.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

func (e *StripeExecutor) clientFor(a *coreauth.Auth) *stripeai.Client {
	return stripeai.NewClient(httpClientForProxy(e.resolveProxy(a), 60*time.Second))
}

func (e *StripeExecutor) questionFrom(req clipexec.Request, opts clipexec.Options) string {
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	q := lastUserQuestion(payload)
	return questionWithOptionalTools(payload, q)
}

func (e *StripeExecutor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	q := e.questionFrom(req, opts)
	if strings.TrimSpace(q) == "" {
		return clipexec.Response{}, errors.New("stripe: empty user question")
	}
	content, _, err := e.clientFor(a).Ask(ctx, q)
	if err != nil {
		return clipexec.Response{}, err
	}
	responseFormat := clipexec.ResponseFormatOrSource(opts)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		return clipexec.Response{Payload: buildResponsesOutput(req.Model, content)}, nil
	}
	return clipexec.Response{Payload: buildChatCompletion(req.Model, content)}, nil
}

func (e *StripeExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	q := e.questionFrom(req, opts)
	if strings.TrimSpace(q) == "" {
		return nil, errors.New("stripe: empty user question")
	}
	chunks, _, err := e.clientFor(a).StreamAsk(ctx, q)
	if err != nil {
		return nil, err
	}
	responseFormat := clipexec.ResponseFormatOrSource(opts)
	out := make(chan clipexec.StreamChunk)
	go func() {
		defer close(out)
		if responseFormat == sdktranslator.FormatOpenAIResponse {
			var parts []string
			for c := range chunks {
				parts = append(parts, c)
			}
			select {
			case out <- clipexec.StreamChunk{Payload: buildResponsesOutput(req.Model, strings.Join(parts, ""))}:
			case <-ctx.Done():
			}
			return
		}
		// role chunk
		select {
		case out <- clipexec.StreamChunk{Payload: buildChatChunk(req.Model, "", nil)}:
		case <-ctx.Done():
			return
		}
		for c := range chunks {
			select {
			case out <- clipexec.StreamChunk{Payload: buildChatChunk(req.Model, c, nil)}:
			case <-ctx.Done():
				return
			}
		}
		stop := "stop"
		select {
		case out <- clipexec.StreamChunk{Payload: buildChatChunk(req.Model, "", &stop)}:
		case <-ctx.Done():
		}
	}()
	return &clipexec.StreamResult{Chunks: out}, nil
}

func (e *StripeExecutor) CountTokens(ctx context.Context, _ *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	return countTokensResponse(ctx, sdktranslator.FormatOpenAI, req, opts)
}

func (e *StripeExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("stripe: HttpRequest not supported")
}

func (e *StripeExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
