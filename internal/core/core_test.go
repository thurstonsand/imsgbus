package core_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

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
	t.Setenv(fakeScenario, scenario)
	service, err := core.Start(zerolog.Nop(), os.Args[0])
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
	err := service.Watch(ctx, 0, func(m core.Message) {
		rows = append(rows, m.RowID)
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
	err := service.Watch(ctx, 0, func(m core.Message) { texts = append(texts, m.Text) })
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
