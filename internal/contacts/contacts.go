// Package contacts reads this Mac's address book, so names stand in for handles: a handle shows as
// its contact's name, and a name resolves to exactly one contact or fails.
package contacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/fsnotify/fsnotify"
	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver
	"github.com/nyaruka/phonenumbers"
	"github.com/rs/zerolog"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// storeName is the Core Data store Contacts keeps at the address book's root and in each source.
const storeName = "AddressBook-v22.abcddb"

// debounce is how long the stores must stay quiet before a watch reloads: Contacts writes a sync
// as a burst of transactions.
const debounce = 2 * time.Second

// ErrNotFound reports a name that matches no contact.
var ErrNotFound = errors.New("no contact matches")

// Contact is one person, gathered from every card that describes them.
type Contact struct {
	Name         string
	Nickname     string
	Organization string
	Handles      []Handle
	// Photo is the contact's picture as Contacts crops it, as JPEG or PNG; nil when it has none.
	Photo []byte

	// fullNames, nicknames and firstNames are the folded names Resolve matches, from every card.
	fullNames, nicknames, firstNames []string
}

// Handle is a phone number in E.164 or a lowercase email address, with its card's label, such as
// "mobile", "home" or "iPhone".
type Handle struct {
	Value string
	Label string
}

// AmbiguousError reports a name that matches more than one contact.
type AmbiguousError struct {
	Name       string
	Candidates []Contact
}

func (e *AmbiguousError) Error() string {
	descs := make([]string, len(e.Candidates))
	for i, c := range e.Candidates {
		var details []string
		if c.Nickname != "" {
			details = append(details, "nickname "+c.Nickname)
		}
		if c.Organization != "" && c.Organization != c.Name {
			details = append(details, c.Organization)
		}
		descs[i] = c.Name
		if len(details) > 0 {
			descs[i] += " (" + strings.Join(details, ", ") + ")"
		}
	}
	return fmt.Sprintf("%q matches %d contacts: %s", e.Name, len(descs), strings.Join(descs, "; "))
}

// Catalog is every contact in an address book directory.
type Catalog struct {
	dir string

	lock     sync.RWMutex
	contacts []Contact
	// owners maps a handle to the contacts that list it.
	owners map[string][]int
}

// Open loads the address book under dir, normally ~/Library/Application Support/AddressBook: its
// root store and the store of every source under Sources.
func Open(ctx context.Context, dir string) (*Catalog, error) {
	c := &Catalog{dir: dir}
	return c, c.Reload(ctx)
}

// Lookup is the contact that lists handle. A handle on several different contacts has none:
// picking one would make its name flip between them.
func (c *Catalog) Lookup(handle string) (Contact, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	owners := c.owners[Normalize(handle)]
	if len(owners) != 1 {
		return Contact{}, false
	}
	return c.contacts[owners[0]], true
}

// Len is how many contacts the catalog holds.
func (c *Catalog) Len() int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return len(c.contacts)
}

// Name is the name of the contact Lookup finds for handle.
func (c *Catalog) Name(handle string) (string, bool) {
	contact, ok := c.Lookup(handle)
	return contact.Name, ok
}

// Resolve finds the one contact a name means, case- and accent-insensitively. It tries full
// names, then nicknames, then first names, and stops at the first of those that matches anyone.
func (c *Catalog) Resolve(name string) (Contact, error) {
	query := fold(name)
	if query == "" {
		return Contact{}, errors.New("resolving a contact needs a name")
	}
	c.lock.RLock()
	defer c.lock.RUnlock()
	tiers := []func(Contact) []string{
		func(c Contact) []string { return c.fullNames },
		func(c Contact) []string { return c.nicknames },
		func(c Contact) []string { return c.firstNames },
	}
	for _, names := range tiers {
		var matches []Contact
		for _, contact := range c.contacts {
			if slices.Contains(names(contact), query) {
				matches = append(matches, contact)
			}
		}
		switch len(matches) {
		case 0:
			continue
		case 1:
			return matches[0], nil
		default:
			return Contact{}, &AmbiguousError{Name: name, Candidates: matches}
		}
	}
	return Contact{}, fmt.Errorf("%w %q", ErrNotFound, name)
}

