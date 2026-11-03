// imsgbus serves one Mac's Messages to Beeper over Matrix and to agents over MCP.
package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/thurstonsand/imsgbus/internal/matrix"
)

// Set by goreleaser.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	m := mxmain.BridgeMain{
		Name:        "imsgbus",
		Description: "One Mac's iMessage, served to Beeper over Matrix and to agents over MCP",
		URL:         "https://github.com/thurstonsand/imsgbus",
		Version:     "0.0.0",
		Connector:   &matrix.Connector{},
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
