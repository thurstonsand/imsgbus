// Package contactstest writes address books the way Contacts stores them, for tests.
package contactstest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver

	"github.com/thurstonsand/imsgbus/internal/contacts"
)

// Person is one contact card.
type Person struct {
	First, Last, Nickname, Organization string
	Phones, Emails                      []string
	LinkID                              string
	Preferred, PreferredPhoto           bool
	// Thumbnail and Image are stored as Contacts stores them, behind their storage marker.
	Thumbnail, Image []byte
	// ID is the card's unique ID; a random one when empty.
	ID string
}

// WriteStore writes people to a store with Contacts' own table and column names, in WAL mode, and
// keeps its connection open so the rows stay in the write-ahead log, as they do on a live Mac.
func WriteStore(t *testing.T, path string, people ...Person) *sql.DB {
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
			ZPREFERREDFORLINKPHOTO INTEGER, ZTHUMBNAILIMAGEDATA BLOB, ZIMAGEDATA BLOB, ZUNIQUEID VARCHAR);
		CREATE TABLE ZABCDPHONENUMBER (Z_PK INTEGER PRIMARY KEY, ZORDERINGINDEX INTEGER, ZOWNER INTEGER,
			ZFULLNUMBER VARCHAR, ZLABEL VARCHAR);
		CREATE TABLE ZABCDEMAILADDRESS (Z_PK INTEGER PRIMARY KEY, ZORDERINGINDEX INTEGER, ZOWNER INTEGER,
			ZADDRESS VARCHAR, ZLABEL VARCHAR);
		INSERT INTO ZABCDRECORD (Z_ENT, ZNAME) VALUES (19, 'Family');`)
	for _, p := range people {
		AddPerson(t, db, p)
	}
	return db
}

// AddPerson adds a card to a store WriteStore wrote.
func AddPerson(t *testing.T, db *sql.DB, p Person) {
	t.Helper()
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	id := p.ID
	if id == "" {
		id = uuid.NewString() + ":ABPerson"
	}
	res, err := db.Exec(`INSERT INTO ZABCDRECORD (Z_ENT, ZDISPLAYFLAGS, ZPREFERREDFORLINKNAME, ZFIRSTNAME,
		ZLASTNAME, ZNICKNAME, ZORGANIZATION, ZLINKID, ZPREFERREDFORLINKPHOTO, ZTHUMBNAILIMAGEDATA, ZIMAGEDATA, ZUNIQUEID)
		VALUES (22, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Preferred, nullable(p.First), nullable(p.Last), nullable(p.Nickname), nullable(p.Organization), nullable(p.LinkID),
		p.PreferredPhoto, p.Thumbnail, p.Image, id)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for i, phone := range p.Phones {
		exec(t, db, `INSERT INTO ZABCDPHONENUMBER (ZORDERINGINDEX, ZOWNER, ZFULLNUMBER, ZLABEL)
			VALUES (?, ?, ?, '_$!<Mobile>!$_')`, i, owner, phone)
	}
	for i, email := range p.Emails {
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

// AddressBook writes a root store holding only a handle-less owner card, and one store per source,
// in order, and opens the catalog over them.
func AddressBook(t *testing.T, sources ...[]Person) (*contacts.Catalog, []*sql.DB) {
	t.Helper()
	dir := t.TempDir()
	WriteStore(t, filepath.Join(dir, "AddressBook-v22.abcddb"), Person{First: "Alex"})
	dbs := make([]*sql.DB, len(sources))
	for i, people := range sources {
		dbs[i] = WriteStore(t, filepath.Join(dir, "Sources", fmt.Sprintf("%02d-SOURCE", i), "AddressBook-v22.abcddb"), people...)
	}
	catalog, err := contacts.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, dbs
}
