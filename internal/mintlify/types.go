package mintlify

import "time"

// Message is a Mintlify assistant chat message.
type Message struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Parts     []Part    `json:"parts"`
}

// Part is a text part of a Mintlify message.
type Part struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// MessageRequest is the Mintlify assistant /message request body.
type MessageRequest struct {
	ID           string    `json:"id"`
	Messages     []Message `json:"messages"`
	FP           string    `json:"fp"`
	Filter       Filter    `json:"filter"`
	CurrentPath  string    `json:"currentPath"`
	Token        string    `json:"_,omitempty"`
	CaptchaToken string    `json:"captchaToken,omitempty"`
	ThreadID     string    `json:"threadId,omitempty"`
	ThreadKey    string    `json:"threadKey,omitempty"`
	Regenerate   bool      `json:"regenerate,omitempty"`
}

// Filter selects docs language.
type Filter struct {
	Language string `json:"language"`
}

// Chunk is one Mintlify stream line (`T:value`).
type Chunk struct {
	Type  string
	Value string
}

// Session holds thread continuity across requests.
type Session struct {
	ThreadID  string
	ThreadKey string
	MessageID string
}

// SiteConfig holds deploy-time Mintlify site parameters.
type SiteConfig struct {
	Subdomain  string
	SiteOrigin string
	DocsPath   string
	Language   string
}

// DefaultSiteConfig returns Claude Code docs defaults.
func DefaultSiteConfig() SiteConfig {
	return SiteConfig{
		Subdomain:  "claude-code",
		SiteOrigin: "https://code.claude.com",
		DocsPath:   "/en/quickstart",
		Language:   "en",
	}
}
