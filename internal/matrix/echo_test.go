package matrix

import (
	"testing"
	"time"
)

func TestEchoOfMatchesSendsMessagesRecordedUnderAnotherGUID(t *testing.T) {
	start := time.Date(2026, 11, 5, 18, 19, 0, 0, time.UTC)
	var s sends
	s.add("any;-;+15550001111", "6B3A5202-2D11-4E22-A813-AA3A89121E8F", "ok", start)
	s.add("any;-;+15550001111", "1C0E8A4E-7F3D-4A8E-9A55-2B3F9D6C1E01", "ok", start.Add(time.Second))
	s.add("any;-;+15550001111", "3F5D2C1B-0A9E-4D8C-B7A6-5E4F3D2C1B0A", "see you", start.Add(2*time.Second))
	s.add("any;-;+15550002222", "9B53095F-5E7C-4FA5-BA1A-C07E051B4697", "hi", start)
	s.add("any;-;+15550003333", "77A1B2C3-D4E5-4F60-8172-93A4B5C6D7E8", "late", start)

	for _, tc := range []struct {
		name, chatGUID, guid, text string
		at                         time.Duration
		want                       string
	}{
		// Messages kept the second send's GUID: the GUID claims it, though the first has the same text.
		{"kept", "any;-;+15550001111", "1C0E8A4E-7F3D-4A8E-9A55-2B3F9D6C1E01", "ok", 3 * time.Second, "1C0E8A4E-7F3D-4A8E-9A55-2B3F9D6C1E01"},
		{"replaced", "any;-;+15550001111", "A9DD330E-5007-467F-8B9B-FE8938E3878B", "ok", 3 * time.Second, "6B3A5202-2D11-4E22-A813-AA3A89121E8F"},
		{"same text again", "any;-;+15550001111", "B1C2D3E4-0000-4000-8000-000000000001", "ok", 4 * time.Second, ""},
		{"other text", "any;-;+15550001111", "B1C2D3E4-0000-4000-8000-000000000002", "see ya", 4 * time.Second, ""},
		{"other chat", "any;-;+15550009999", "B1C2D3E4-0000-4000-8000-000000000003", "hi", 4 * time.Second, ""},
		{"second chat", "any;-;+15550002222", "6824242B-CDC4-430B-9AB0-6F188ABE12DF", "hi", 5 * time.Second, "9B53095F-5E7C-4FA5-BA1A-C07E051B4697"},
		{"too late", "any;-;+15550003333", "B1C2D3E4-0000-4000-8000-000000000004", "late", echoWindow + time.Second, ""},
		{"text after its window", "any;-;+15550001111", "B1C2D3E4-0000-4000-8000-000000000005", "see you", echoWindow + time.Second, "3F5D2C1B-0A9E-4D8C-B7A6-5E4F3D2C1B0A"},
	} {
		if got := s.echoOf(tc.chatGUID, tc.guid, tc.text, start.Add(tc.at)); got != tc.want {
			t.Errorf("%s: echoOf = %q; want %q", tc.name, got, tc.want)
		}
	}
	if len(s.pending) != 0 {
		t.Errorf("left pending: %+v", s.pending)
	}
}
