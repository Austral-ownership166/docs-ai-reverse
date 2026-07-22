package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"claude-code-chat/internal/mintlify"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const providerKey = "mintlify"

// Executor implements auth.ProviderExecutor for Mintlify docs assistant.
type Executor struct {
	mu       sync.Mutex
	clients  map[string]*mintlify.Client
	sessions map[string]*mintlify.Session
	tokens   map[string]tokenCache
}

type tokenCache struct {
	token     string
	expiresAt time.Time
}

// NewExecutor constructs a mintlify executor.
func NewExecutor() *Executor {
	return &Executor{
		clients:  make(map[string]*mintlify.Client),
		sessions: make(map[string]*mintlify.Session),
		tokens:   make(map[string]tokenCache),
	}
}

// Identifier returns the provider key.
func (e *Executor) Identifier() string { return providerKey }

// RequestToFormat reports the upstream request format (mintlify).
func (e *Executor) RequestToFormat(_ clipexec.Request, _ clipexec.Options) sdktranslator.Format {
	return formatMintlify
}

func (e *Executor) authID(a *coreauth.Auth) string {
	if a == nil {
		return "default"
	}
	if id := strings.TrimSpace(a.ID); id != "" {
		return id
	}
	return "default"
}

func siteFromAuth(a *coreauth.Auth) mintlify.SiteConfig {
	site := mintlify.DefaultSiteConfig()
	if a == nil || a.Attributes == nil {
		return site
	}
	if v := strings.TrimSpace(a.Attributes["subdomain"]); v != "" {
		site.Subdomain = v
	}
	if v := strings.TrimSpace(a.Attributes["site_origin"]); v != "" {
		site.SiteOrigin = v
	}
	if v := strings.TrimSpace(a.Attributes["docs_path"]); v != "" {
		if !strings.HasPrefix(v, "/") {
			v = "/" + v
		}
		site.DocsPath = v
	}
	if v := strings.TrimSpace(a.Attributes["language"]); v != "" {
		site.Language = v
	}
	return site
}

func (e *Executor) clientFor(authID string) (*mintlify.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := e.clients[authID]; c != nil {
		return c, nil
	}
	c, err := mintlify.NewClient()
	if err != nil {
		return nil, err
	}
	e.clients[authID] = c
	return c, nil
}

func (e *Executor) sessionFor(authID string) *mintlify.Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s := e.sessions[authID]; s != nil {
		return s
	}
	s := &mintlify.Session{}
	e.sessions[authID] = s
	return s
}

func (e *Executor) updateSession(authID string, s mintlify.Session) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.sessions[authID]
	if cur == nil {
		cur = &mintlify.Session{}
		e.sessions[authID] = cur
	}
	if s.ThreadID != "" {
		cur.ThreadID = s.ThreadID
	}
	if s.ThreadKey != "" {
		cur.ThreadKey = s.ThreadKey
	}
	if s.MessageID != "" {
		cur.MessageID = s.MessageID
	}
}

func (e *Executor) getToken(site mintlify.SiteConfig, authID string) (string, error) {
	e.mu.Lock()
	cached, ok := e.tokens[authID]
	e.mu.Unlock()
	if ok && cached.token != "" && time.Now().Before(cached.expiresAt.Add(-60*time.Second)) {
		return cached.token, nil
	}
	tok, err := mintlify.FetchToken(site.SiteOrigin)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(30 * time.Minute)
	if claims, err := mintlify.DecodeTokenPayload(tok); err == nil {
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			expires = time.Unix(int64(exp), 0)
		}
	}
	e.mu.Lock()
	e.tokens[authID] = tokenCache{token: tok, expiresAt: expires}
	e.mu.Unlock()
	return tok, nil
}

func (e *Executor) invalidateToken(authID string) {
	e.mu.Lock()
	delete(e.tokens, authID)
	e.mu.Unlock()
}

func prepareUpstreamBody(translated []byte, site mintlify.SiteConfig, token string, session *mintlify.Session) ([]byte, error) {
	body := translated
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("mintlify: invalid translated request")
	}
	body, _ = sjson.SetBytes(body, "id", site.Subdomain)
	body, _ = sjson.SetBytes(body, "fp", site.Subdomain)
	body, _ = sjson.SetBytes(body, "filter.language", site.Language)
	body, _ = sjson.SetBytes(body, "currentPath", site.DocsPath)
	body, _ = sjson.SetBytes(body, "_", token)
	if session != nil {
		if session.ThreadID != "" {
			body, _ = sjson.SetBytes(body, "threadId", session.ThreadID)
		}
		if session.ThreadKey != "" {
			body, _ = sjson.SetBytes(body, "threadKey", session.ThreadKey)
		}
	}
	// Ensure messages exist
	if !gjson.GetBytes(body, "messages").Exists() {
		return nil, fmt.Errorf("mintlify: request missing messages")
	}
	return body, nil
}

