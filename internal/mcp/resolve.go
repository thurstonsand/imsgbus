package mcp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thurstonsand/imsgbus/internal/contacts"
	"github.com/thurstonsand/imsgbus/internal/core"
)

// Agents see people and chats by name, and pass the same names back. A name is shown with its
// handle, as "Name (+15551234567)", only when the name alone would not find that person.

// address is a chat.db handle as people write it. chat.db marks some SMS participants with a
// suffix, as in "+15551234567(smsfp)"; short codes and business handles stay as they are.
func address(handle string) string {
	if i := strings.Index(handle, "("); i > 0 {
		handle = handle[:i]
	}
	if strings.Contains(handle, "@") {
		return strings.ToLower(handle)
	}
	return handle
}

// person labels a handle: the contact's name, or the handle for someone not in Contacts.
func (t *tools) person(handle string) string {
	handle = address(handle)
	name := t.core.Name(handle)
	if name == handle {
		return handle
	}
	if c, err := t.core.Person(name); err != nil || !owns(c, handle) {
		return fmt.Sprintf("%s (%s)", name, handle)
	}
	return name
}

func owns(c contacts.Contact, handle string) bool {
	return slices.ContainsFunc(c.Handles, func(h contacts.Handle) bool { return h.Value == handle })
}

// chatLabel names a chat: a group by its name, or by its members when it has none, and a
// one-on-one chat by the other person.
func (t *tools) chatLabel(c core.Chat) string {
	if c.IsGroup && c.Name != "" {
		return c.Name
	}
	names := make([]string, len(c.Participants))
	for i, handle := range c.Participants {
		names[i] = t.person(handle)
	}
	return strings.Join(names, ", ")
}

// target is where a chat name leads: an existing chat, or for one person without one yet, the
// address a new chat would start with.
type target struct {
	chat    core.Chat
	address string
}

var (
	labelled  = regexp.MustCompile(`^(.*\S)\s*\(([^()]+)\)$`)
	addressRE = regexp.MustCompile(`^(\+?[\d\s().-]{7,}|[^\s@]+@[^\s@]+)$`)
)

// resolve finds the chat a name means: a person's name or address for one-on-one chats, a group
// name, or several people separated by commas for the group of exactly them. Anything ambiguous
// is an error naming the choices, never a guess.
func (t *tools) resolve(ctx context.Context, name string) (target, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return target{}, errors.New("no chat named")
	}
	chats, err := t.core.RecentChats(ctx, time.Time{})
	if err != nil {
		return target{}, err
	}
	// A handle of an existing one-on-one chat, as person shows a stranger, is that chat.
	for _, c := range chats {
		if !c.IsGroup && len(c.Participants) == 1 && (address(c.Participants[0]) == name || address(c.Participants[0]) == contacts.Normalize(name)) {
			return target{chat: c}, nil
		}
	}
	if strings.Contains(name, ",") {
		return t.group(chats, name)
	}
	var groups []core.Chat
	for _, c := range chats {
		if c.IsGroup && strings.EqualFold(c.Name, name) {
			groups = append(groups, c)
		}
	}
	handles, personErr := t.handles(name)
	if _, ambiguous := errors.AsType[*ambiguousNameError](personErr); ambiguous {
		return target{}, personErr
	}
	switch {
	case personErr != nil && len(groups) == 0:
		return target{}, personErr
	case personErr == nil && len(groups) > 0:
		return target{}, fmt.Errorf("%q is both a person and a group chat; name the group's members, or the person with their number", name)
	case len(groups) > 1:
		return target{}, fmt.Errorf("%d group chats are named %q; name the group by its members instead", len(groups), name)
	case len(groups) == 1:
		return target{chat: groups[0]}, nil
	}
	// chats is newest first, so the first one-on-one chat with any of their handles is the one in use.
	for _, c := range chats {
		if !c.IsGroup && len(c.Participants) == 1 && slices.Contains(handles, address(c.Participants[0])) {
			return target{chat: c}, nil
		}
	}
	if len(handles) == 1 {
		return target{address: handles[0]}, nil
	}
	return target{}, fmt.Errorf("no chat with %s yet; say which address to start one at: %s", name, strings.Join(handles, ", "))
}

