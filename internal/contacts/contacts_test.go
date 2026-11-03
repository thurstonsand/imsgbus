package contacts_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/thurstonsand/imsgbus/internal/contacts"
)

type person struct {
	first, last, nickname, organization string
	phones, emails                      []string
	linkID                              string
	preferred, preferredPhoto           bool
	// thumbnail and image are stored as Contacts stores them, behind their storage marker.
	thumbnail, image []byte
}

// writeStore writes people to a store with Contacts' own table and column names, in WAL mode, and
// keeps its connection open so the rows stay in the write-ahead log, as they do on a live Mac.
func writeStore(t *testing.T, path string, people ...person) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	exec(t, db, `
		CREATE TABLE Z_PRIMARYKEY (Z_ENT INTEGER PRIMARY KEY, Z_NAME VARCHAR);
		INSERT INTO Z_PRIMARYKEY VALUES (19, 'ABCDGroup'), (22, 'ABCDContact');
		CREATE TABLE ZABCDRECORD (Z_PK INTEGER PRIMARY KEY, Z_ENT INTEGER, ZDISPLAYFLAGS INTEGER,
			ZPREFERREDFORLINKNAME INTEGER, ZFIRSTNAME VARCHAR, ZLASTNAME VARCHAR, ZMIDDLENAME VARCHAR,
			ZNICKNAME VARCHAR, ZORGANIZATION VARCHAR, ZLINKID VARCHAR, ZNAME VARCHAR,
			ZPREFERREDFORLINKPHOTO INTEGER, ZTHUMBNAILIMAGEDATA BLOB, ZIMAGEDATA BLOB);
		CREATE TABLE ZABCDPHONENUMBER (Z_PK INTEGER PRIMARY KEY, ZORDERINGINDEX INTEGER, ZOWNER INTEGER,
			ZFULLNUMBER VARCHAR, ZLABEL VARCHAR);
		CREATE TABLE ZABCDEMAILADDRESS (Z_PK INTEGER PRIMARY KEY, ZORDERINGINDEX INTEGER, ZOWNER INTEGER,
			ZADDRESS VARCHAR, ZLABEL VARCHAR);
		INSERT INTO ZABCDRECORD (Z_ENT, ZNAME) VALUES (19, 'Family');`)
	for _, p := range people {
		addPerson(t, db, p)
	}
	return db
}

