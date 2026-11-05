<!-- markdownlint-disable MD024 -->

# Changelog

## [Unreleased]

## [0.2.3] - 2026-11-05

### Fixed

- A change to a group, such as someone being added, removed or leaving, or a new name or photo, comes into Beeper as a line from whoever made it ("Dana added Sam to the conversation") instead of "Unsupported message". imsg doesn't report what these rows are, so imsgbus reads them from chat.db. Messages already in Beeper keep the old placeholder.

## [0.2.2] - 2026-11-03

### Changed

- Every chat with a message comes into Beeper on connect, with its full history, not only those active in the last 30 days.

## [0.2.1] - 2026-11-03

### Fixed

- Backfilling older history no longer stops at a chat's oldest bridged message: imsg returned that message again as older than itself, and Beeper's bridge failed the page.

## [0.2.0] - 2026-11-03

### Added

- Deleting a chat syncs both ways. A chat deleted in iMessage leaves Beeper, including one deleted while imsgbus was down, and comes back when it is recovered from Recently Deleted or gets a new message; a delete on another device arrives once the Mac syncs Messages in iCloud. Deleting a chat in Beeper erases it from Messages on the Mac, without Recently Deleted, and leaves it on your other devices.
- Log events name the chat and message they are about, and `mcp_tool` logs a call's arguments without the text of messages sent.

## [0.1.1] - 2026-11-03

### Fixed

- Contacts reloads only when Contacts changes, not every 2 seconds: reading the address book set off the next reload, and each reload reapplied Contacts to every person in Beeper.

## [0.1.0] - 2026-11-03

### Added

- A Beeper bridge for the Apple Account that Messages on the Mac is signed in to, for one-on-one and group chats:
  - Messages both ways, with replies, attachments and voice messages; a file's caption goes as a message of its own, as in Messages.
  - Tapbacks both ways, including emoji tapbacks; one per person per message, as in iMessage.
  - Edits and unsends both ways, within Apple's 15- and 2-minute windows.
  - Read receipts both ways: reading in Beeper marks the chat read on iMessage once nothing newer from the other side is unread, and reads on your other devices or by the other person show in Beeper. Delivery receipts too.
  - Typing indicators both ways in one-on-one chats.
  - Chats active in the last 30 days appear in Beeper on login, with their recent history.
  - People show as Contacts names them, with their contact photo, and follow edits in Contacts; anyone else shows as their number written out.
  - Chats that Messages filters into Transactions, Promotions or Unknown Senders go to Beeper's Low Priority.
  - Stickers arrive as stickers.
- An MCP server for agents, over Streamable HTTP behind a bearer token, that speaks in contact names: people and chats go in and come out by name, a phone number or email only when asked for, and an ambiguous name is an error listing who it could be. Its tools list chats or an unread digest, read a chat's history, search every message, show an image attachment, send, tapback, edit, unsend and mark read.
- Log events for traffic, latency, failures and anything bridged as unsupported, with a heartbeat every 5 minutes; see the README.
