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

func registerMintlify(core *coreauth.Manager, exec *provider.Executor) (string, error) {
	// Re-bind executor after Service.Load / ensureExecutorsForAuth may have run.
	core.RegisterExecutor(exec)

	authID := "mintlify-default"
	registered, err := core.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "mintlify",
		Label:    "Mintlify Docs Assistant",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
			"subdomain":    "claude-code",
			"site_origin":  "https://code.claude.com",
			"docs_path":    "/en/quickstart",
			"language":     "en",
		},
	})
	if err != nil {
		return "", err
	}
	if registered != nil && registered.ID != "" {
		authID = registered.ID
	}

	models := []*cliproxy.ModelInfo{{
		ID:          "claude-docs",
		Object:      "model",
		Type:        "mintlify",
		DisplayName: "Claude Code Docs",
	}}
	for _, a := range core.List() {
		if strings.EqualFold(a.Provider, "mintlify") {
			cliproxy.GlobalModelRegistry().RegisterClient(a.ID, "mintlify", models)
			core.RefreshSchedulerEntry(a.ID)
		}
	}
	return authID, nil
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
	exec := provider.NewExecutor()
	core.RegisterExecutor(exec)

	hooks := cliproxy.Hooks{
		OnAfterStart: func(_ *cliproxy.Service) {
			// Register AFTER Service.Run → Manager.Load(), which replaces the auth map.
			authID, errReg := registerMintlify(core, exec)
			if errReg != nil {
				fmt.Fprintf(os.Stderr, "register mintlify auth: %v\n", errReg)
				return
			}
			fmt.Printf("mintlify gateway ready; model=claude-docs auth=%s\n", authID)

			// Watcher may settle shortly after start; re-bind models once more.
			go func() {
				time.Sleep(750 * time.Millisecond)
				if _, errRetry := registerMintlify(core, exec); errRetry != nil {
					fmt.Fprintf(os.Stderr, "re-register mintlify auth: %v\n", errRetry)
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
