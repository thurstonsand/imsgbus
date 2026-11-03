<!-- markdownlint-disable MD024 -->

# Changelog

## [Unreleased]

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
