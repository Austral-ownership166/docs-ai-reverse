package mintlify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/steipete/sweetcookie"
)

const (
	aiMessageHost = "https://leaves.mintlify.com"
	// Match docs-expert + current Chrome docs widget UA range.
	chromeUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// Upstream status meanings from Mintlify docs frontend (status → type/message).
const (
	errMsg402 = "Usage of this feature is temporarily limited (402 payment/quota). Please try again later."
	errMsg418 = "The assistant is unavailable from your network (418). Mintlify blocks many VPN/datacenter egress IPs — disable proxy-url or use a clean residential exit."
	errMsg420 = "Your request could not be verified (420 CF bot). Refresh cookies via Chrome or warm cookies on the same egress IP."
)

var cookieFilePath = filepath.Join(os.Getenv("HOME"), ".claude-code-cookies.json")

type cookieFile struct {
	CookieString string `json:"cookie_string"`
	Expires      int64  `json:"expires"`
	Domain       string `json:"domain"`
}

// GenerateID returns a random hex id.
func GenerateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func newTLSClient(proxyURL string) (tls_client.HttpClient, error) {
	// Chrome_146（HTTP/2）可绕过 CF；ForceHttp1 反而会 420。
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithClientProfile(profiles.Chrome_146),
		tls_client.WithNotFollowRedirects(),
	}
	if proxyURL = strings.TrimSpace(proxyURL); proxyURL != "" {
		opts = append(opts, tls_client.WithProxyUrl(proxyURL))
	}
	return tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
}

func buildCookieString(cfBm, awsalb, awsalbCors string) (string, error) {
	if cfBm == "" {
		return "", fmt.Errorf("未找到 mintlify.com __cf_bm cookie")
	}
	var cookies []string
	cookies = append(cookies, "__cf_bm="+cfBm)
	if awsalb != "" {
		cookies = append(cookies, "AWSALB="+awsalb)
	}
	if awsalbCors != "" {
		cookies = append(cookies, "AWSALBCORS="+awsalbCors)
	}
	return strings.Join(cookies, "; "), nil
}

func pickChromeCookies(cookies []sweetcookie.Cookie, grace time.Duration) (cfBm, awsalb, awsalbCors string) {
	cutoff := time.Now().Add(-grace)
	for _, c := range cookies {
		if c.Expires != nil && c.Expires.Before(cutoff) {
			continue
		}
		switch {
		case strings.Contains(c.Domain, "mintlify") && c.Name == "__cf_bm":
			cfBm = c.Value
		case c.Domain == "leaves.mintlify.com" && c.Name == "AWSALB":
			awsalb = c.Value
		case c.Domain == "leaves.mintlify.com" && c.Name == "AWSALBCORS":
			awsalbCors = c.Value
		}
	}
	return
}

// ExtractCookiesFromChrome reads mintlify CF cookies from local Chromium browsers.
func ExtractCookiesFromChrome() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	opts := sweetcookie.Options{
		URL: "https://leaves.mintlify.com/",
		Origins: []string{
			"https://leaves.mintlify.com/",
			"https://mintlify.com/",
		},
		Names: []string{"__cf_bm", "AWSALB", "AWSALBCORS"},
		Browsers: []sweetcookie.Browser{
			sweetcookie.BrowserChrome,
			sweetcookie.BrowserChromium,
			sweetcookie.BrowserEdge,
			sweetcookie.BrowserBrave,
			sweetcookie.BrowserArc,
		},
		Mode:    sweetcookie.ModeMerge,
		Timeout: 30 * time.Second,
	}

	res, err := sweetcookie.Get(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("sweetcookie 读取失败: %w", err)
	}

	cfBm, awsalb, awsalbCors := pickChromeCookies(res.Cookies, 0)
	if cfBm == "" {
		// __cf_bm 寿命约 30min；允许刚过期数分钟内仍尝试（CF 侧偶发仍接受）
		opts.IncludeExpired = true
		res, err = sweetcookie.Get(ctx, opts)
		if err != nil {
			return "", fmt.Errorf("sweetcookie 读取失败: %w", err)
		}
		cfBm, awsalb, awsalbCors = pickChromeCookies(res.Cookies, 10*time.Minute)
	}

	cookieStr, err := buildCookieString(cfBm, awsalb, awsalbCors)
	if err != nil {
		if len(res.Warnings) > 0 {
			return "", fmt.Errorf("%w（警告: %s）", err, strings.Join(res.Warnings, "; "))
		}
		return "", err
	}
	return cookieStr, nil
}

func persistCookies(cookieStr string) {
	persist := cookieFile{CookieString: cookieStr, Expires: time.Now().Unix() + 1800, Domain: "mintlify.com"}
	saveData, _ := json.Marshal(persist)
	_ = os.WriteFile(cookieFilePath, saveData, 0600)
}

