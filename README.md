# imsgbus

One Mac's iMessage, served to [Beeper](https://www.beeper.com) as a self-hosted bridge and to coding agents as an MCP server.

imsgbus drives Messages through [imsg](https://github.com/openclaw/imsg), so the Mac it runs on must be signed in to Messages and have imsg's IMCore bridge running (`imsg launch`).

## Status

Early, and used by one person. The Beeper bridge carries messages, attachments, tapbacks, edits, unsends, read and delivery receipts and typing, in one-on-one and group chats. The MCP server reads, searches and sends by contact name.

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

## MCP

The same process serves MCP over Streamable HTTP at `network.mcp.listen`. Clients send `Authorization: Bearer <network.mcp.token>`; the token is generated on first start. imsgbus needs Full Disk Access for chat.db and the AddressBook stores.

## License

MIT
