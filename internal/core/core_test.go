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
		}
		if err := json.Unmarshal(in.Bytes(), &request); err != nil {
			panic(err)
		}
		switch request.Method {
		case "chats.list":
			write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"chats":[{"id":62,"guid":"any;-;+15550000000"}]}}`, request.ID))
			continue
		case "messages.history":
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
		write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, request.ID))
	}
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
	service := startFake(t, "receipts")
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
