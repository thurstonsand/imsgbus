// imsgbus serves one Mac's Messages to Beeper over Matrix and to agents over MCP.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exzerolog"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/thurstonsand/imsgbus/internal/core"
	"github.com/thurstonsand/imsgbus/internal/matrix"
	"github.com/thurstonsand/imsgbus/internal/mcp"
)

// Set by goreleaser.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	connector := &matrix.Connector{}
	m := mxmain.BridgeMain{
		Name:        "imsgbus",
		Description: "One Mac's iMessage, served to Beeper over Matrix and to agents over MCP",
		URL:         "https://github.com/thurstonsand/imsgbus",
		Version:     "0.2.20",
		Connector:   connector,
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.PreInit()
	cfg := connector.Config
	switch {
	case cfg.Matrix.Enabled:
		// The Matrix frontend owns the process through mxmain, and core with it; the rest start once it runs.
		m.PostStart = func() { serve(m.Log.WithContext(context.Background()), connector.Core(), cfg, m.Version) }
		m.Init()
		m.Start()
		code := m.WaitForInterrupt()
		m.Stop()
		os.Exit(code)
	case cfg.MCP.Enabled:
		log, err := m.Config.Logging.Compile()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Failed to initialize logger:", err)
			os.Exit(12)
		}
		exzerolog.SetupDefaults(log)
		log.Info().Str("name", m.Name).Str("version", m.Version).Msg("Starting MCP only; network.matrix.enabled is off")
		ctx, stop := signal.NotifyContext(log.WithContext(context.Background()), os.Interrupt, syscall.SIGTERM)
		service, err := core.Open(log.With().Str("component", "core").Logger(), cfg.IMsgPath, cfg.SearchIndex)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to start core")
		}
		if err := service.WatchContacts(ctx, func() {}); err != nil {
			log.Fatal().Err(err).Msg("Failed to watch Contacts")
		}
		serve(ctx, service, cfg, m.Version)
		<-ctx.Done()
		stop()
		log.Info().Msg("Interrupt signal received from OS")
	default:
		_, _ = fmt.Fprintln(os.Stderr, "Both network.matrix.enabled and network.mcp.enabled are off, so there is nothing to serve")
		os.Exit(11)
	}
}

// serve starts what runs beside the Matrix frontend: the search index, the heartbeat, and MCP when enabled.
func serve(ctx context.Context, service *core.Service, cfg matrix.Config, version string) {
	go service.RunIndex(ctx)
	go service.RunHeartbeat(ctx)
	if !cfg.MCP.Enabled {
		return
	}
	go func() {
		if err := mcp.Serve(ctx, cfg.MCP.Listen, cfg.MCP.Token, version, service); err != nil {
			zerolog.Ctx(ctx).Fatal().Err(err).Msg("MCP server stopped")
		}
	}()
}
