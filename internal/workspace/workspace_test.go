package workspace

import (
	"strings"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		p       string
		wantErr string // "" = ok
	}{
		{"out/report.html", ""},
		{"./out/report.html", ""},
		{"nested/dir/f.txt", ""},
		{"../escape.txt", "escapes workspace"},
		{"../../etc/passwd", "escapes workspace"},
		{"/etc/passwd", "absolute path not allowed"},
		{"", "empty path"},
	}
	for _, tc := range cases {
		_, err := SafeJoin(base, tc.p)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("SafeJoin(%q) unexpected err: %v", tc.p, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("SafeJoin(%q): want error containing %q, got %v", tc.p, tc.wantErr, err)
		}
	}
}
