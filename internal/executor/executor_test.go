package executor

import "testing"

func TestSafePrefix(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "normal string", s: "abcdefghij", n: 8, want: "abcdefgh"},
		{name: "short string", s: "abc", n: 8, want: "abc"},
		{name: "empty string", s: "", n: 8, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafePrefix(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("SafePrefix(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
		})
	}
}
