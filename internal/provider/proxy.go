package provider

import (
	"net/http"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// proxyDefaults holds the global proxy-url fallback (CLIProxyAPI cfg.ProxyURL).
// Resolution order matches CLIProxyAPI helpers: auth.ProxyURL → defaultProxy.
type proxyDefaults struct {
	mu           sync.Mutex
	defaultProxy string
}

// SetDefaultProxy sets the outbound proxy used when auth has no ProxyURL.
func (p *proxyDefaults) SetDefaultProxy(proxyURL string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.defaultProxy = strings.TrimSpace(proxyURL)
	p.mu.Unlock()
}

func (p *proxyDefaults) resolveProxy(a *coreauth.Auth) string {
	if v := proxyFromAuth(a); v != "" {
		return v
	}
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.defaultProxy
}

func proxyFromAuth(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if v := strings.TrimSpace(a.ProxyURL); v != "" {
		return v
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["proxy"]); v != "" {
			return v
		}
	}
	return ""
}

// httpClientForProxy builds an *http.Client using CLIProxyAPI proxyutil.
// Empty proxyURL uses the default transport; "direct"/"none" bypass env proxies.
func httpClientForProxy(proxyURL string, timeout time.Duration) *http.Client {
	client := &http.Client{}
	if timeout > 0 {
		client.Timeout = timeout
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return client
	}
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil || transport == nil {
		return client
	}
	client.Transport = transport
	return client
}
