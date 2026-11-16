package seabird_irc

import "testing"

func TestStripControlChars(t *testing.T) {
	got := stripControlChars("a\x02b\x03c\r\td\x7f\ne")
	if want := "abc d\ne"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitPoint(t *testing.T) {
	tests := []struct {
		name string
		text string
		max  int
		want int
	}{
		{"prefers space", "hello world again", 14, 11},
		{"ignores early space", "a bcdefghijklmnop", 10, 10},
		{"no space", "abcdefghijkl", 5, 5},
		{"avoids splitting rune", "ééééé", 5, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitPoint(tt.text, tt.max); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
