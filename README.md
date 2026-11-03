# imsgbus

One Mac's iMessage, served to [Beeper](https://www.beeper.com) as a self-hosted bridge and to coding agents as an MCP server.

imsgbus drives Messages through [imsg](https://github.com/openclaw/imsg), so the Mac it runs on must be signed in to Messages and have imsg's IMCore bridge running (`imsg launch`).

## Status

Early, and used by one person. The Beeper bridge carries messages, attachments, stickers, tapbacks, edits, unsends, read and delivery receipts and typing, in one-on-one and group chats, with people named and pictured as in Contacts. The MCP server reads, searches and sends by contact name.

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

## Logs

Configure a `json` file writer under `logging` to keep a log worth querying. Each line that records something worth counting has an `event` field:

| Event | Logged when | Fields |
| --- | --- | --- |
| `inbound` | an iMessage message, tapback, edit, unsend or group change heads to Beeper | `kind`, `from_me`, `lag_ms` since it happened |
| `receipt` | a delivery or read from iMessage heads to Beeper | `type`, `from_me`, `lag_ms` |
| `read_receipt` | Beeper reports you read a chat | `marked_read`: whether iMessage was told |
| `imsg_call` | imsg answers any request | `method`, `ok`, `duration_ms`, and `code` on failure; Beeper's `portal_id` or the MCP `tool` that asked |
| `mcp_tool` | an agent's tool call finishes | `tool`, `ok`, `duration_ms`, `error` |
| `unsupported` | a message with neither text nor files reaches Beeper as a placeholder | `guid`, and the iMessage `app` that made it |
| `attachment_missing` | a file isn't downloaded to the Mac yet | `mime` |
| `watch_restart` | a watch ended and starts again | `watch`, `error` |
| `ghosts_refreshed` | Contacts was reapplied to the people in Beeper | `ghosts`, `changed` |
| `heartbeat` | every 5 minutes | `chat_db_row` and `index_row`, `contacts`, `goroutines`, `heap_mb`, `uptime_s` |

## License

MIT