func addPerson(t *testing.T, db *sql.DB, p person) {
	t.Helper()
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	res, err := db.Exec(`INSERT INTO ZABCDRECORD (Z_ENT, ZDISPLAYFLAGS, ZPREFERREDFORLINKNAME, ZFIRSTNAME,
		ZLASTNAME, ZNICKNAME, ZORGANIZATION, ZLINKID, ZPREFERREDFORLINKPHOTO, ZTHUMBNAILIMAGEDATA, ZIMAGEDATA)
		VALUES (22, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.preferred, nullable(p.first), nullable(p.last), nullable(p.nickname), nullable(p.organization), nullable(p.linkID),
		p.preferredPhoto, p.thumbnail, p.image)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for i, phone := range p.phones {
		exec(t, db, `INSERT INTO ZABCDPHONENUMBER (ZORDERINGINDEX, ZOWNER, ZFULLNUMBER, ZLABEL)
			VALUES (?, ?, ?, '_$!<Mobile>!$_')`, i, owner, phone)
	}
	for i, email := range p.emails {
		exec(t, db, `INSERT INTO ZABCDEMAILADDRESS (ZORDERINGINDEX, ZOWNER, ZADDRESS, ZLABEL)
			VALUES (?, ?, ?, 'school')`, i, owner, email)
	}
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// addressBook writes a root store holding only a handle-less owner card, and one store per source,
// in order, and opens the catalog over them.
func addressBook(t *testing.T, sources ...[]person) (*contacts.Catalog, []*sql.DB) {
	t.Helper()
	dir := t.TempDir()
	writeStore(t, filepath.Join(dir, "AddressBook-v22.abcddb"), person{first: "Alex"})
	dbs := make([]*sql.DB, len(sources))
	for i, people := range sources {
		dbs[i] = writeStore(t, filepath.Join(dir, "Sources", fmt.Sprintf("%02d-SOURCE", i), "AddressBook-v22.abcddb"), people...)
	}
	catalog, err := contacts.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, dbs
}

func TestHandleInTwoSourcesResolvesConsistently(t *testing.T) {
	icloud := []person{
		{first: "Dana", last: "Whitfield", phones: []string{"(555) 201-3344"}},
		{first: "Robert", last: "Okafor", phones: []string{"555-201-7788"}},
		{first: "Priya", last: "Natarajan", phones: []string{"555 201 9900"}, linkID: "LINK-1", preferred: true},
	}
	google := []person{
		{first: "Dana", last: "Whitfield", phones: []string{"+1 555 201 3344"}, emails: []string{"Dana.W@Example.com"}},
		{first: "Bobby", last: "Okafor", phones: []string{"+15552017788"}},
		{first: "P", last: "Natarajan", phones: []string{"+15552019900"}, linkID: "LINK-1"},
	}
	for _, order := range [][][]person{{icloud, google}, {google, icloud}} {
		catalog, _ := addressBook(t, order...)

		if name, ok := catalog.Name("+15552013344"); name != "Dana Whitfield" || !ok {
			t.Errorf("Name(Dana's number) = %q, %v; want Dana Whitfield", name, ok)
		}
		dana, err := catalog.Resolve("Dana Whitfield")
		if err != nil {
			t.Fatalf("Resolve(Dana Whitfield): %v", err)
		}
		want := []contacts.Handle{{Value: "+15552013344", Label: "mobile"}, {Value: "dana.w@example.com", Label: "school"}}
		if !slices.Equal(dana.Handles, want) {
			t.Errorf("Dana's handles = %v; want %v", dana.Handles, want)
		}

		// Two cards giving one number different names: neither wins, whichever store loads last.
		if name, ok := catalog.Name("+15552017788"); ok {
			t.Errorf("Name(number on Robert and Bobby) = %q; want no name", name)
		}
		// Linked cards take the preferred card's name, whichever store loads last.
		if name, ok := catalog.Name("+15552019900"); name != "Priya Natarajan" || !ok {
			t.Errorf("Name(linked number) = %q, %v; want Priya Natarajan", name, ok)
		}
	}
}

func TestPhotosReadInlineAndExternal(t *testing.T) {
	inline := func(data string) []byte { return append([]byte{1}, data...) }
	external := func(name string) []byte { return append(append([]byte{2}, name...), 0) }
	dir := t.TempDir()
	writeStore(t, filepath.Join(dir, "AddressBook-v22.abcddb"))
	source := filepath.Join(dir, "Sources", "00-SOURCE")
	writeStore(t, filepath.Join(source, "AddressBook-v22.abcddb"),
		person{first: "Thumb", last: "Over Image", phones: []string{"555-301-0001"}, thumbnail: inline("thumbnail"), image: inline("full image")},
		person{first: "External", last: "File", phones: []string{"555-301-0002"}, image: external("8044FDE1-C993")},
		person{first: "Not", last: "Downloaded", phones: []string{"555-301-0003"}, thumbnail: external("0E04AA96-AAE8")},
		person{first: "Linked", last: "Plain", phones: []string{"555-301-0004"}, linkID: "LINK-2", preferred: true, image: inline("plain card")},
		person{first: "Linked", last: "Chosen", phones: []string{"555-301-0005"}, linkID: "LINK-2", preferredPhoto: true, image: inline("chosen card")},
		person{first: "No", last: "Photo", phones: []string{"555-301-0006"}},
	)
	data := filepath.Join(source, ".AddressBook-v22_SUPPORT", "_EXTERNAL_DATA")
	if err := os.MkdirAll(data, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "8044FDE1-C993"), []byte("external image"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := contacts.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	for handle, want := range map[string]string{
		"+15553010001": "thumbnail",
		"+15553010002": "external image",
		"+15553010003": "",
		// Linked cards take the photo of the card preferred for it, not the one preferred for the name.
		"+15553010004": "chosen card",
		"+15553010006": "",
	} {
		contact, ok := catalog.Lookup(handle)
		if !ok || string(contact.Photo) != want {
			t.Errorf("Lookup(%s) = photo %q, %v; want %q", handle, contact.Photo, ok, want)
		}
	}
}

func TestPhoneFormatsMatch(t *testing.T) {
	catalog, _ := addressBook(t, []person{
		{first: "Ines", last: "Calloway", phones: []string{"(555) 123-4567"}},
		{first: "Theo", last: "Brandt", phones: []string{"+1 555 765 4321"}},
		{first: "Mara", last: "Quist", phones: []string{"5550001111"}, emails: []string{"Mara@Example.COM"}},
	})
	for name, number := range map[string]string{"Ines Calloway": "5551234567", "Theo Brandt": "5557654321", "Mara Quist": "5550001111"} {
		formats := []string{
			fmt.Sprintf("(%s) %s-%s", number[:3], number[3:6], number[6:]),
			fmt.Sprintf("+1 %s %s %s", number[:3], number[3:6], number[6:]),
			number,
			"+1" + number,
		}
		for _, handle := range formats {
			if got, ok := catalog.Name(handle); got != name || !ok {
				t.Errorf("Name(%q) = %q, %v; want %s", handle, got, ok, name)
			}
		}
	}
	if got, ok := catalog.Name("mara@example.com"); got != "Mara Quist" || !ok {
		t.Errorf("Name(lowercased email) = %q, %v; want Mara Quist", got, ok)
	}
}

var alexes = []person{
	{first: "Alex", last: "Moreau", organization: "Lumen Labs", phones: []string{"5553100001"}},
	{first: "Alex", last: "Varga", phones: []string{"5553100002"}},
	{first: "Élodie", last: "Durand", nickname: "Lo", phones: []string{"5553100003"}},
}

func TestFirstNameAmbiguityListsCandidates(t *testing.T) {
	catalog, _ := addressBook(t, alexes)
	_, err := catalog.Resolve("alex")
	var ambiguous *contacts.AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(alex) = %v; want an AmbiguousError", err)
	}
	if len(ambiguous.Candidates) != 2 {
		t.Errorf("candidates = %v; want Alex Moreau and Alex Varga", ambiguous.Candidates)
	}
	for _, want := range []string{"Alex Moreau (Lumen Labs)", "Alex Varga"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not list %q", err, want)
		}
	}
	if _, err := catalog.Resolve("Sam"); !errors.Is(err, contacts.ErrNotFound) {
		t.Errorf("Resolve(Sam) = %v; want ErrNotFound", err)
	}
}

func TestFullNameBeatsFirstNameAmbiguity(t *testing.T) {
	catalog, _ := addressBook(t, append([]person{{first: "Alex", phones: []string{"5553100004"}}}, alexes...))
	for query, want := range map[string]string{
		// A card named only "Alex" is a full-name match, ahead of both Alexes' first names.
		"alex":          "Alex",
		"ALEX VARGA":    "Alex Varga",
		"elodie durand": "Élodie Durand",
		"lo":            "Élodie Durand",
	} {
		contact, err := catalog.Resolve(query)
		if err != nil || contact.Name != want {
			t.Errorf("Resolve(%q) = %q, %v; want %s", query, contact.Name, err, want)
		}
	}
}

func TestWatchReloadsOnChange(t *testing.T) {
	catalog, dbs := addressBook(t, []person{{first: "Ines", last: "Calloway", phones: []string{"5551234567"}}})
	reloaded := make(chan struct{}, 1)
	if err := catalog.Watch(t.Context(), zerolog.Nop(), func() { reloaded <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	addPerson(t, dbs[0], person{first: "Theo", last: "Brandt", phones: []string{"5557654321"}})
	select {
	case <-reloaded:
	case <-time.After(10 * time.Second):
		t.Fatal("the catalog never reloaded after a card was added")
	}
	if name, _ := catalog.Name("+15557654321"); name != "Theo Brandt" {
		t.Fatalf("Name(added card's number) = %q after the reload; want Theo Brandt", name)
	}
	// SQLite changes the mode of a store's shared-memory file when it reads it, so a reload sees
	// that; it must not set off the next reload.
	var seq int
	var schema, store string
	if err := dbs[0].QueryRow("PRAGMA database_list").Scan(&seq, &schema, &store); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store+"-shm", 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
		t.Fatal("the catalog reloaded again with nothing changed")
	case <-time.After(5 * time.Second):
	}
}
