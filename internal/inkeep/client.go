// Package inkeep talks to Inkeep docs AI (api.inkeep.com) with challenge-response auth.
// Protocol matches docs-expert's Claude/Anthropic provider.
package inkeep

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	BaseURL     = "https://api.inkeep.com"
	DefaultModel = "inkeep-qa-expert"
	// Hardcoded Anthropic docs widget key (same as docs-expert).
	ClaudeAPIKey = "338b6cdd7488066de9b9dc40e996d96b11488d29ef05b56d"
	userAgent    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
)

// Config holds Inkeep site/auth parameters.
type Config struct {
	APIKey string
	Origin string
	Referer string
}

// DefaultClaudeConfig returns Anthropic/Claude docs Inkeep settings.
func DefaultClaudeConfig() Config {
	return Config{
		APIKey:  ClaudeAPIKey,
		Origin:  "https://platform.claude.com",
		Referer: "https://platform.claude.com/",
	}
}

type challengeData struct {
	Challenge string `json:"challenge"`
	Salt      string `json:"salt"`
	MaxNumber int    `json:"maxnumber"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

// SolveChallenge brute-forces the Inkeep SHA-256 challenge (docs-expert compatible).
func SolveChallenge(data challengeData) (string, error) {
	if data.Algorithm != "" && data.Algorithm != "SHA-256" {
		return "", fmt.Errorf("inkeep: unsupported algorithm %q", data.Algorithm)
	}
	for n := 0; n <= data.MaxNumber; n++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s%d", data.Salt, n)))
		if hex.EncodeToString(sum[:]) == data.Challenge {
			sol := map[string]any{
				"number":    n,
				"algorithm": "SHA-256",
				"challenge": data.Challenge,
				"maxnumber": data.MaxNumber,
				"salt":      data.Salt,
				"signature": data.Signature,
			}
			b, _ := json.Marshal(sol)
			return base64.StdEncoding.EncodeToString(b), nil
		}
	}
	return "", fmt.Errorf("inkeep: challenge not solved within maxnumber=%d", data.MaxNumber)
}

// Client is an Inkeep HTTP client.
type Client struct {
	http *http.Client
	cfg  Config
}

// NewClient constructs an Inkeep client. If httpClient is nil, a default client is used.
func NewClient(cfg Config, httpClient *http.Client) *Client {
	if strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = ClaudeAPIKey
	}
	if cfg.Origin == "" {
		cfg.Origin = "https://platform.claude.com"
	}
	if cfg.Referer == "" {
		cfg.Referer = cfg.Origin + "/"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{
		http: httpClient,
		cfg:  cfg,
	}
}

func (c *Client) FetchChallenge(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/v1/challenge", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Origin", c.cfg.Origin)
	req.Header.Set("Referer", c.cfg.Referer)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("inkeep challenge: status %d: %s", resp.StatusCode, truncate(body, 200))
	}
	var data challengeData
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("inkeep challenge decode: %w", err)
	}
	return SolveChallenge(data)
}

func (c *Client) authHeaders(solution string) http.Header {
	h := make(http.Header)
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", userAgent)
	h.Set("Origin", c.cfg.Origin)
	h.Set("Referer", c.cfg.Referer)
	h.Set("Authorization", "Bearer "+c.cfg.APIKey)
	h.Set("X-Inkeep-Challenge-Solution", solution)
	return h
}

// ChatCompletions posts an OpenAI-compatible chat body and returns the raw response body.
func (c *Client) ChatCompletions(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	solution, err := c.FetchChallenge(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = c.authHeaders(solution)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("inkeep chat: status %d: %s", resp.StatusCode, truncate(b, 300))
	}
	return resp, nil
}

// ProvideLinksTool is the docs-expert default tool schema for citation links.
func ProvideLinksTool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "provideLinks",
			"description": "Provides links",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"links": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":       map[string]any{"type": []any{"string", "null"}},
										"url":         map[string]any{"type": "string"},
										"title":       map[string]any{"type": []any{"string", "null"}},
										"description": map[string]any{"type": []any{"string", "null"}},
									},
									"required":             []string{"url"},
									"additionalProperties": true,
								},
							},
							map[string]any{"type": "null"},
						},
					},
					"text": map[string]any{"type": "string"},
				},
				"required":             []string{"text"},
				"additionalProperties": false,
			},
		},
	}
}

// NewMessageID returns a docs-expert-style message id.
func NewMessageID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%x-1", time.Now().UnixMilli(), b)
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}
