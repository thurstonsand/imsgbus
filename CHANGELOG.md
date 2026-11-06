<!-- markdownlint-disable MD024 -->

# Changelog

## [Unreleased]

### Changed

- A person has one room in Beeper, however many chats Messages' filter splits them across. Messages files a message from someone not in Contacts under a chat of its own for each category (Transactions, Promotions, Unknown Senders), so one number used to bring a room per category. Their messages now share one room, which is low priority while the newest one is filtered, and moves out of it when one arrives in the conversation list, as after Mark as Known. Sends go to the chat Messages left alone; reading, tapbacks, edits and deletes reach whichever chat holds the message. On connect, rooms split this way are deleted and come back as one with their combined history.

## [0.2.6] - 2026-11-06

### Changed

- Deleting a chat in Beeper moves it to Recently Deleted in Messages instead of erasing it, so it can be recovered there, which brings it back into Beeper. This needs imsg 0.21.0 (openclaw/imsg#381).

## [0.2.5] - 2026-11-05

### Fixed

- Reading a chat in Beeper whose newest event is a tapback marks it read in Messages. Beeper puts the read receipt on the tapback, which imsgbus used to ignore, so the chat stayed unread until the next message.

## [0.2.4] - 2026-11-05

### Fixed

- A message sent from Beeper no longer comes back as a second copy when Messages saves it under a GUID other than the one imsgbus sent it with (openclaw/imsg#385). imsgbus matches Messages' copy to the send by chat and text, and points the message Beeper shows at Messages' GUID, so receipts and tapbacks on it still arrive.

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
