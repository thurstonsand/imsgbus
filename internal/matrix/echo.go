package matrix

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// echoWindow is how long after a send Messages' own row for it can still turn up.
const echoWindow = time.Minute

// sends holds the texts Beeper sent until Messages records them. imsg 0.20.0's send.tracked can
// report success while Messages saves the message under a GUID of its own, and that row would
// otherwise come back into Beeper as a second copy of the message (openclaw/imsg#385).
type sends struct {
	lock    sync.Mutex
	pending []sentText
}

type sentText struct {
	chatGUID, guid, text string
	at                   time.Time
}

func (s *sends) add(chatGUID, guid, text string, at time.Time) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.pending = append(s.pending, sentText{chatGUID: chatGUID, guid: guid, text: text, at: at})
}

func (s *sends) remove(guid string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.take(func(p sentText) bool { return p.guid == guid })
}

// echoOf is the GUID Beeper sent a message from me under, when Messages recorded that send under
// guid instead. A message under the GUID it was sent with is no echo to drop.
func (s *sends) echoOf(chatGUID, guid, text string, at time.Time) string {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.pending = slices.DeleteFunc(s.pending, func(p sentText) bool { return at.Sub(p.at) > echoWindow })
	if _, ok := s.take(func(p sentText) bool { return strings.EqualFold(p.guid, guid) }); ok {
		return ""
	}
	sent, _ := s.take(func(p sentText) bool { return p.chatGUID == chatGUID && p.text == text })
	return sent.guid
}

// take removes and returns the oldest pending send that matches.
func (s *sends) take(match func(sentText) bool) (sentText, bool) {
	for i, p := range s.pending {
		if match(p) {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return p, true
		}
	}
	return sentText{}, false
}
