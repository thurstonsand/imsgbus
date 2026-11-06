package core_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/thurstonsand/imsgbus/internal/contacts"
	"github.com/thurstonsand/imsgbus/internal/core"
)

const fakeScenario = "IMSGBUS_FAKE_IMSG"

// fakeCalls names a file the fake appends each read and chats.delete to, with the chat it got.
const fakeCalls = "IMSGBUS_FAKE_CALLS"

// TestMain lets the test binary stand in for `imsg rpc`: core runs `<path> rpc`, and the scenario
// in the environment says what the fake writes when a watch subscribes.
func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakeScenario); scenario != "" {
		fakeIMsg(scenario)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeIMsg(scenario string) {
	out := bufio.NewWriter(os.Stdout)
	write := func(frame string) {
		_, _ = out.WriteString(frame + "\n")
		_ = out.Flush()
	}
	message := func(row int) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","method":"message","params":{"subscription":7,"message":{"id":%d,"guid":"g%d","chat_guid":"any;-;+15550000000","sender":"+15550000000","text":"m%d"}}}`, row, row, row)
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				ChatID   int64  `json:"chat_id"`
				ChatGUID string `json:"chat_guid"`
			} `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &request); err != nil {
			panic(err)
		}
		switch request.Method {
		case "chats.list":
			write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"chats":[{"id":62,"guid":"any;-;+15550000000"}]}}`, request.ID))
			continue
		case "messages.history":
			if scenario == "conversation" {
				// A person's plain chat (62) and the chat Messages' filter filed one of their
				// messages under (63), each newest first.
				page := map[int64]string{
					62: `{"id":40,"guid":"plain40","created_at":"2026-10-01T10:00:00Z"},{"id":20,"guid":"plain20","created_at":"2026-09-01T10:00:00Z"}`,
					63: `{"id":30,"guid":"filtered30","created_at":"2026-09-15T10:00:00Z"}`,
				}[request.Params.ChatID]
				write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"messages":[%s]}}`, request.ID, page))
				continue
			}
			// Newest first by date, as imsg orders it: row 31 came in after row 30 went out but
			// carries an earlier date, as Messages records a message to yourself.
			write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"messages":[`+
				`{"id":30,"guid":"out30","is_from_me":true},{"id":31,"guid":"in31"},{"id":29,"guid":"in29"}]}}`, request.ID))
			continue
		}
		if request.Method == "watch.subscribe" {
			switch scenario {
			case "backlog":
				// More rows than any fixed buffer, all before the subscribe response.
				for row := 1; row <= 300; row++ {
					write(message(row))
				}
			case "mutations":
				for _, m := range []string{
					`{"id":5,"guid":"t5","chat_guid":"c","sender":"+15550000000","is_reaction":true,"reaction_type":"laugh","is_reaction_add":true,"reacted_to_guid":"g1"}`,
					`{"id":6,"guid":"t6","chat_guid":"c","sender":"+15550000000","is_reaction":true,"reaction_type":"custom","reaction_emoji":"🦀","is_reaction_add":false,"reacted_to_guid":"g1"}`,
					`{"id":1,"guid":"g1","chat_guid":"c","type":"edited","text":"fixed"}`,
					`{"id":2,"guid":"g2","chat_guid":"c","type":"edited","text":""}`,
					`{"id":3,"guid":"g3","chat_guid":"c","type":"unsent"}`,
					`{"id":7,"guid":"g7","chat_guid":"c","text":"https://example.com","balloon_bundle_id":"com.apple.messages.URLBalloonProvider",` +
						`"attachments":[{"transfer_name":"7AB00049.pluginPayloadAttachment","original_path":"/a/7AB00049.pluginPayloadAttachment"}]}`,
					`{"id":8,"guid":"g8","chat_guid":"c","text":"\ufffc","attachments":[{"transfer_name":"cat.heic","original_path":"/a/cat.heic",` +
						`"mime_type":"image/heic","converted_path":"/c/cat.png","converted_mime_type":"image/png","is_sticker":true}]}`,
				} {
					write(`{"jsonrpc":"2.0","method":"message","params":{"subscription":7,"message":` + m + `}}`)
				}
			case "groups":
				// Group changes carry neither text nor files, and imsg names the user's own number,
				// not as theirs, for one chat.db records without a sender.
				for _, m := range []string{
					`{"id":1,"guid":"added","chat_guid":"c","sender":"+15550001111"}`,
					`{"id":2,"guid":"renamed","chat_guid":"c","sender":"+15559999999"}`,
					`{"id":3,"guid":"removed","chat_guid":"c","sender":"+15559999999"}`,
					`{"id":4,"guid":"photo","chat_guid":"c","sender":"+15550001111","is_from_me":true}`,
					`{"id":5,"guid":"blank","chat_guid":"c","sender":"+15550001111"}`,
				} {
					write(`{"jsonrpc":"2.0","method":"message","params":{"subscription":7,"message":` + m + `}}`)
				}
			case "error":
				// Row 1 has an attachment before its text.
				write(strings.Replace(message(1), `"text":"m1"`, `"text":"\ufffcm1"`, 1))
				write(`{"jsonrpc":"2.0","method":"error","params":{"subscription":7,"error":{"message":"database is locked"}}}`)
			}
			write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"subscription":7}}`, request.ID))
			continue
		}
		if calls := os.Getenv(fakeCalls); calls != "" && (request.Method == "read" || request.Method == "chats.delete") {
			f, err := os.OpenFile(filepath.Clean(calls), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the test's own temporary file
			if err != nil {
				panic(err)
			}
			_, _ = fmt.Fprintf(f, "%s %s\n", request.Method, request.Params.ChatGUID)
			_ = f.Close()
		}
		write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, request.ID))
	}
}

// chatDB writes a chat.db holding the given chats, each with a message at the given time unless
// that time is zero, and returns its path.
func chatDB(t *testing.T, chats map[int64]struct {
	guid string
	last time.Time
}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE chat (ROWID INTEGER PRIMARY KEY, guid TEXT)`,
		`CREATE TABLE chat_message_join (chat_id INTEGER, message_id INTEGER)`,
		`CREATE TABLE message (ROWID INTEGER PRIMARY KEY, date INTEGER)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	epoch := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	for id, c := range chats {
		if _, err := db.Exec(`INSERT INTO chat VALUES (?, ?)`, id, c.guid); err != nil {
			t.Fatal(err)
		}
		if c.last.IsZero() {
			continue
		}
		if _, err := db.Exec(`INSERT INTO message (date) VALUES (?)`, int64(c.last.Sub(epoch))); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO chat_message_join VALUES (?, last_insert_rowid())`, id); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func startFake(t *testing.T, scenario string) *core.Service {
	t.Helper()
	return startFakeWith(t, scenario, "")
}

