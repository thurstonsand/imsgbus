# imsgbus

One Mac's iMessage, served to [Beeper](https://www.beeper.com) as a self-hosted bridge and to coding agents as an MCP server.

imsgbus drives Messages through [imsg](https://github.com/openclaw/imsg), so the Mac it runs on must be signed in to Messages and have imsg's IMCore bridge running (`imsg launch`).

## Status

Early. The Beeper bridge carries text in one-on-one chats both ways and turns Beeper read receipts into iMessage ones. The MCP server is not built yet.

## Install

```sh
brew install --cask thurstonsand/tap/imsgbus
```

## Run as a Beeper bridge

Register the bridge with [bbctl](https://github.com/beeper/bridge-manager), set `network.imsg_path` in the generated config to imsg's path, and run it in the GUI session of the user signed in to Messages:

```sh
bbctl config --type bridgev2 -o config.yaml sh-imsgbus
imsgbus -c config.yaml
```

Then send `login messages` to `@sh-imsgbusbot:beeper.local` in Beeper.

## License

MIT
