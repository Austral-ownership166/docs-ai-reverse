// Package readme talks to ReadMe docs Ask AI (/{subdomain}/chatgpt/ask).
// Protocol matches docs-expert's ReadMe provider.
package readme

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultDocsURL = "https://docs.readme.com"
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
)

var (
	subdomainJSON = regexp.MustCompile(`"subdomain"\s*:\s*"([^"]+)"`)
	subdomainAttr = regexp.MustCompile(`data-subdomain="([^"]+)"`)
	subdomainEsc  = regexp.MustCompile(`subdomain&quot;:&quot;([^&]+)&quot;`)
)

// Client is a ReadMe Ask AI client.
type Client struct {
	http *http.Client
}

// NewClient constructs a ReadMe client. If httpClient is nil, a default client is used.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 90 * time.Second}
	}
	return &Client{http: httpClient}
}

// DetectSubdomain scrapes the docs HTML for the ReadMe subdomain.
func (c *Client) DetectSubdomain(ctx context.Context, docsURL string) (string, error) {
	docsURL = strings.TrimRight(docsURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("readme: fetch site status %d", resp.StatusCode)
	}
	html := string(body)
	for _, re := range []*regexp.Regexp{subdomainJSON, subdomainAttr, subdomainEsc} {
		if m := re.FindStringSubmatch(html); len(m) > 1 {
			return m[1], nil
		}
	}
	return "main", nil
}

// Ask posts a question and returns markdown content.
func (c *Client) Ask(ctx context.Context, docsURL, question string) (string, error) {
	docsURL = strings.TrimRight(docsURL, "/")
	if docsURL == "" {
		docsURL = DefaultDocsURL
	}
	subdomain, err := c.DetectSubdomain(ctx, docsURL)
	if err != nil {
		return "", err
	}
	apiURL := fmt.Sprintf("%s/%s/chatgpt/ask", docsURL, subdomain)
	conversationID := fmt.Sprintf("askAI-%s-%s", subdomain, uuid.NewString())
	messageID := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), uuid.NewString()[:7])

	payload, _ := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"id": messageID, "role": "user", "content": question},
		},
		"conversation_id": conversationID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("readme ask: status %d: %s", resp.StatusCode, truncate(body, 200))
	}
	return string(body), nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}