// Execute handles non-streaming OpenAI-compatible requests.
func (e *Executor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	authID := e.authID(a)
	site := siteFromAuth(a)
	from := opts.SourceFormat
	to := formatMintlify
	responseFormat := clipexec.ResponseFormatOrSource(opts)

	translated := sdktranslator.TranslateRequest(from, to, req.Model, req.Payload, opts.Stream)

	token, err := e.getToken(site, authID)
	if err != nil {
		return clipexec.Response{}, err
	}
	session := e.sessionFor(authID)
	body, err := prepareUpstreamBody(translated, site, token, session)
	if err != nil {
		return clipexec.Response{}, err
	}

	client, err := e.clientFor(authID)
	if err != nil {
		return clipexec.Response{}, err
	}
	cookie, err := mintlify.LoadCookies()
	if err != nil {
		return clipexec.Response{}, err
	}

	stream, err := client.SendStream(ctx, site, cookie, body)
	if err != nil {
		if strings.Contains(err.Error(), "419") {
			e.invalidateToken(authID)
		}
		return clipexec.Response{}, err
	}
	defer stream.Close()
	e.updateSession(authID, stream.Session)

	data, err := io.ReadAll(stream.Body)
	if err != nil {
		return clipexec.Response{}, err
	}

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, data, &param)
	return clipexec.Response{Payload: out}, nil
}

// ExecuteStream streams Mintlify line chunks through translators.
func (e *Executor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	authID := e.authID(a)
	site := siteFromAuth(a)
	from := opts.SourceFormat
	to := formatMintlify
	responseFormat := clipexec.ResponseFormatOrSource(opts)

	translated := sdktranslator.TranslateRequest(from, to, req.Model, req.Payload, true)

	token, err := e.getToken(site, authID)
	if err != nil {
		return nil, err
	}
	session := e.sessionFor(authID)
	body, err := prepareUpstreamBody(translated, site, token, session)
	if err != nil {
		return nil, err
	}

	client, err := e.clientFor(authID)
	if err != nil {
		return nil, err
	}
	cookie, err := mintlify.LoadCookies()
	if err != nil {
		return nil, err
	}

	stream, err := client.SendStream(ctx, site, cookie, body)
	if err != nil {
		if strings.Contains(err.Error(), "419") {
			e.invalidateToken(authID)
		}
		return nil, err
	}
	e.updateSession(authID, stream.Session)

	out := make(chan clipexec.StreamChunk)
	go func() {
		defer close(out)
		defer stream.Close()

		scanner := bufio.NewScanner(stream.Body)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		var param any
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, line, &param)
			for i := range chunks {
				select {
				case out <- clipexec.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			select {
			case out <- clipexec.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		// Sentinel flushes finish / completed events from translators.
		chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, []byte(streamDoneSentinel), &param)
		for i := range chunks {
			select {
			case out <- clipexec.StreamChunk{Payload: chunks[i]}:
			case <-ctx.Done():
				return
			}
		}
	}()

	headers := http.Header{}
	if stream.Header != nil {
		for k, vals := range stream.Header {
			for _, v := range vals {
				headers.Add(k, v)
			}
		}
	}
	return &clipexec.StreamResult{Headers: headers, Chunks: out}, nil
}

// CountTokens estimates prompt tokens locally (Mintlify has no count API).
// Uses tiktoken o200k_base; advertised model context is maxContextTokens (200k).
func (e *Executor) CountTokens(ctx context.Context, _ *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	from := formatMintlify
	responseFormat := clipexec.ResponseFormatOrSource(opts)

	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	count, err := estimatePromptTokens(payload)
	if err != nil {
		return clipexec.Response{}, err
	}
	if count > maxContextTokens {
		count = maxContextTokens
	}

	usageJSON := buildOpenAIUsageJSON(count)
	out := sdktranslator.TranslateTokenCount(ctx, from, responseFormat, count, usageJSON)
	return clipexec.Response{Payload: out}, nil
}

// HttpRequest is not supported (Mintlify uses tls-client, not net/http).
func (e *Executor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("mintlify: HttpRequest not supported")
}

// Refresh is a no-op; tokens are refreshed lazily on demand.
func (e *Executor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
