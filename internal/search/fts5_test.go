package search_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/thurstonsand/imsgbus/internal/search"
)

var start = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func backend(t *testing.T, docs ...search.Document) search.Backend {
	t.Helper()
	f, err := search.OpenFTS5(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Index(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	return f
}

func doc(row int64, chat string, minutes int, text string) search.Document {
	return search.Document{
		RowID:    row,
		GUID:     fmt.Sprintf("guid-%d", row),
		ChatGUID: chat,
		Sender:   fmt.Sprintf("+155501000%d", row%10),
		Time:     start.Add(time.Duration(minutes) * time.Minute),
		Text:     text,
	}
}

// rows runs q and returns the hits' row IDs, failing the test if the search fails.
func rows(t *testing.T, b search.Backend, q search.Query) []int64 {
	t.Helper()
	if q.Limit == 0 {
		q.Limit = 100
	}
	res, err := b.Search(t.Context(), q)
	if err != nil {
		t.Fatalf("Search(%+v): %v", q, err)
	}
	ids := make([]int64, len(res.Hits))
	for i, h := range res.Hits {
		ids[i] = h.Document.RowID
	}
	return ids
}

func TestAccentInsensitiveMatch(t *testing.T) {
	b := backend(t,
		doc(1, "chat-a", 1, "Rendez-vous au Café Lumière?"),
		doc(2, "chat-a", 2, "the cafe on 5th is closed"),
		doc(3, "chat-a", 3, "nothing to see"),
	)
	for _, text := range []string{"cafe", "CAFÉ", "lumiere"} {
		want := []int64{2, 1}
		if text == "lumiere" {
			want = []int64{1}
		}
		if got := rows(t, b, search.Query{Text: text}); !slices.Equal(got, want) {
			t.Errorf("Search(%q) = %v; want %v", text, got, want)
		}
	}
}

func TestPhraseAndWords(t *testing.T) {
	b := backend(t,
		doc(1, "chat-a", 1, "good morning sunshine"),
		doc(2, "chat-a", 2, "morning! good to hear"),
		doc(3, "chat-a", 3, "got two cheeseburgers"),
	)
	if got := rows(t, b, search.Query{Text: "good morning"}); !slices.Equal(got, []int64{2, 1}) {
		t.Errorf("words good morning = %v; want both messages holding both words", got)
	}
	if got := rows(t, b, search.Query{Text: `"good morning"`}); !slices.Equal(got, []int64{1}) {
		t.Errorf(`phrase "good morning" = %v; want only the message with the words together`, got)
	}
	if got := rows(t, b, search.Query{Text: "burger"}); !slices.Equal(got, []int64{3}) {
		t.Errorf("partial word burger = %v; want the cheeseburgers message", got)
	}
	if _, err := b.Search(t.Context(), search.Query{Text: "ok burger", Limit: 10}); err == nil {
		t.Error("a two-letter term matched silently; want an error")
	}
}

func TestChatAndDateFilters(t *testing.T) {
	b := backend(t,
		doc(1, "chat-a", 10, "dinner at eight"),
		doc(2, "chat-b", 20, "dinner tomorrow"),
		doc(3, "chat-a", 30, "dinner again"),
		doc(4, "chat-a", 40, "dinner was great"),
	)
	if got := rows(t, b, search.Query{Text: "dinner", ChatGUID: "chat-a"}); !slices.Equal(got, []int64{4, 3, 1}) {
		t.Errorf("chat-a = %v; want 4, 3, 1", got)
	}
	q := search.Query{Text: "dinner", Since: start.Add(20 * time.Minute), Before: start.Add(40 * time.Minute)}
	if got := rows(t, b, q); !slices.Equal(got, []int64{3, 2}) {
		t.Errorf("since 20m before 40m = %v; want 3, 2: since is inclusive, before exclusive", got)
	}
	q.ChatGUID = "chat-b"
	if got := rows(t, b, q); !slices.Equal(got, []int64{2}) {
		t.Errorf("chat-b since 20m before 40m = %v; want 2", got)
	}
}

func TestSenderFilters(t *testing.T) {
	mine := doc(4, "chat-a", 40, "dinner was great")
	mine.IsFromMe = true
	b := backend(t,
		doc(1, "chat-a", 10, "dinner at eight"),
		doc(2, "chat-b", 20, "dinner tomorrow"),
		doc(3, "chat-a", 30, "dinner again"),
		mine,
	)
	// doc gives row N the sender +155501000N.
	if got := rows(t, b, search.Query{Text: "dinner", From: []string{"+1555010001", "+1555010003"}}); !slices.Equal(got, []int64{3, 1}) {
		t.Errorf("from two handles = %v; want 3, 1", got)
	}
	if got := rows(t, b, search.Query{Text: "dinner", FromMe: true}); !slices.Equal(got, []int64{4}) {
		t.Errorf("from me = %v; want 4", got)
	}
}

func TestCursorPagesThroughEveryHitOnce(t *testing.T) {
	var docs []search.Document
	for row := int64(1); row <= 23; row++ {
		// Three messages share each minute, so a page can end partway through a timestamp.
		docs = append(docs, doc(row, "chat-a", int(row/3), "see you soon"))
	}
	docs = append(docs, doc(99, "chat-a", 5, "unrelated"))
	b := backend(t, docs...)

	var got []int64
	q := search.Query{Text: "soon", Limit: 5}
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("paging never ended")
		}
		res, err := b.Search(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range res.Hits {
			got = append(got, h.Document.RowID)
		}
		if res.Next == "" {
			break
		}
		q.Cursor = res.Next
	}
	var want []int64
	for row := int64(23); row >= 1; row-- {
		want = append(want, row)
	}
	if !slices.Equal(got, want) {
		t.Errorf("pages = %v; want every row once, newest first: %v", got, want)
	}
}

func TestReindexingReplacesTheRow(t *testing.T) {
	b := backend(t, doc(7, "chat-a", 1, "meet at the station"))
	edited := doc(7, "chat-a", 1, "meet at the library")
	if err := b.Index(t.Context(), []search.Document{edited, edited}); err != nil {
		t.Fatal(err)
	}
	if got := rows(t, b, search.Query{Text: "meet"}); !slices.Equal(got, []int64{7}) {
		t.Errorf("meet = %v; want row 7 once", got)
	}
	if got := rows(t, b, search.Query{Text: "station"}); len(got) != 0 {
		t.Errorf("station = %v; want nothing once the edit replaced it", got)
	}
	if last, err := b.LastRowID(t.Context()); last != 7 || err != nil {
		t.Errorf("LastRowID = %d, %v; want 7", last, err)
	}
	if err := b.Remove(t.Context(), []string{edited.GUID}); err != nil {
		t.Fatal(err)
	}
	if got := rows(t, b, search.Query{Text: "library"}); len(got) != 0 {
		t.Errorf("library = %v; want nothing once removed", got)
	}
}

func TestOpensARelativePath(t *testing.T) {
	t.Chdir(t.TempDir())
	f, err := search.OpenFTS5("search.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := os.Stat("search.db"); err != nil {
		t.Fatalf("index is not at ./search.db: %v", err)
	}
}
