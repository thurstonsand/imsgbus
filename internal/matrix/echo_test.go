package matrix

import (
	"testing"
	"time"
)

func TestEchoOfMatchesSendsMessagesRecordedUnderAnotherGUID(t *testing.T) {
	start := time.Date(2026, 11, 5, 18, 19, 0, 0, time.UTC)
	var s sends
	s.add("any;-;+15550001111", "6b3a5202-2d11-4e22-a813-aa3a89121e8f", "ok", start)
	s.add("any;-;+15550001111", "1c0e8a4e-7f3d-4a8e-9a55-2b3f9d6c1e01", "ok", start.Add(time.Second))
	s.add("any;-;+15550001111", "3f5d2c1b-0a9e-4d8c-b7a6-5e4f3d2c1b0a", "see you", start.Add(2*time.Second))
	s.add("any;-;+15550002222", "9b53095f-5e7c-4fa5-ba1a-c07e051b4697", "hi", start)
	s.add("any;-;+15550003333", "77a1b2c3-d4e5-4f60-8172-93a4b5c6d7e8", "late", start)

	for _, tc := range []struct {
		name, chatGUID, guid, text string
		at                         time.Duration
		want                       string
	}{
		// Messages kept the GUID, upper-cased: the message bridges as itself, and its send is done.
		{"kept", "any;-;+15550001111", "1C0E8A4E-7F3D-4A8E-9A55-2B3F9D6C1E01", "ok", 3 * time.Second, ""},
		{"replaced", "any;-;+15550001111", "A9DD330E-5007-467F-8B9B-FE8938E3878B", "ok", 3 * time.Second, "6b3a5202-2d11-4e22-a813-aa3a89121e8f"},
		{"same text again", "any;-;+15550001111", "B1C2D3E4-0000-4000-8000-000000000001", "ok", 4 * time.Second, ""},
		{"other text", "any;-;+15550001111", "B1C2D3E4-0000-4000-8000-000000000002", "see ya", 4 * time.Second, ""},
		{"other chat", "any;-;+15550009999", "B1C2D3E4-0000-4000-8000-000000000003", "hi", 4 * time.Second, ""},
		{"second chat", "any;-;+15550002222", "6824242B-CDC4-430B-9AB0-6F188ABE12DF", "hi", 5 * time.Second, "9b53095f-5e7c-4fa5-ba1a-c07e051b4697"},
		{"too late", "any;-;+15550003333", "B1C2D3E4-0000-4000-8000-000000000004", "late", echoWindow + time.Second, ""},
		{"text after its window", "any;-;+15550001111", "B1C2D3E4-0000-4000-8000-000000000005", "see you", echoWindow + time.Second, "3f5d2c1b-0a9e-4d8c-b7a6-5e4f3d2c1b0a"},
	} {
		if got := s.echoOf(tc.chatGUID, tc.guid, tc.text, start.Add(tc.at)); got != tc.want {
			t.Errorf("%s: echoOf = %q; want %q", tc.name, got, tc.want)
		}
	}
	if len(s.pending) != 0 {
		t.Errorf("left pending: %+v", s.pending)
	}
}
