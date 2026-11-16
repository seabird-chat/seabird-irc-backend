package seabird_irc

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func splitLines(text string) []string {
	return strings.Split(text, "\n")
}

// stripControlChars removes control characters from s, keeping newlines and
// turning tabs into spaces.
func stripControlChars(s string) string {
	return strings.Map(func(c rune) rune {
		switch {
		case c == '\n':
			return c

		case c == '\t':
			return ' '

		case c < 0x20 || c == 0x7f:
			return -1
		}

		return c
	}, s)
}

// splitPoint returns the byte offset at which to split text, which must be
// longer than max, so the first part is at most max bytes. It prefers a space
// in the second half of the first part, and never splits inside a UTF-8
// sequence.
func splitPoint(text string, max int) int {
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}

	if idx := strings.LastIndexByte(text[:cut], ' '); idx > cut/2 {
		return idx
	}

	return cut
}

// mentionPrefixLen reports whether text opens by addressing nick, as in
// "nick: hello", and returns the length of that address: the nick, one
// punctuation character and the space after it.
func mentionPrefixLen(text, nick string) (int, bool) {
	rest, ok := strings.CutPrefix(text, nick)
	if !ok || len(rest) < 2 {
		return 0, false
	}

	punct, space := rest[0], rest[1]
	if punct >= utf8.RuneSelf || !unicode.IsPunct(rune(punct)) || space != ' ' {
		return 0, false
	}

	return len(nick) + 2, true
}
