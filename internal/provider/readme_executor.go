package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claude-code-chat/internal/readme"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const readmeProviderKey = "readme"

// ReadmeExecutor implements auth.ProviderExecutor for ReadMe Ask AI.
type ReadmeExecutor struct {
	proxyDefaults
}

// NewReadmeExecutor constructs a ReadMe docs executor.
func NewReadmeExecutor() *ReadmeExecutor {
	return &ReadmeExecutor{}
}

func (e *ReadmeExecutor) Identifier() string { return readmeProviderKey }

func (e *ReadmeExecutor) RequestToFormat(_ clipexec.Request, _ clipexec.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

func docsURLFromAuth(a *coreauth.Auth) string {
	if a != nil && a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["docs_url"]); v != "" {
			return v
		}
	}
	return readme.DefaultDocsURL
}

func (e *ReadmeExecutor) clientFor(a *coreauth.Auth) *readme.Client {
	return readme.NewClient(httpClientForProxy(e.resolveProxy(a), 90*time.Second))
}

func (e *ReadmeExecutor) questionFrom(req clipexec.Request, opts clipexec.Options) string {
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	q := lastUserQuestion(payload)
	return questionWithOptionalTools(payload, q)
}

func (e *ReadmeExecutor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	q := e.questionFrom(req, opts)
	if strings.TrimSpace(q) == "" {
		return clipexec.Response{}, errors.New("readme: empty user question")
	}
	content, err := e.clientFor(a).Ask(ctx, docsURLFromAuth(a), q)
	if err != nil {
		return clipexec.Response{}, err
	}
	responseFormat := clipexec.ResponseFormatOrSource(opts)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		return clipexec.Response{Payload: buildResponsesOutput(req.Model, content)}, nil
	}
	return clipexec.Response{Payload: buildChatCompletion(req.Model, content)}, nil
}

func (e *ReadmeExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	q := e.questionFrom(req, opts)
	if strings.TrimSpace(q) == "" {
		return nil, errors.New("readme: empty user question")
	}
	content, err := e.clientFor(a).Ask(ctx, docsURLFromAuth(a), q)
	if err != nil {
		return nil, err
	}
	responseFormat := clipexec.ResponseFormatOrSource(opts)
	out := make(chan clipexec.StreamChunk)
	go func() {
		defer close(out)
		if responseFormat == sdktranslator.FormatOpenAIResponse {
			select {
			case out <- clipexec.StreamChunk{Payload: buildResponsesOutput(req.Model, content)}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case out <- clipexec.StreamChunk{Payload: buildChatChunk(req.Model, "", nil)}:
		case <-ctx.Done():
			return
		}
		select {
		case out <- clipexec.StreamChunk{Payload: buildChatChunk(req.Model, content, nil)}:
		case <-ctx.Done():
			return
		}
		stop := "stop"
		select {
		case out <- clipexec.StreamChunk{Payload: buildChatChunk(req.Model, "", &stop)}:
		case <-ctx.Done():
		}
	}()
	return &clipexec.StreamResult{Chunks: out}, nil
}

func (e *ReadmeExecutor) CountTokens(ctx context.Context, _ *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	return countTokensResponse(ctx, sdktranslator.FormatOpenAI, req, opts)
}

func (e *ReadmeExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("readme: HttpRequest not supported")
}

func (e *ReadmeExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