// LoadCookies returns a cached or freshly extracted cookie string.
func LoadCookies() (string, error) {
	var stale string
	data, err := os.ReadFile(cookieFilePath)
	if err == nil {
		var cf cookieFile
		if json.Unmarshal(data, &cf) == nil && cf.CookieString != "" {
			if cf.Expires == 0 || time.Now().Unix() < cf.Expires-60 {
				return cf.CookieString, nil
			}
			stale = cf.CookieString
		}
	}

	cookieStr, err := ExtractCookiesFromChrome()
	if err != nil {
		// Chrome store may be locked/unavailable; last-known cookie sometimes still works.
		if stale != "" {
			return stale, nil
		}
		return "", err
	}
	persistCookies(cookieStr)
	return cookieStr, nil
}

// InvalidateCookies deletes the on-disk cookie cache (e.g. after CF 420).
func InvalidateCookies() {
	_ = os.Remove(cookieFilePath)
}

// FetchToken loads the Mintlify siteconfig JWT for the given site origin.
// proxyURL may be empty, or socks5:// / http:// for outbound access.
func FetchToken(siteOrigin, proxyURL string) (string, error) {
	siteOrigin = strings.TrimRight(strings.TrimSpace(siteOrigin), "/")
	if siteOrigin == "" {
		siteOrigin = DefaultSiteConfig().SiteOrigin
	}
	url := siteOrigin + "/docs/_mintlify/assistant/siteconfig"
	req, err := fhttp.NewRequest(fhttp.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Referer", siteOrigin+"/docs/en/quickstart")
	req.Header.Set("Origin", siteOrigin)

	client, err := newTLSClient(proxyURL)
	if err != nil {
		return "", fmt.Errorf("创建 TLS client 失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 siteconfig 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("siteconfig 返回 %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	tok, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	return string(bytes.TrimSpace(tok)), nil
}

// DecodeTokenPayload decodes the JWT payload claims.
func DecodeTokenPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("无效的 JWT token")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(decoded, &result); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	return result, nil
}

// Client talks to the Mintlify assistant API with Chrome TLS fingerprinting.
type Client struct {
	http     tls_client.HttpClient
	proxyURL string
}

// NewClient creates a Mintlify TLS client.
// proxyURL may be empty, or e.g. socks5://host:port / http://host:port.
func NewClient(proxyURL string) (*Client, error) {
	httpClient, err := newTLSClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("创建 TLS client 失败: %w", err)
	}
	return &Client{http: httpClient, proxyURL: strings.TrimSpace(proxyURL)}, nil
}

// ProxyURL returns the outbound proxy configured for this client.
func (c *Client) ProxyURL() string {
	if c == nil {
		return ""
	}
	return c.proxyURL
}

// StreamResponse is an open upstream stream; caller must Close.
type StreamResponse struct {
	Body    io.ReadCloser
	Session Session
	Header  fhttp.Header
}

// Close closes the response body.
func (r *StreamResponse) Close() error {
	if r == nil || r.Body == nil {
		return nil
	}
	return r.Body.Close()
}

// WarmCookies fetches Cloudflare / ALB cookies via the client's outbound path
// (useful when using a proxy whose egress IP differs from the local Chrome jar).
func (c *Client) WarmCookies(ctx context.Context, site SiteConfig) (string, error) {
	if c == nil || c.http == nil {
		return "", fmt.Errorf("mintlify client is nil")
	}
	if site.SiteOrigin == "" {
		site.SiteOrigin = DefaultSiteConfig().SiteOrigin
	}
	if site.DocsPath == "" {
		site.DocsPath = DefaultSiteConfig().DocsPath
	}
	referer := strings.TrimRight(site.SiteOrigin, "/") + "/docs" + site.DocsPath

	targets := []string{
		aiMessageHost + "/",
		strings.TrimRight(site.SiteOrigin, "/") + "/docs" + site.DocsPath,
	}
	var parts []string
	seen := map[string]string{}
	for _, u := range targets {
		req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header = fhttp.Header{
			"user-agent":         {chromeUA},
			"accept":             {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
			"accept-language":    {"en-US,en;q=0.9"},
			"referer":            {referer},
			"sec-fetch-dest":     {"document"},
			"sec-fetch-mode":     {"navigate"},
			"sec-fetch-site":     {"none"},
			fhttp.HeaderOrderKey: {"user-agent", "accept", "accept-language", "referer"},
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return "", fmt.Errorf("warm cookies failed (%s): %w", u, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		for _, sc := range resp.Header.Values("Set-Cookie") {
			name, value := parseSetCookieNV(sc)
			if name == "" || value == "" {
				continue
			}
			switch name {
			case "__cf_bm", "AWSALB", "AWSALBCORS", "cf_clearance":
				seen[name] = value
			}
		}
	}
	for _, name := range []string{"__cf_bm", "AWSALB", "AWSALBCORS", "cf_clearance"} {
		if v := seen[name]; v != "" {
			parts = append(parts, name+"="+v)
		}
	}
	if seen["__cf_bm"] == "" && seen["cf_clearance"] == "" {
		return "", fmt.Errorf("warm cookies: missing __cf_bm from proxy egress")
	}
	cookieStr := strings.Join(parts, "; ")
	persistCookies(cookieStr)
	return cookieStr, nil
}

func parseSetCookieNV(setCookie string) (name, value string) {
	part := strings.SplitN(setCookie, ";", 2)[0]
	kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
	if len(kv) != 2 {
		return "", ""
	}
	return strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
}

// LoadCookiesForClient prefers proxy-warmed cookies when proxyURL is set,
// otherwise falls back to Chrome/cache cookies.
func LoadCookiesForClient(c *Client, site SiteConfig) (string, error) {
	if c != nil && c.proxyURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		warmed, err := c.WarmCookies(ctx, site)
		if err != nil {
			return "", fmt.Errorf("warm cookies via proxy: %w", err)
		}
		if warmed != "" {
			return warmed, nil
		}
	}
	return LoadCookies()
}

// SendStream posts a message request and returns a streaming body (line-oriented).
func (c *Client) SendStream(ctx context.Context, site SiteConfig, cookie string, reqBody []byte) (*StreamResponse, error) {
	if c == nil || c.http == nil {
		return nil, fmt.Errorf("mintlify client is nil")
	}
	if site.Subdomain == "" {
		site.Subdomain = DefaultSiteConfig().Subdomain
	}
	if site.SiteOrigin == "" {
		site.SiteOrigin = DefaultSiteConfig().SiteOrigin
	}
	if site.DocsPath == "" {
		site.DocsPath = DefaultSiteConfig().DocsPath
	}

	apiURL := fmt.Sprintf("%s/api/assistant/%s/message", aiMessageHost, site.Subdomain)
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	referer := strings.TrimRight(site.SiteOrigin, "/") + "/docs" + site.DocsPath
	// Header set mirrors docs-expert (Accept: */*, Origin/Referer/UA). Cookie is optional
	// but helps CF when present; leave empty when using a foreign egress IP.
	req.Header = fhttp.Header{
		"accept":          {"*/*"},
		"content-type":    {"application/json"},
		"origin":          {site.SiteOrigin},
		"referer":         {referer + "/"},
		"user-agent":      {chromeUA},
		"accept-language": {"en-US,en;q=0.9"},
		fhttp.HeaderOrderKey: {
			"accept",
			"content-type",
			"origin",
			"referer",
			"user-agent",
			"accept-language",
		},
	}
	if strings.TrimSpace(cookie) != "" {
		req.Header.Set("cookie", cookie)
		req.Header[fhttp.HeaderOrderKey] = append(req.Header[fhttp.HeaderOrderKey], "cookie")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TLS 请求失败: %w", err)
	}

	session := Session{
		ThreadID:  resp.Header.Get("x-thread-id"),
		ThreadKey: resp.Header.Get("x-thread-key"),
		MessageID: resp.Header.Get("x-message-id"),
	}

	switch {
	case resp.StatusCode == 200:
		return &StreamResponse{Body: resp.Body, Session: session, Header: resp.Header}, nil
	case resp.StatusCode == 402:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			return nil, fmt.Errorf("%s", errMsg402)
		}
		return nil, fmt.Errorf("%s 详情: %s", errMsg402, detail)
	case resp.StatusCode == 420:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		InvalidateCookies()
		return nil, fmt.Errorf("%s 请用 Chrome 访问 %s 后再试: %s", errMsg420, referer, bytes.TrimSpace(body))
	case resp.StatusCode == 419:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("token 过期 (419): 需要重新获取 siteconfig: %s", bytes.TrimSpace(body))
	case resp.StatusCode == 418:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			return nil, fmt.Errorf("%s", errMsg418)
		}
		return nil, fmt.Errorf("%s 详情: %s", errMsg418, detail)
	case resp.StatusCode == 400 || resp.StatusCode == 450:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("请求失败 (%d): %s", resp.StatusCode, string(body))
	default:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, string(body))
	}
}

