package contacts_test

import (
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
	"github.com/thurstonsand/imsgbus/internal/contacts/contactstest"
)

func TestHandleInTwoSourcesResolvesConsistently(t *testing.T) {
	icloud := []contactstest.Person{
		{First: "Dana", Last: "Whitfield", Phones: []string{"(555) 201-3344"}},
		{First: "Robert", Last: "Okafor", Phones: []string{"555-201-7788"}},
		{First: "Priya", Last: "Natarajan", Phones: []string{"555 201 9900"}, LinkID: "LINK-1", Preferred: true, ID: "B-PRIYA"},
	}
	google := []contactstest.Person{
		{First: "Dana", Last: "Whitfield", Phones: []string{"+1 555 201 3344"}, Emails: []string{"Dana.W@Example.com"}},
		{First: "Bobby", Last: "Okafor", Phones: []string{"+15552017788"}},
		{First: "P", Last: "Natarajan", Phones: []string{"+15552019900"}, LinkID: "LINK-1", ID: "A-PRIYA"},
	}
	for _, order := range [][][]contactstest.Person{{icloud, google}, {google, icloud}} {
		catalog, _ := contactstest.AddressBook(t, order...)

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
		// Linked cards keep one ID, whichever store loads first.
		if priya, ok := catalog.ByID("A-PRIYA"); priya.Name != "Priya Natarajan" || !ok {
			t.Errorf("ByID(A-PRIYA) = %q, %v; want Priya Natarajan", priya.Name, ok)
		}
	}
}

func TestPrimaryStandsForEveryHandleOfAContact(t *testing.T) {
	catalog, _ := contactstest.AddressBook(t, []contactstest.Person{
		{First: "Ana", Last: "Reyes", Phones: []string{"555-201-1111", "555-201-0002"}, Emails: []string{"ana@example.com"}},
		{First: "Ben", Last: "Ito", Phones: []string{"555-201-3333"}, Emails: []string{"ben@example.com"}},
		{First: "Cal", Last: "Moss", Phones: []string{"(555) 201-3333"}},
	})
	for handle, want := range map[string]string{
		// The least phone number, not the first one listed, from any of the contact's handles.
		"ana@example.com": "+15552010002",
		"+15552011111":    "+15552010002",
		// A number another contact also lists stands for neither, so Ben falls back to his email.
		"ben@example.com": "ben@example.com",
	} {
		if got, ok := catalog.Primary(handle); got != want || !ok {
			t.Errorf("Primary(%q) = %q, %v; want %q", handle, got, ok, want)
		}
	}
	if got, ok := catalog.Primary("+15552013333"); ok {
		t.Errorf("Primary(number on Ben and Cal) = %q; want none", got)
	}
}

func TestPhotosReadInlineAndExternal(t *testing.T) {
	inline := func(data string) []byte { return append([]byte{1}, data...) }
	external := func(name string) []byte { return append(append([]byte{2}, name...), 0) }
	dir := t.TempDir()
	contactstest.WriteStore(t, filepath.Join(dir, "AddressBook-v22.abcddb"))
	source := filepath.Join(dir, "Sources", "00-SOURCE")
	contactstest.WriteStore(t, filepath.Join(source, "AddressBook-v22.abcddb"),
		contactstest.Person{First: "Thumb", Last: "Over Image", Phones: []string{"555-301-0001"}, Thumbnail: inline("thumbnail"), Image: inline("full image")},
		contactstest.Person{First: "External", Last: "File", Phones: []string{"555-301-0002"}, Image: external("8044FDE1-C993")},
		contactstest.Person{First: "Not", Last: "Downloaded", Phones: []string{"555-301-0003"}, Thumbnail: external("0E04AA96-AAE8")},
		contactstest.Person{First: "Linked", Last: "Plain", Phones: []string{"555-301-0004"}, LinkID: "LINK-2", Preferred: true, Image: inline("plain card")},
		contactstest.Person{First: "Linked", Last: "Chosen", Phones: []string{"555-301-0005"}, LinkID: "LINK-2", PreferredPhoto: true, Image: inline("chosen card")},
		contactstest.Person{First: "No", Last: "Photo", Phones: []string{"555-301-0006"}},
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
	catalog, _ := contactstest.AddressBook(t, []contactstest.Person{
		{First: "Ines", Last: "Calloway", Phones: []string{"(555) 123-4567"}},
		{First: "Theo", Last: "Brandt", Phones: []string{"+1 555 765 4321"}},
		{First: "Mara", Last: "Quist", Phones: []string{"5550001111"}, Emails: []string{"Mara@Example.COM"}},
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

var alexes = []contactstest.Person{
	{First: "Alex", Last: "Moreau", Organization: "Lumen Labs", Phones: []string{"5553100001"}},
	{First: "Alex", Last: "Varga", Phones: []string{"5553100002"}},
	{First: "Élodie", Last: "Durand", Nickname: "Lo", Phones: []string{"5553100003"}},
}

func TestFirstNameAmbiguityListsCandidates(t *testing.T) {
	catalog, _ := contactstest.AddressBook(t, alexes)
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
	catalog, _ := contactstest.AddressBook(t, append([]contactstest.Person{{First: "Alex", Phones: []string{"5553100004"}}}, alexes...))
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
	catalog, dbs := contactstest.AddressBook(t, []contactstest.Person{{First: "Ines", Last: "Calloway", Phones: []string{"5551234567"}}})
	reloaded := make(chan struct{}, 1)
	if err := catalog.Watch(t.Context(), zerolog.Nop(), func() { reloaded <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	contactstest.AddPerson(t, dbs[0], contactstest.Person{First: "Theo", Last: "Brandt", Phones: []string{"5557654321"}})
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