// handles are the addresses a single person's name or address stands for.
func (t *tools) handles(name string) ([]string, error) {
	// "Name (+15551234567)" is how person labels someone the name alone wouldn't find; anything
	// else in parentheses is part of a name.
	if m := labelled.FindStringSubmatch(name); m != nil && t.person(m[2]) == name {
		return []string{address(m[2])}, nil
	}
	if isAddress(name) {
		return []string{contacts.Normalize(name)}, nil
	}
	c, err := t.core.Person(name)
	if ambiguous, ok := errors.AsType[*contacts.AmbiguousError](err); ok {
		choices := make([]string, len(ambiguous.Candidates))
		for i, candidate := range ambiguous.Candidates {
			choices[i] = describe(candidate)
		}
		return nil, &ambiguousNameError{name: name, choices: choices}
	}
	if err != nil {
		return nil, fmt.Errorf("no contact or group chat is named %q", name)
	}
	var handles []string
	for _, h := range c.Handles {
		// A handle on several contacts' cards, such as a shared landline, reaches more than them.
		if owner, ok := t.core.ContactName(h.Value); ok && owner == c.Name {
			handles = append(handles, h.Value)
		}
	}
	if len(handles) == 0 {
		return nil, fmt.Errorf("every address of %s is shared with another contact; name the address to use", c.Name)
	}
	return handles, nil
}

// namesAddress reports whether name names one address rather than a person, so a send to it
// goes to that address even when the person has others.
func (t *tools) namesAddress(name string) bool {
	name = strings.TrimSpace(name)
	if m := labelled.FindStringSubmatch(name); m != nil && t.person(m[2]) == name {
		return true
	}
	return isAddress(name)
}

// isAddress reports whether s is an email or a phone number iMessage can reach.
func isAddress(s string) bool {
	return addressRE.MatchString(s) && (strings.Contains(s, "@") || strings.HasPrefix(contacts.Normalize(s), "+"))
}

func describe(c contacts.Contact) string {
	var extra []string
	if c.Nickname != "" {
		extra = append(extra, "nickname "+c.Nickname)
	}
	if c.Organization != "" {
		extra = append(extra, c.Organization)
	}
	if len(extra) == 0 {
		return c.Name
	}
	return fmt.Sprintf("%s (%s)", c.Name, strings.Join(extra, ", "))
}

// group finds the group chat whose members are exactly the comma-separated people.
func (t *tools) group(chats []core.Chat, names string) (target, error) {
	var people [][]string
	for part := range strings.SplitSeq(names, ",") {
		handles, err := t.handles(strings.TrimSpace(part))
		if err != nil {
			return target{}, err
		}
		people = append(people, handles)
	}
	for _, c := range chats {
		if c.IsGroup && sameMembers(c.Participants, people) {
			return target{chat: c}, nil
		}
	}
	return target{}, fmt.Errorf("no group chat has exactly %s", names)
}

// sameMembers reports whether each participant is one of the people, and each person is there.
func sameMembers(participants []string, people [][]string) bool {
	if len(participants) != len(people) {
		return false
	}
	found := make([]bool, len(people))
	for _, p := range participants {
		i := slices.IndexFunc(people, func(handles []string) bool { return slices.Contains(handles, address(p)) })
		if i < 0 || found[i] {
			return false
		}
		found[i] = true
	}
	return true
}

type ambiguousNameError struct {
	name    string
	choices []string
}

func (e *ambiguousNameError) Error() string {
	return fmt.Sprintf("%q could be %s; use a fuller name", e.name, strings.Join(e.choices, " or "))
}