// ReadLines scans a Mintlify stream body line by line and invokes fn for each Chunk.
// It does not buffer the entire response.
func ReadLines(r io.Reader, fn func(Chunk) error) error {
	scanner := bufio.NewScanner(r)
	// Mintlify chunks are small; keep a reasonable buffer.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || len(line) < 2 || line[1] != ':' {
			continue
		}
		if err := fn(Chunk{Type: line[:1], Value: line[2:]}); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// ParseTextDelta unmarshals a type-0 chunk value into plain text.
func ParseTextDelta(value string) (string, bool) {
	var text string
	if json.Unmarshal([]byte(value), &text) != nil {
		return "", false
	}
	return text, true
}

// NewUserMessage builds a Mintlify user message.
func NewUserMessage(text string) Message {
	return Message{
		ID:        GenerateID(),
		CreatedAt: time.Now().UTC(),
		Role:      "user",
		Content:   text,
		Parts:     []Part{{Type: "text", Text: text}},
	}
}

// NewAssistantMessage builds a Mintlify assistant message.
func NewAssistantMessage(text string) Message {
	return Message{
		ID:        GenerateID(),
		CreatedAt: time.Now().UTC(),
		Role:      "assistant",
		Content:   text,
		Parts:     []Part{{Type: "text", Text: text}},
	}
}
