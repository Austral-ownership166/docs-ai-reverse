// Package stripeai talks to Stripe docs AI (ai.stripe.com) via create-thread + poll.
// Protocol matches docs-expert's Stripe provider.
package stripeai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	baseURL  = "https://ai.stripe.com"
	docsURL  = "https://docs.stripe.com"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
)

// Client is a Stripe docs AI client.
type Client struct {
	http *http.Client
}

// NewClient constructs a Stripe AI client. If httpClient is nil, a default client is used.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{http: httpClient}
}

type threadResponse struct {
	ThreadID       string `json:"thread_id"`
	ConversationID string `json:"conversation_id"`
	Answerable     *bool  `json:"answerable"`
	Sources        []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"sources"`
}

type pollResponse struct {
	Content    string `json:"content"`
	IsComplete bool   `json:"is_complete"`
}

func (c *Client) post(ctx context.Context, url string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", docsURL)
	req.Header.Set("Referer", docsURL+"/")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("stripe ai: status %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) createThread(ctx context.Context, question, clientID string) (*threadResponse, error) {
	pageContent := ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docsURL+"/.md", nil)
	if err == nil {
		req.Header.Set("User-Agent", userAgent)
		if resp, errDo := c.http.Do(req); errDo == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				pageContent = string(b)
			}
		}
	}

	var out threadResponse
	err = c.post(ctx, baseURL+"/assistant/thread", map[string]any{
		"question":         question,
		"message_metadata": map[string]any{"question_type": "chat"},
		"client":           "docs",
		"client_id":        clientID,
		"question_metadata": map[string]any{
			"stripe_doc": map[string]any{
				"url":     "/",
				"title":   "Stripe Documentation",
				"prefs":   map[string]any{},
				"content": pageContent,
			},
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Ask returns the full answer text for a question (polling).
func (c *Client) Ask(ctx context.Context, question string) (content string, threadID string, err error) {
	clientID := uuid.NewString()
	thread, err := c.createThread(ctx, question, clientID)
	if err != nil {
		return "", "", err
	}
	if thread.Answerable != nil && !*thread.Answerable {
		return "The AI assistant determined this question is not answerable from Stripe docs.", thread.ThreadID, nil
	}

	var parts []string
	offset := 0
	for i := 0; i < 120; i++ {
		var poll pollResponse
		if err := c.post(ctx, baseURL+"/smart-docs/get-streaming-ask-summary-state", map[string]any{
			"conversation_id": thread.ConversationID,
			"offset":          offset,
			"client":          "docs",
			"client_id":       clientID,
		}, &poll); err != nil {
			return strings.Join(parts, ""), thread.ThreadID, err
		}
		if poll.Content != "" {
			parts = append(parts, poll.Content)
			offset += len(poll.Content)
		}
		if poll.IsComplete {
			break
		}
		select {
		case <-ctx.Done():
			return strings.Join(parts, ""), thread.ThreadID, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return strings.Join(parts, ""), thread.ThreadID, nil
}

// StreamAsk yields incremental content chunks via a channel.
func (c *Client) StreamAsk(ctx context.Context, question string) (<-chan string, string, error) {
	clientID := uuid.NewString()
	thread, err := c.createThread(ctx, question, clientID)
	if err != nil {
		return nil, "", err
	}
	out := make(chan string, 8)
	go func() {
		defer close(out)
		if thread.Answerable != nil && !*thread.Answerable {
			select {
			case out <- "The AI assistant determined this question is not answerable from Stripe docs.":
			case <-ctx.Done():
			}
			return
		}
		offset := 0
		for i := 0; i < 120; i++ {
			var poll pollResponse
			if err := c.post(ctx, baseURL+"/smart-docs/get-streaming-ask-summary-state", map[string]any{
				"conversation_id": thread.ConversationID,
				"offset":          offset,
				"client":          "docs",
				"client_id":       clientID,
			}, &poll); err != nil {
				return
			}
			if poll.Content != "" {
				offset += len(poll.Content)
				select {
				case out <- poll.Content:
				case <-ctx.Done():
					return
				}
			}
			if poll.IsComplete {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()
	return out, thread.ThreadID, nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}
