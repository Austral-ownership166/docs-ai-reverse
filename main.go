package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"claude-code-chat/internal/provider"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type modelDef struct {
	ID          string
	DisplayName string
	Description string
	Type        string
}

type providers struct {
	mintlify *provider.Executor
	inkeep   *provider.InkeepExecutor
	stripe   *provider.StripeExecutor
	readme   *provider.ReadmeExecutor
}

func (p *providers) setDefaultProxy(proxyURL string) {
	p.mintlify.SetDefaultProxy(proxyURL)
	p.inkeep.SetDefaultProxy(proxyURL)
	p.stripe.SetDefaultProxy(proxyURL)
	p.readme.SetDefaultProxy(proxyURL)
}

func registerAuth(core *coreauth.Manager, id, providerName, label string, attrs map[string]string, proxyURL string, models []modelDef) (string, error) {
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["runtime_only"] = "true"
	if proxyURL = strings.TrimSpace(proxyURL); proxyURL != "" {
		attrs["proxy"] = proxyURL
	}
	registered, err := core.Register(context.Background(), &coreauth.Auth{
		ID:         id,
		Provider:   providerName,
		Label:      label,
		Status:     coreauth.StatusActive,
		Attributes: attrs,
		ProxyURL:   proxyURL,
	})
	if err != nil {
		return "", err
	}
	authID := id
	if registered != nil && registered.ID != "" {
		authID = registered.ID
	}

	infos := make([]*cliproxy.ModelInfo, 0, len(models))
	for _, m := range models {
		infos = append(infos, &cliproxy.ModelInfo{
			ID:                         m.ID,
			Object:                     "model",
			Type:                       m.Type,
			DisplayName:                m.DisplayName,
			Description:                m.Description,
			ContextLength:              200000,
			InputTokenLimit:            200000,
			OutputTokenLimit:           8192,
			MaxCompletionTokens:        8192,
			SupportedGenerationMethods: []string{"generateContent", "countTokens"},
		})
	}
	for _, a := range core.List() {
		if strings.EqualFold(a.Provider, providerName) && a.ID == authID {
			cliproxy.GlobalModelRegistry().RegisterClient(a.ID, providerName, infos)
			core.RefreshSchedulerEntry(a.ID)
		}
	}
	return authID, nil
}

func registerAllProviders(core *coreauth.Manager, p *providers, proxyURL string) error {
	core.RegisterExecutor(p.mintlify)
	core.RegisterExecutor(p.inkeep)
	core.RegisterExecutor(p.stripe)
	core.RegisterExecutor(p.readme)

	if _, err := registerAuth(core, "mintlify-default", "mintlify", "Mintlify Docs Assistant", map[string]string{
		"subdomain":   "claude-code",
		"site_origin": "https://code.claude.com",
		"docs_path":   "/en/quickstart",
		"language":    "en",
	}, proxyURL, []modelDef{{
		ID: "claude-docs", DisplayName: "Claude Code Docs", Type: "mintlify",
		Description: "Mintlify docs assistant (code.claude.com)",
	}}); err != nil {
		return fmt.Errorf("mintlify: %w", err)
	}

	if _, err := registerAuth(core, "inkeep-claude", "inkeep", "Anthropic Docs (Inkeep)", map[string]string{
		"origin":  "https://platform.claude.com",
		"referer": "https://platform.claude.com/",
	}, proxyURL, []modelDef{{
		ID: "anthropic-docs", DisplayName: "Anthropic Docs", Type: "inkeep",
		Description: "Inkeep docs AI for Anthropic/Claude platform docs",
	}}); err != nil {
		return fmt.Errorf("inkeep: %w", err)
	}

	if _, err := registerAuth(core, "stripe-default", "stripe", "Stripe Docs AI", nil, proxyURL, []modelDef{{
		ID: "stripe-docs", DisplayName: "Stripe Docs", Type: "stripe",
		Description: "Stripe docs AI (ai.stripe.com)",
	}}); err != nil {
		return fmt.Errorf("stripe: %w", err)
	}

	if _, err := registerAuth(core, "readme-default", "readme", "ReadMe Docs AI", map[string]string{
		"docs_url": "https://docs.readme.com",
	}, proxyURL, []modelDef{{
		ID: "readme-docs", DisplayName: "ReadMe Docs", Type: "readme",
		Description: "ReadMe Ask AI (docs.readme.com)",
	}}); err != nil {
		return fmt.Errorf("readme: %w", err)
	}

	return nil
}

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		example := "config.example.yaml"
		if _, errEx := os.Stat(example); errEx == nil {
			fmt.Fprintf(os.Stderr, "config %q not found; copy %s to %s and set api-keys\n", cfgPath, example, cfgPath)
		} else {
			fmt.Fprintf(os.Stderr, "config %q not found\n", cfgPath)
		}
		os.Exit(1)
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	tokenStore := sdkAuth.GetTokenStore()
	if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(cfg.AuthDir)
	}

	core := coreauth.NewManager(tokenStore, nil, nil)
	p := &providers{
		mintlify: provider.NewExecutor(),
		inkeep:   provider.NewInkeepExecutor(),
		stripe:   provider.NewStripeExecutor(),
		readme:   provider.NewReadmeExecutor(),
	}
	proxyURL := strings.TrimSpace(cfg.ProxyURL)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(os.Getenv("MINTLIFY_PROXY"))
	}
	p.setDefaultProxy(proxyURL)

	hooks := cliproxy.Hooks{
		OnAfterStart: func(_ *cliproxy.Service) {
			if errReg := registerAllProviders(core, p, proxyURL); errReg != nil {
				fmt.Fprintf(os.Stderr, "register providers: %v\n", errReg)
				return
			}
			fmt.Println("docs gateway ready; models=claude-docs,anthropic-docs,stripe-docs,readme-docs")
			go func() {
				time.Sleep(750 * time.Millisecond)
				if errRetry := registerAllProviders(core, p, proxyURL); errRetry != nil {
					fmt.Fprintf(os.Stderr, "re-register providers: %v\n", errRetry)
				}
			}()
		},
	}

	svc, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(cfgPath).
		WithCoreAuthManager(core).
		WithHooks(hooks).
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build service: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	abs, _ := filepath.Abs(cfgPath)
	fmt.Printf("starting cliproxy with config %s on port %d\n", abs, cfg.Port)
	if errRun := svc.Run(ctx); errRun != nil && !errors.Is(errRun, context.Canceled) {
		fmt.Fprintf(os.Stderr, "run: %v\n", errRun)
		os.Exit(1)
	}
}
