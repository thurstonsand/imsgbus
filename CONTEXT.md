# CONTEXT.md

## Language

- **imsg**: The CLI imsgbus runs as one `imsg rpc` child, speaking JSON-RPC over stdio. It reads chat.db and sends through its IMCore **bridge** injected into Messages.
- **Core**: The frontend-neutral service in `internal/core`. It owns the imsg child, fans its watch feed out to frontends, and caches chats and contact names.
- **Frontend**: An adapter that serves core over one protocol: **Matrix** (`internal/matrix`, a bridgev2 network connector) or **MCP**.
- **Search backend**: The pluggable index behind message search; FTS5 first.
- **Portal**: A Matrix room bridged to one iMessage chat, keyed by the chat's GUID.
- **Login**: The one Apple Account Messages is signed in to, adopted as is; its ID is imsg's `account_id`.
- **Cursor**: The chat.db message row ID a login has bridged through, saved after every message so a restart resumes where it stopped.
- **cupertino**: The Tart macOS VM on pod153 that runs Messages, imsg and imsgbus. `cupertino-golden` is its backup and must never boot while it runs.
