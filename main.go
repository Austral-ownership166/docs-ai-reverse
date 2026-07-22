// claude-code-chat — 逆向实现的 Claude Code 文档聊天助手客户端
//
// 通过分析 code.claude.com 文档站的聊天助手（Mintlify 驱动），
// 逆向出完整的 API 调用流程。
//
// 核心发现:
// - Cloudflare Bot Management 拦截非浏览器的 TLS 指纹（JA3）
// - 使用 bogdanfinn/tls-client 模拟 Chrome TLS 指纹绕过检测
// - 必须携带有效的 __cf_bm cookie（优先从本机 Chrome cookie 库自动提取）
// - SSE 响应格式已变更（不再是 data: 前缀）

package main

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
	siteConfigURL   = "https://code.claude.com/docs/_mintlify/assistant/siteconfig"
	aiMessageHost   = "https://leaves.mintlify.com"
	subdomain       = "claude-code"
	defaultLanguage = "en"
	defaultPath     = "/en/quickstart"
	chromeUA        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
)

type Message struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Parts     []Part    `json:"parts"`
}

type Part struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type client struct {
	token     string
	threadID  string
	threadKey string
	messageID string
	history   []Message
	http      tls_client.HttpClient
}

type messageRequest struct {
	ID           string    `json:"id"`
	Messages     []Message `json:"messages"`
	FP           string    `json:"fp"`
	Filter       filter    `json:"filter"`
	CurrentPath  string    `json:"currentPath"`
	Token        string    `json:"_,omitempty"`
	CaptchaToken string    `json:"captchaToken,omitempty"`
	ThreadID     string    `json:"threadId,omitempty"`
	ThreadKey    string    `json:"threadKey,omitempty"`
	Regenerate   bool      `json:"regenerate,omitempty"`
}

type filter struct {
	Language string `json:"language"`
}

type cookieFile struct {
	CookieString string `json:"cookie_string"`
	Expires      int64  `json:"expires"`
	Domain       string `json:"domain"`
}

type mintlifyChunk struct {
	Type  string
	Value string
}

var cookieFilePath = filepath.Join(os.Getenv("HOME"), ".claude-code-cookies.json")

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func newTLSClient() (tls_client.HttpClient, error) {
	// Chrome_146（HTTP/2）可绕过 CF；ForceHttp1 反而会 420。
	return tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithClientProfile(profiles.Chrome_146),
		tls_client.WithNotFollowRedirects(),
	)
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

// extractCookiesFromChrome 用 sweetcookie 直接读取本机 Chrome cookie 库并解密。
func extractCookiesFromChrome() (string, error) {
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
	os.WriteFile(cookieFilePath, saveData, 0600)
}

func loadCookies() (string, error) {
	data, err := os.ReadFile(cookieFilePath)
	if err == nil {
		var cf cookieFile
		if json.Unmarshal(data, &cf) == nil && cf.CookieString != "" {
			if cf.Expires == 0 || time.Now().Unix() < cf.Expires-60 {
				return cf.CookieString, nil
			}
			fmt.Println("cookie 已过期，重新获取...")
		}
	}

	cookieStr, err := extractCookiesFromChrome()
	if err != nil {
		return "", err
	}
	persistCookies(cookieStr)
	return cookieStr, nil
}

