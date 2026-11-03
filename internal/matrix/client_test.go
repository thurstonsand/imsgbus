package matrix

import "testing"

func TestDisplayAddressWritesNumbersOut(t *testing.T) {
	for address, want := range map[string]string{
		"+18598593641":       "+1 859-859-3641",
		"+442071838750":      "+44 20 7183 8750",
		"97790":              "97790",
		"dana.w@example.com": "dana.w@example.com",
	} {
		if got := displayAddress(address); got != want {
			t.Errorf("displayAddress(%q) = %q; want %q", address, got, want)
		}
	}
}
