// Package matrix is imsgbus's Matrix frontend: a mautrix bridgev2 network connector over core.
package matrix

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	up "go.mau.fi/util/configupgrade"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/random"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/thurstonsand/imsgbus/internal/contacts"
	"github.com/thurstonsand/imsgbus/internal/core"
	"github.com/thurstonsand/imsgbus/internal/search"
)

//go:embed example-config.yaml
var exampleConfig string

// Config is the bridge config's `network` section.
type Config struct {
	IMsgPath    string `yaml:"imsg_path"`
	SearchIndex string `yaml:"search_index"`
	MCP         struct {
		Listen string `yaml:"listen"`
		Token  string `yaml:"token"`
	} `yaml:"mcp"`
}

// UserLoginMetadata persists the login's cursor, so a restart replays what it missed.
type UserLoginMetadata struct {
	Cursor int64 `json:"cursor"`
}

// Connector serves this Mac's one Messages account as one login.
type Connector struct {
	Config Config

	bridge   *bridgev2.Bridge
	core     *core.Service
	contacts *contacts.Catalog
}

var _ bridgev2.NetworkConnector = (*Connector)(nil)

// Init starts core rather than Start does: bridgev2 starts the Matrix side first, and its
// events can load a login, whose client needs core, before Start runs.
func (c *Connector) Init(bridge *bridgev2.Bridge) {
	c.bridge = bridge
	log := bridge.Log.With().Str("component", "core").Logger()
	fail := func(err error, msg string) {
		if err != nil {
			bridge.Log.Fatal().Err(err).Msg(msg)
		}
	}
	home, err := os.UserHomeDir()
	fail(err, "No home directory to find Messages and Contacts in")
	ctx := log.WithContext(context.Background())
	people, err := contacts.Open(ctx, filepath.Join(home, "Library", "Application Support", "AddressBook"))
	fail(err, "Failed to read Contacts")
	c.contacts = people
	index, err := search.OpenFTS5(c.Config.SearchIndex)
	fail(err, "Failed to open the search index")
	service, err := core.Start(log, core.Options{
		IMsgPath: c.Config.IMsgPath,
		ChatDB:   filepath.Join(home, "Library", "Messages", "chat.db"),
		Contacts: people,
		Search:   index,
	})
	fail(err, "Failed to start imsg rpc")
	c.core = service
	go func() {
		<-service.Exited()
		bridge.Log.Fatal().Err(service.Err()).Msg("imsg rpc exited")
	}()
}

// Core is the service the connector bridges, for other frontends to share.
func (c *Connector) Core() *core.Service { return c.core }

func (c *Connector) Start(ctx context.Context) error {
	ctx = c.bridge.Log.With().Str("component", "ghosts").Logger().WithContext(context.WithoutCancel(ctx))
	reloaded := func() {
		c.refreshGhosts(ctx)
		for _, login := range c.bridge.GetAllCachedUserLogins() {
			if client, ok := login.Client.(*Client); ok {
				client.regroup()
			}
		}
	}
	if err := c.contacts.Watch(ctx, *zerolog.Ctx(ctx), reloaded); err != nil {
		return fmt.Errorf("watch Contacts: %w", err)
	}
	go c.refreshGhosts(ctx)
	return nil
}

// refreshGhosts brings every ghost up to date with Contacts. bridgev2 only asks for a ghost's info
// until it has a name and avatar, so a contact added or edited later would never show.
func (c *Connector) refreshGhosts(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	rows, err := c.bridge.DB.Query(ctx, "SELECT id FROM ghost WHERE bridge_id=$1", c.bridge.ID)
	ids, err := dbutil.NewRowIterWithError(rows, dbutil.ScanSingleColumn[networkid.UserID], err).AsList()
	if err != nil {
		log.Err(err).Msg("Failed to list ghosts to refresh")
		return
	}
	changed := 0
	for _, id := range ids {
		ghost, err := c.bridge.GetExistingGhostByID(ctx, id)
		if err != nil {
			log.Err(err).Str("ghost_id", string(id)).Msg("Failed to load ghost to refresh")
			continue
		}
		name, avatar := ghost.Name, ghost.AvatarID
		ghost.UpdateInfo(ctx, userInfo(c.core, ghost))
		if ghost.Name != name || ghost.AvatarID != avatar {
			changed++
		}
	}
	log.Info().Str("event", "ghosts_refreshed").Int("ghosts", len(ids)).Int("changed", changed).Msg("Refreshed ghosts from Contacts")
}

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
		helper.Copy(up.Str, "search_index")
		helper.Copy(up.Str, "mcp", "listen")
		if token, ok := helper.Get(up.Str, "mcp", "token"); !ok || token == "generate" {
			helper.Set(up.Str, random.String(64), "mcp", "token")
		} else {
			helper.Copy(up.Str, "mcp", "token")
		}
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
	login.Client = &Client{login: login, core: c.core, cursor: newCursor(login, meta), deleting: map[string]bool{}, filtered: map[string]bool{}, refill: map[string]bool{}}
	return nil
}