func startFakeWith(t *testing.T, scenario, chatDB string) *core.Service {
	t.Helper()
	t.Setenv(fakeScenario, scenario)
	service, err := core.Start(zerolog.Nop(), core.Options{IMsgPath: os.Args[0], ChatDB: chatDB, Contacts: &contacts.Catalog{}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestWatchDeliversBacklogInOrder(t *testing.T) {
	service := startFake(t, "backlog")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var rows []int64
	err := service.Watch(ctx, 0, false, func(evt core.Event) {
		msg, _ := evt.(core.Message)
		rows = append(rows, msg.RowID)
		if len(rows) == 300 {
			cancel()
		}
	})
	if len(rows) != 300 {
		t.Fatalf("got %d of 300 messages before %v", len(rows), err)
	}
	for i, row := range rows {
		if row != int64(i+1) {
			t.Fatalf("message %d is row %d, want %d", i, row, i+1)
		}
	}
}

func TestWatchEndsOnImsgError(t *testing.T) {
	service := startFake(t, "error")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var texts []string
	err := service.Watch(ctx, 0, false, func(evt core.Event) {
		msg, _ := evt.(core.Message)
		texts = append(texts, msg.Text)
	})
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("Watch returned %v, want imsg's error", err)
	}
	if strings.Join(texts, ",") != "m1" {
		t.Fatalf("delivered %q before the error, want m1 without its attachment marker", texts)
	}
}

func TestMarkReadThroughNeedsEveryInboundMessageSeen(t *testing.T) {
	service := startFakeWith(t, "receipts", chatDB(t, map[int64]struct {
		guid string
		last time.Time
	}{62: {"any;-;+15550000000", time.Now()}}))
	for _, tc := range []struct {
		seen string
		want bool
	}{
		{"in31", true},
		{"out30", false},
		{"in29", false},
		{"unknown", false},
	} {
		marked, err := service.MarkReadThrough(t.Context(), "any;-;+15550000000", tc.seen)
		if err != nil {
			t.Fatal(err)
		}
		if marked != tc.want {
			t.Errorf("read through %s: marked %v, want %v", tc.seen, marked, tc.want)
		}
	}
}

func TestWatchClassifiesEveryKindOfRow(t *testing.T) {
	service := startFake(t, "mutations")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var got []string
	_ = service.Watch(ctx, 0, false, func(evt core.Event) {
		switch evt := evt.(type) {
		case core.Tapback:
			got = append(got, fmt.Sprintf("tapback %s on %s removed=%v", evt.Emoji, evt.Target, evt.Removed))
		case core.Edit:
			got = append(got, fmt.Sprintf("edit %s to %q", evt.GUID, evt.Text))
		case core.Unsend:
			got = append(got, "unsend "+evt.GUID)
		case core.Message:
			got = append(got, fmt.Sprintf("message %s %q from %s files %v", evt.GUID, evt.Text, evt.App, evt.Attachments))
		default:
			got = append(got, fmt.Sprintf("%T", evt))
		}
		if len(got) == 7 {
			cancel()
		}
	})
	want := []string{
		"tapback 😂 on g1 removed=false",
		"tapback 🦀 on g1 removed=true",
		`edit g1 to "fixed"`,
		"unsend g2",
		"unsend g3",
		// A link's preview is Messages' own rendering, not a file anyone sent.
		`message g7 "https://example.com" from com.apple.messages.URLBalloonProvider files []`,
		`message g8 "" from  files [{cat.heic image/png /c/cat.png true}]`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAddressSplitsTheFilterCategory(t *testing.T) {
	for handle, want := range map[string][2]string{
		"+14175413549(smsft_rm)": {"+14175413549", "smsft_rm"},
		"97790(smsft)(smsft)":    {"97790", "smsft"},
		"+18598593641(smsfp)":    {"+18598593641", "smsfp"},
		"+15552013344":           {"+15552013344", ""},
		"dana.w@example.com":     {"dana.w@example.com", ""},
	} {
		if address, category := core.Address(handle); [2]string{address, category} != want {
			t.Errorf("Address(%q) = %q, %q; want %q, %q", handle, address, category, want[0], want[1])
		}
	}
}

func TestWatchDescribesGroupChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// {"uat": true}: Messages shows the change without saying who made it.
	uat, err := hex.DecodeString("62706c6973743030d101025375617409080b0f0000000000000101000000000000000300000000000000000000000000000010")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT)`, nil},
		{`CREATE TABLE message (ROWID INTEGER PRIMARY KEY, guid TEXT, item_type INTEGER, group_action_type INTEGER,
			handle_id INTEGER, other_handle INTEGER, group_title TEXT, message_summary_info BLOB)`, nil},
		{`INSERT INTO handle VALUES (1, '+15550001111'), (2, '+15550002222')`, nil},
		{`INSERT INTO message (guid, item_type, group_action_type, handle_id, other_handle, group_title, message_summary_info)
			VALUES ('added', 1, 0, 1, 2, NULL, NULL), ('renamed', 2, 0, 0, 0, 'Fam', NULL),
			('removed', 1, 1, 0, 2, NULL, ?), ('photo', 3, 1, 1, 0, NULL, NULL), ('blank', 0, 0, 1, 0, NULL, NULL)`, []any{uat}},
	} {
		if _, err := db.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}

	service := startFakeWith(t, "groups", path)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var got []string
	_ = service.Watch(ctx, 0, false, func(evt core.Event) {
		msg, _ := evt.(core.Message)
		got = append(got, fmt.Sprintf("%s from=%q me=%v announcement=%v %q", msg.GUID, msg.Sender, msg.IsFromMe, msg.Announcement, msg.Text))
		if len(got) == 5 {
			cancel()
		}
	})
	want := []string{
		`added from="+15550001111" me=false announcement=true "added +15550002222 to the conversation"`,
		`renamed from="" me=true announcement=true "named the conversation “Fam”"`,
		`removed from="" me=false announcement=true "+15550002222 was removed from the conversation"`,
		`photo from="+15550001111" me=true announcement=true "changed the group photo"`,
		`blank from="+15550001111" me=false announcement=false ""`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestConversationJoinsAPersonsFilteredChats(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv(fakeCalls, calls)
	service := startFakeWith(t, "conversation", chatDB(t, map[int64]struct {
		guid string
		last time.Time
	}{
		62: {"any;-;+15550000000", time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)},
		63: {"any;-;+15550000000(smsft)", time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)},
		// Deleted: no message left outside Recently Deleted.
		64: {"any;-;+15550000000(smsfp)", time.Time{}},
		// A number that only starts the same is someone else.
		65: {"any;-;+155500000001", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)},
	}))
	ctx := t.Context()

	conversation, err := service.Conversation(ctx, "any;-;+15550000000(smsft)")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%s %v", conversation.ID, conversation.Chats), "any;-;+15550000000 [any;-;+15550000000 any;-;+15550000000(smsft)]"; got != want {
		t.Errorf("conversation %s; want %s", got, want)
	}

	history, err := service.History(ctx, "any;-;+15550000000", time.Time{}, time.Time{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	guids := make([]string, 0, len(history))
	for _, m := range history {
		guids = append(guids, m.GUID)
	}
	if got, want := strings.Join(guids, ","), "filtered30,plain40"; got != want {
		t.Errorf("newest 2 of the conversation, oldest first: %s; want %s", got, want)
	}

	// Sends go to the chat the filter left alone, even though the filtered one was named.
	target, err := service.Target(ctx, "any;-;+15550000000(smsft)")
	if err != nil {
		t.Fatal(err)
	}
	if target != "any;-;+15550000000" {
		t.Errorf("target %s; want any;-;+15550000000", target)
	}

	if err = service.MarkRead(ctx, "any;-;+15550000000(smsft)"); err != nil {
		t.Fatal(err)
	}
	if err = service.DeleteChat(ctx, "any;-;+15550000000"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Clean(calls))
	if err != nil {
		t.Fatal(err)
	}
	if want := "read any;-;+15550000000\nread any;-;+15550000000(smsft)\nchats.delete any;-;+15550000000\nchats.delete any;-;+15550000000(smsft)\n"; string(got) != want {
		t.Errorf("marked read, then deleted\n%swant\n%s", got, want)
	}
}

func TestConversationIDDropsTheFilterCategory(t *testing.T) {
	for guid, want := range map[string]string{
		"any;-;+15550000000(smsft_rm)": "any;-;+15550000000",
		"any;-;+15550000000":           "any;-;+15550000000",
		"any;-;24215(smsft_or)":        "any;-;24215",
		"any;+;chat123(x)":             "any;+;chat123(x)",
	} {
		if got := core.ConversationID(guid); got != want {
			t.Errorf("ConversationID(%q) = %q; want %q", guid, got, want)
		}
	}
}
