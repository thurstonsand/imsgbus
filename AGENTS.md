# AGENTS.md

`imsgbus` serves one Mac's iMessage to more than one consumer at once: to Beeper as a self-hosted mautrix bridgev2 bridge, and to coding agents as an MCP server. It runs inside a macOS VM signed in to Messages and drives Messages through [imsg](https://github.com/openclaw/imsg).

## Motivation

Beeper dropped its own iMessage bridge, and the iMessage MCP servers I tried each reimplement chat.db reading and AppleScript sending with their own bugs. imsg already does the hard parts well (decoded message text, tracked sends through the IMCore bridge, read receipts, a watch feed), so imsgbus is the thin layer that shapes it for each consumer.

## Context

see @CONTEXT.md for domain definitions for this project.

## Tenets

- imsg owns Messages: everything that reads or changes Messages goes through imsg, never chat.db writes or AppleScript of our own
- Pluggable frontends: core knows nothing about Matrix or MCP; each frontend adapts core to its protocol
- Pluggable search: search sits behind a backend interface so FTS5 can be swapped for Spotlight or imsg's own search
- Read receipts both ways are a hard requirement, not a nice-to-have
- Names, not handles: the MCP frontend takes and returns contact names, and ambiguity is an error, never a guess
- Stay close to mautrix: mxmain hosts the process, so mautrix updates arrive as a version bump; read mautrix's changelog and mxmain's diff on every bump
- Ruthless refactoring encouraged: break backwards compat freely

## Developer Notes

see @DEV.md for commands and coding style.
