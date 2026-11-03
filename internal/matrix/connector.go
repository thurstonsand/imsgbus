// Package matrix is imsgbus's Matrix frontend: a mautrix bridgev2 network connector over core.
package matrix

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	up "go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/thurstonsand/imsgbus/internal/core"
)

//go:embed example-config.yaml
var exampleConfig string

// Config is the bridge config's `network` section.
type Config struct {
	IMsgPath string `yaml:"imsg_path"`
}

// UserLoginMetadata persists the login's cursor, so a restart replays what it missed.
type UserLoginMetadata struct {
	Cursor int64 `json:"cursor"`
}

// Connector serves this Mac's one Messages account as one login.
type Connector struct {
	Config Config

	bridge *bridgev2.Bridge
	core   *core.Service
}

var _ bridgev2.NetworkConnector = (*Connector)(nil)

// Init starts core rather than Start does: bridgev2 starts the Matrix side first, and its
// events can load a login, whose client needs core, before Start runs.
func (c *Connector) Init(bridge *bridgev2.Bridge) {
	c.bridge = bridge
	home, err := os.UserHomeDir()
	if err != nil {
		bridge.Log.Fatal().Err(err).Msg("No home directory to find chat.db in")
	}
	chatDB := filepath.Join(home, "Library", "Messages", "chat.db")
	service, err := core.Start(bridge.Log.With().Str("component", "core").Logger(), c.Config.IMsgPath, chatDB)
	if err != nil {
		bridge.Log.Fatal().Err(err).Msg("Failed to start imsg rpc")
	}
	c.core = service
	go func() {
		<-service.Exited()
		bridge.Log.Fatal().Err(service.Err()).Msg("imsg rpc exited")
	}()
}

func (c *Connector) Start(context.Context) error { return nil }

func (c *Connector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "iMessage",
		NetworkURL:           "https://support.apple.com/messages",
		NetworkID:            "imessage",
		BeeperBridgeType:     "imsgbus",
		DefaultPort:          29350,
		DefaultCommandPrefix: "!im",
	}
}

func (c *Connector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		UserLogin: func() any { return &UserLoginMetadata{} },
	}
}

func (c *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

func (c *Connector) GetConfig() (string, any, up.Upgrader) {
	return exampleConfig, &c.Config, up.SimpleUpgrader(func(helper up.Helper) {
		helper.Copy(up.Str, "imsg_path")
	})
}

func (c *Connector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 2
}

func (c *Connector) LoadUserLogin(_ context.Context, login *bridgev2.UserLogin) error {
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok {
		return fmt.Errorf("login %s has metadata of type %T", login.ID, login.Metadata)
	}
	login.Client = &Client{login: login, core: c.core, cursor: newCursor(login, meta)}
	return nil
}