func FetchToken() (string, error) {
	req, err := http.NewRequest("GET", siteConfigURL, nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Referer", "https://code.claude.com/docs/en/quickstart")
	req.Header.Set("Origin", "https://code.claude.com")

	resp, err := http.DefaultClient.Do(req)
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

// sendViaTLS 用 Go TLS 指纹模拟客户端发送 API 请求。
// Cloudflare Bot Management 检测 JA3；标准 net/http 会被拦截，
// bogdanfinn/tls-client + Chrome_146 profile 可模拟浏览器握手。
func (c *client) sendViaTLS(cookie string, reqBody []byte) ([]byte, error) {
	if c.http == nil {
		httpClient, err := newTLSClient()
		if err != nil {
			return nil, fmt.Errorf("创建 TLS client 失败: %w", err)
		}
		c.http = httpClient
	}

	apiURL := fmt.Sprintf("%s/api/assistant/%s/message", aiMessageHost, subdomain)
	req, err := fhttp.NewRequest(fhttp.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header = fhttp.Header{
		"content-type":    {"application/json"},
		"user-agent":      {chromeUA},
		"accept":          {"text/event-stream"},
		"accept-language": {"en-US,en;q=0.9"},
		"referer":         {"https://code.claude.com/docs/en/quickstart"},
		"origin":          {"https://code.claude.com"},
		"cookie":          {cookie},
		fhttp.HeaderOrderKey: {
			"content-type",
			"user-agent",
			"accept",
			"accept-language",
			"referer",
			"origin",
			"cookie",
		},
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TLS 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if tid := resp.Header.Get("x-thread-id"); tid != "" {
		c.threadID = tid
	}
	if tkey := resp.Header.Get("x-thread-key"); tkey != "" {
		c.threadKey = tkey
	}
	if mid := resp.Header.Get("x-message-id"); mid != "" {
		c.messageID = mid
	}

	switch {
	case resp.StatusCode == 200:
		return respBody, nil
	case resp.StatusCode == 420:
		os.Remove(cookieFilePath)
		return nil, fmt.Errorf("CF Bot Management 拦截 (420): 需要更新 cookie。请用 Chrome 访问 https://code.claude.com/docs/en/quickstart 后再试")
	case resp.StatusCode == 419:
		return nil, fmt.Errorf("token 过期 (419): 需要重新获取 siteconfig")
	case resp.StatusCode == 400 || resp.StatusCode == 450:
		return nil, fmt.Errorf("请求失败 (%d): %s", resp.StatusCode, string(respBody))
	default:
		return nil, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, string(respBody))
	}
}

func (c *client) Send(text string) ([]byte, error) {
	now := time.Now().UTC()
	msg := Message{
		ID:        generateID(),
		CreatedAt: now,
		Role:      "user",
		Content:   text,
		Parts:     []Part{{Type: "text", Text: text}},
	}
	c.history = append(c.history, msg)

	reqBody := messageRequest{
		ID:          subdomain,
		Messages:    c.history,
		FP:          subdomain,
		Filter:      filter{Language: defaultLanguage},
		CurrentPath: defaultPath,
		Token:       c.token,
	}
	if c.threadID != "" {
		reqBody.ThreadID = c.threadID
	}
	if c.threadKey != "" {
		reqBody.ThreadKey = c.threadKey
	}

	jsonBody, _ := json.Marshal(reqBody)

	cookies, err := loadCookies()
	if err != nil {
		return nil, err
	}

	return c.sendViaTLS(cookies, jsonBody)
}

func parseMintlifyResponse(data []byte, onChunk func(mintlifyChunk)) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || len(line) < 2 || line[1] != ':' {
			continue
		}
		onChunk(mintlifyChunk{Type: line[:1], Value: line[2:]})
	}
}

func Chat(token string) error {
	c := &client{token: token}
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Claude Code 文档助手 · 逆向实现版")
	fmt.Println("  /exit 退出  /new 新对话")
	fmt.Println(strings.Repeat("─", 50))

	for {
		fmt.Print("\n> ")
		text, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		text = strings.TrimSpace(text)
		switch text {
		case "/exit", "/quit":
			return nil
		case "/new":
			c.history = nil
			fmt.Println("--- 新对话 ---")
			continue
		case "":
			continue
		}

		respBody, err := c.Send(text)
		if err != nil {
			return fmt.Errorf("发送失败: %w", err)
		}

		fmt.Print("\n助手: ")
		var full strings.Builder
		parseMintlifyResponse(respBody, func(chunk mintlifyChunk) {
			if chunk.Type == "0" {
				var text string
				if json.Unmarshal([]byte(chunk.Value), &text) == nil {
					fmt.Print(text)
					full.WriteString(text)
				}
			}
		})
		fmt.Println()

		c.history = append(c.history, Message{
			ID:        generateID(),
			CreatedAt: time.Now().UTC(),
			Role:      "assistant",
			Content:   full.String(),
			Parts:     []Part{{Type: "text", Text: full.String()}},
		})
	}
}

func main() {
	fmt.Println("正在获取授权 token...")
	token, err := FetchToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取 token 失败: %v\n", err)
		os.Exit(1)
	}
	if p, err := DecodeTokenPayload(token); err == nil {
		exp := int64(p["exp"].(float64))
		provider, _ := p["p"].(string)
		if provider == "" {
			provider = "none"
		}
		fmt.Printf("Token: sub=%s provider=%s 过期=%s (剩余 %v)\n",
			p["sub"], provider,
			time.Unix(exp, 0).Format("15:04:05"),
			time.Until(time.Unix(exp, 0)).Round(time.Second))
	}

	if _, err := os.Stat(cookieFilePath); os.IsNotExist(err) {
		fmt.Println("\n首次使用：正在从本机 Chrome cookie 库自动提取...")
		fmt.Println("（请确保近期用 Chrome 访问过 https://code.claude.com/docs/en/quickstart）")
	}

	args := os.Args[1:]
	if len(args) > 0 {
		query := strings.Join(args, " ")
		fmt.Printf("查询: %s\n\n", query)
		c := &client{token: token}
		respBody, err := c.Send(query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Print("助手: ")
		parseMintlifyResponse(respBody, func(chunk mintlifyChunk) {
			if chunk.Type == "0" {
				var text string
				if json.Unmarshal([]byte(chunk.Value), &text) == nil {
					fmt.Print(text)
				}
			}
		})
		fmt.Println()
		return
	}

	if err := Chat(token); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}
