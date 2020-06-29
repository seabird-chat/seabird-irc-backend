package seabird_irc

import "strings"

func splitLines(text string) []string {
	return strings.Split(text, "\n")
}
