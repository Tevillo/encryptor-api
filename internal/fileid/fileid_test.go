package fileid

import "testing"

func TestNew(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if !Valid(id) {
			t.Fatalf("New produced invalid id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"AAAAAAAAAAAAAAAAAAAAAA", true},
		{"abcdefghijklmnopqrs-_A", true},
		{"", false},
		{"AAAAAAAAAAAAAAAAAAAAA", false},   // 21 chars
		{"AAAAAAAAAAAAAAAAAAAAAAA", false}, // 23 chars
		{"../../../../etc/passwd", false},  // 22 chars, bad characters
		{"AAAAAAAAAAAAAAAAAAAA+/", false},  // standard base64, not URL-safe
		{"AAAAAAAAAAAAAAAAAAAAAB", false},  // non-canonical trailing bits
	}
	for _, tt := range tests {
		if got := Valid(tt.in); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
