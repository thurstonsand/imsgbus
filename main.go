// imsgbus serves one Mac's Messages to Beeper over Matrix and to agents over MCP.
package main

import (
	"context"

	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

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
		Version:     "0.2.0",
		Connector:   connector,
	}
	// The Matrix frontend owns the process through mxmain; the others start once it runs.
	m.PostStart = func() {
		ctx := m.Log.WithContext(context.Background())
		service := connector.Core()
		go service.RunIndex(ctx)
		go service.RunHeartbeat(ctx)
		go func() {
			err := mcp.Serve(ctx, connector.Config.MCP.Listen, connector.Config.MCP.Token, m.Version, service)
			m.Log.Fatal().Err(err).Msg("MCP server stopped")
		}()
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
