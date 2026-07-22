package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"claude-code-chat/internal/mintlify"
)

func main() {
	proxyURL := strings.TrimSpace(os.Getenv("MINTLIFY_PROXY"))
	if proxyURL == "" {
		proxyURL = "socks5://100.74.21.88:7890"
	}
	fmt.Println("proxy=", proxyURL)

	c, err := mintlify.NewClient(proxyURL)
	if err != nil {
		fmt.Println("client err:", err)
		os.Exit(1)
	}
	site := mintlify.DefaultSiteConfig()
	cookie, err := mintlify.LoadCookiesForClient(c, site)
	if err != nil {
		fmt.Println("cookie err:", err)
		os.Exit(1)
	}
	fmt.Println("cookie ok, len=", len(cookie), "prefix=", cookie[:min(40, len(cookie))])

	token, err := mintlify.FetchToken(site.SiteOrigin, proxyURL)
	if err != nil {
		fmt.Println("token err:", err)
	} else {
		fmt.Println("token ok, len=", len(token))
	}
	msg := mintlify.NewUserMessage("Say hi in one word")
	req := mintlify.MessageRequest{
		ID:          site.Subdomain,
		FP:          site.Subdomain,
		Filter:      mintlify.Filter{Language: site.Language},
		CurrentPath: site.DocsPath,
		Messages:    []mintlify.Message{msg},
		Token:       token,
	}
	b, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	stream, err := c.SendStream(ctx, site, cookie, b)
	if err != nil {
		fmt.Println("send err:", err)
		os.Exit(2)
	}
	defer stream.Close()
	data, _ := io.ReadAll(stream.Body)
	s := string(data)
	if len(s) > 800 {
		s = s[:800]
	}
	fmt.Println("OK preview:\n", s)
}