// Reload rereads every store, replacing the catalog only once all of them have been read.
func (c *Catalog) Reload(ctx context.Context) error {
	paths, err := filepath.Glob(filepath.Join(c.dir, "Sources", "*", storeName))
	if err != nil {
		return err
	}
	// The root store comes first and sources in path order, so merged contacts take the same name on
	// every load.
	paths = append([]string{filepath.Join(c.dir, storeName)}, paths...)
	var cards []card
	for _, path := range paths {
		read, err := readStore(ctx, path)
		if err != nil {
			return fmt.Errorf("reading contacts from %s: %w", path, err)
		}
		cards = append(cards, read...)
	}
	contacts := merge(cards)
	owners := make(map[string][]int)
	for i, contact := range contacts {
		for _, h := range contact.Handles {
			owners[h.Value] = append(owners[h.Value], i)
		}
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	c.contacts, c.owners = contacts, owners
	return nil
}

// Watch reloads the catalog whenever a store changes, and calls reloaded after each reload, until
// ctx ends. A failed reload keeps the previous catalog and is logged, since the next change
// retries it.
func (c *Catalog) Watch(ctx context.Context, log zerolog.Logger, reloaded func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := c.watchStores(watcher); err != nil {
		_ = watcher.Close()
		return err
	}
	go func() {
		defer func() { _ = watcher.Close() }()
		quiet := time.NewTimer(debounce)
		quiet.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-watcher.Events:
				quiet.Reset(debounce)
			case err := <-watcher.Errors:
				log.Warn().Err(err).Msg("Watching contacts")
			case <-quiet.C:
				if err := c.Reload(ctx); err != nil {
					log.Err(err).Msg("Reloading contacts; keeping the previous catalog")
					continue
				}
				reloaded()
				// A source added since the last load has a store directory nobody watches yet.
				if err := c.watchStores(watcher); err != nil {
					log.Err(err).Msg("Watching contacts")
				}
			}
		}
	}()
	return nil
}

