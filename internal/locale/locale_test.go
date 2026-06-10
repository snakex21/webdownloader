package locale

import "testing"

func TestShortCode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"pl-PL", "pl"},
		{"en_US", "en"},
		{"en", "en"},
		{"de", "de"},
		{"zh-CN", "zh"},
		{"PT-br", "pt"},
		{"", ""},
		{"   ", ""},
		{"kok", "ko"}, // truncated to 2 chars
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := shortCode(c.in)
			if c.in == "" || c.in == "   " {
				// shortCode returns "" for blank input, but Detect wraps that
				// into "en". That's fine — we test shortCode in isolation.
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("shortCode(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