// watchStores watches the address book, its Sources directory and each source's directory: a
// store's changes land in its write-ahead log beside it.
func (c *Catalog) watchStores(watcher *fsnotify.Watcher) error {
	sources, err := filepath.Glob(filepath.Join(c.dir, "Sources", "*"))
	if err != nil {
		return err
	}
	for _, dir := range append([]string{c.dir, filepath.Join(c.dir, "Sources")}, sources...) {
		if err := watcher.Add(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// card is one ABCDContact record in one store.
type card struct {
	name, first, middle, last, nickname, organization string
	// linkID joins the cards Contacts has linked as one person, across sources.
	linkID string
	// preferred and preferredPhoto mark the linked card whose name and photo the person shows.
	preferred, preferredPhoto bool
	photo                     []byte
	handles                   []Handle
}

func readStore(ctx context.Context, path string) ([]card, error) {
	// mode=ro rather than immutable: Contacts keeps recent changes in the write-ahead log.
	db, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	// One transaction reads all three tables from the same snapshot.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT Z_PK, coalesce(ZFIRSTNAME, ''), coalesce(ZMIDDLENAME, ''), coalesce(ZLASTNAME, ''),
			coalesce(ZNICKNAME, ''), coalesce(ZORGANIZATION, ''), coalesce(ZDISPLAYFLAGS, 0),
			coalesce(ZLINKID, ''), coalesce(ZPREFERREDFORLINKNAME, 0), coalesce(ZPREFERREDFORLINKPHOTO, 0),
			coalesce(ZTHUMBNAILIMAGEDATA, ZIMAGEDATA)
		FROM ZABCDRECORD
		WHERE Z_ENT = (SELECT Z_ENT FROM Z_PRIMARYKEY WHERE Z_NAME = 'ABCDContact')
		ORDER BY Z_PK`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var cards []card
	index := make(map[int64]int)
	for rows.Next() {
		var pk, flags int64
		var cd card
		var image []byte
		if err = rows.Scan(&pk, &cd.first, &cd.middle, &cd.last, &cd.nickname, &cd.organization, &flags, &cd.linkID, &cd.preferred, &cd.preferredPhoto, &image); err != nil {
			return nil, err
		}
		if cd.photo, err = readImage(path, image); err != nil {
			return nil, fmt.Errorf("photo of contact %d: %w", pk, err)
		}
		cd.name = strings.TrimSpace(cd.first + " " + cd.last)
		// Bit 0 of the display flags is Contacts' "show as company".
		if cd.name == "" || flags&1 != 0 && cd.organization != "" {
			cd.name = cd.organization
		}
		if cd.name == "" {
			cd.name = cd.nickname
		}
		if cd.name == "" {
			continue
		}
		index[pk] = len(cards)
		cards = append(cards, cd)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, query := range []string{
		`SELECT ZOWNER, ZFULLNUMBER, coalesce(ZLABEL, '') FROM ZABCDPHONENUMBER
			WHERE ZOWNER IS NOT NULL AND ZFULLNUMBER IS NOT NULL ORDER BY ZOWNER, ZORDERINGINDEX`,
		`SELECT ZOWNER, ZADDRESS, coalesce(ZLABEL, '') FROM ZABCDEMAILADDRESS
			WHERE ZOWNER IS NOT NULL AND ZADDRESS IS NOT NULL ORDER BY ZOWNER, ZORDERINGINDEX`,
	} {
		if err := readHandles(ctx, tx, query, cards, index); err != nil {
			return nil, err
		}
	}
	// A card without handles can neither name a sender nor be sent to.
	return slices.DeleteFunc(cards, func(cd card) bool { return len(cd.handles) == 0 }), nil
}

// readImage reads a Core Data image attribute stored beside the database when it is large: 0x01
// and the image, or 0x02 and the NUL-terminated name of its file under the store's external
// data directory. A file Contacts hasn't synced down yet reads as no image.
func readImage(store string, blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	switch blob[0] {
	case 1:
		return blob[1:], nil
	case 2:
		name := strings.TrimRight(string(blob[1:]), "\x00")
		support := "." + strings.TrimSuffix(filepath.Base(store), filepath.Ext(store)) + "_SUPPORT"
		root, err := os.OpenRoot(filepath.Join(filepath.Dir(store), support, "_EXTERNAL_DATA"))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()
		data, err := root.ReadFile(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return data, err
	}
	return nil, fmt.Errorf("unknown image storage marker %#x", blob[0])
}

func readHandles(ctx context.Context, tx *sql.Tx, query string, cards []card, index map[int64]int) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var owner int64
		var value, label string
		if err := rows.Scan(&owner, &value, &label); err != nil {
			return err
		}
		i, ok := index[owner]
		if !ok {
			continue
		}
		// Contacts stores its built-in labels wrapped, as in "_$!<Mobile>!$_".
		if inner, ok := strings.CutPrefix(label, "_$!<"); ok {
			label = strings.ToLower(strings.TrimSuffix(inner, ">!$_"))
		}
		if value = Normalize(value); value != "" {
			cards[i].handles = append(cards[i].handles, Handle{Value: value, Label: label})
		}
	}
	return rows.Err()
}

// merge gathers cards into contacts. Cards are one person when Contacts linked them, or when they
// share a handle and a name: the same person kept in two sources. Cards sharing a handle under
// different names stay apart, and Name gives that handle no name.
func merge(cards []card) []Contact {
	parent := make([]int, len(cards))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		// The earliest card roots its group, so a group's order never depends on merge order.
		parent[max(ra, rb)] = min(ra, rb)
	}
	first := make(map[string]int)
	for i, cd := range cards {
		keys := make([]string, 0, len(cd.handles)+1)
		if cd.linkID != "" {
			keys = append(keys, "link\n"+cd.linkID)
		}
		for _, h := range cd.handles {
			keys = append(keys, "handle\n"+h.Value+"\n"+fold(cd.name))
		}
		for _, key := range keys {
			if j, ok := first[key]; ok {
				union(j, i)
			} else {
				first[key] = i
			}
		}
	}

	var contacts []Contact
	group := make(map[int]int)
	for i, cd := range cards {
		root := find(i)
		g, ok := group[root]
		if !ok {
			g = len(contacts)
			group[root] = g
			contacts = append(contacts, Contact{Name: cd.name, Nickname: cd.nickname, Organization: cd.organization})
		}
		contact := &contacts[g]
		if cd.preferred {
			contact.Name, contact.Nickname, contact.Organization = cd.name, cd.nickname, cd.organization
		}
		if cd.photo != nil && (contact.Photo == nil || cd.preferredPhoto) {
			contact.Photo = cd.photo
		}
		for _, h := range cd.handles {
			if !slices.ContainsFunc(contact.Handles, func(o Handle) bool { return o.Value == h.Value }) {
				contact.Handles = append(contact.Handles, h)
			}
		}
		contact.fullNames = appendFolded(contact.fullNames, cd.name, cd.first+" "+cd.last, cd.first+" "+cd.middle+" "+cd.last)
		contact.nicknames = appendFolded(contact.nicknames, cd.nickname)
		contact.firstNames = appendFolded(contact.firstNames, cd.first)
	}
	return contacts
}

func appendFolded(names []string, add ...string) []string {
	for _, name := range add {
		if f := fold(name); f != "" && !slices.Contains(names, f) {
			names = append(names, f)
		}
	}
	return names
}

// fold lowercases a name, strips its accents and collapses its spaces.
func fold(name string) string {
	stripped, _, _ := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), name)
	return strings.Join(strings.Fields(strings.ToLower(stripped)), " ")
}

// Normalize writes a handle the way iMessage does: an email lowercased, a phone number in E.164,
// read as a US number when it has no country code.
func Normalize(handle string) string {
	handle = strings.TrimSpace(handle)
	if strings.Contains(handle, "@") {
		return strings.ToLower(handle)
	}
	number, err := phonenumbers.Parse(handle, "US")
	if err != nil {
		return strings.ToLower(handle)
	}
	return phonenumbers.Format(number, phonenumbers.E164)
}
