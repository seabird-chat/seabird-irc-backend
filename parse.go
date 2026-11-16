package seabird_irc

import (
	"strings"
	"unicode"

	"github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
)

const (
	// formattingChars are the control characters that start a formatting code.
	formattingChars = codeBold + codeItalic + codeUnderline + codeStrike +
		codeMono + codeReset + codeColor + codeHexColor + codeReverse

	// noColor means a color number was not given. defaultColor is the number
	// meaning the client's default color.
	noColor      = -1
	defaultColor = 99
)

// toggleStyle returns the style flipped by a formatting code, if it is one of
// the toggle codes.
func toggleStyle(code string) (style, bool) {
	for _, tc := range toggleCodes {
		if tc.code == code {
			return tc.flag, true
		}
	}

	return 0, false
}

// parseSegments splits IRC text into segments of uniform style, removing the
// formatting control codes. Colors are dropped, except that matching
// foreground and background colors are treated as a spoiler.
func parseSegments(s string) []segment {
	var (
		segments []segment
		current  style
		fg, bg   = noColor, noColor
	)

	// add appends text in the current style, merging it into the previous
	// segment if the style hasn't changed.
	add := func(text string) {
		if last := len(segments) - 1; last >= 0 && segments[last].style == current {
			segments[last].text += text
			return
		}

		segments = append(segments, segment{text: text, style: current})
	}

	for s != "" {
		var tok token
		tok, s = cutToken(s)

		if tok.code == "" {
			add(tok.text)
			continue
		}

		if flag, ok := toggleStyle(tok.code); ok {
			current ^= flag
			continue
		}

		switch tok.code {
		case codeReset:
			current = 0
			fg, bg = noColor, noColor

		case codeColor:
			if tok.fg == noColor {
				// A color code with no numbers resets the colors.
				fg, bg = noColor, noColor
			} else {
				fg = tok.fg

				// Without a background, the previous one stays.
				if tok.bg != noColor {
					bg = tok.bg
				}
			}

			if fg == defaultColor {
				fg = noColor
			}

			if bg == defaultColor {
				bg = noColor
			}

			if fg != noColor && fg == bg {
				current |= styleSpoiler
			} else {
				current &^= styleSpoiler
			}

		case codeHexColor, codeReverse:
			// Not representable as a block.
		}
	}

	return segments
}

// token is either a run of plain text or a single formatting code.
type token struct {
	// text is the plain text. It is empty for a formatting code.
	text string

	// code is the formatting code. It is empty for plain text.
	code string

	// fg and bg are the color numbers following a codeColor, or noColor when
	// not given. A bare code has neither.
	fg, bg int
}

// cutToken removes the first token from the start of s, which must not be
// empty.
func cutToken(s string) (tok token, rest string) {
	idx := strings.IndexAny(s, formattingChars)

	switch {
	case idx < 0:
		return token{text: s}, ""

	case idx > 0:
		return token{text: s[:idx]}, s[idx:]
	}

	tok = token{code: s[:1], fg: noColor, bg: noColor}
	rest = s[1:]

	switch tok.code {
	case codeColor:
		tok.fg, tok.bg, rest = cutColor(rest)

	case codeHexColor:
		rest = cutHexColor(rest)
	}

	return tok, rest
}

// cutColor removes the arguments of a color code from the start of s: a
// foreground number, optionally followed by a comma and a background number.
// A color that is not given is returned as noColor.
func cutColor(s string) (fg, bg int, rest string) {
	fg, rest, ok := cutNumber(s)
	if !ok {
		return noColor, noColor, s
	}

	bg = noColor

	if afterComma, found := strings.CutPrefix(rest, ","); found {
		if number, afterNumber, ok := cutNumber(afterComma); ok {
			bg, rest = number, afterNumber
		}
	}

	return fg, bg, rest
}

// cutNumber removes up to two decimal digits from the start of s.
func cutNumber(s string) (number int, rest string, ok bool) {
	digits := 0
	for digits < len(s) && digits < 2 && s[digits] >= '0' && s[digits] <= '9' {
		number = number*10 + int(s[digits]-'0')
		digits++
	}

	if digits == 0 {
		return 0, s, false
	}

	return number, s[digits:], true
}

// cutHexColor removes the arguments of a hex color code from the start of s:
// an RRGGBB color, optionally followed by a comma and another.
func cutHexColor(s string) string {
	rest, ok := cutHex(s)
	if !ok {
		return s
	}

	if afterComma, found := strings.CutPrefix(rest, ","); found {
		if afterBackground, ok := cutHex(afterComma); ok {
			return afterBackground
		}
	}

	return rest
}

// cutHex removes six hex digits from the start of s.
func cutHex(s string) (rest string, ok bool) {
	if len(s) < 6 {
		return s, false
	}

	for _, char := range s[:6] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return s, false
		}
	}

	return s[6:], true
}

// plainText returns the text of the segments with all formatting removed.
func plainText(segments []segment) string {
	var sb strings.Builder
	for _, seg := range segments {
		sb.WriteString(seg.text)
	}

	return sb.String()
}

// segmentsBlock builds a block tree from segments.
func segmentsBlock(segments []segment) *pb.Block {
	blocks := make([]*pb.Block, 0, len(segments))

	for _, seg := range segments {
		var block *pb.Block
		if seg.style&styleMono != 0 {
			block = seabird.NewInlineCodeBlock(seg.text)
		} else {
			block = seabird.NewTextBlock(seg.text)
		}

		blocks = append(blocks, styledBlock(block, seg.style))
	}

	switch len(blocks) {
	case 0:
		return seabird.NewTextBlock("")

	case 1:
		return blocks[0]
	}

	return seabird.NewContainerBlock(blocks...)
}

// styledBlock wraps block in a block for each style in styles.
func styledBlock(block *pb.Block, styles style) *pb.Block {
	if styles&styleStrike != 0 {
		block = seabird.NewStrikethroughBlock(block)
	}

	if styles&styleUnderline != 0 {
		block = seabird.NewUnderlineBlock(block)
	}

	if styles&styleItalic != 0 {
		block = seabird.NewItalicsBlock(block)
	}

	if styles&styleBold != 0 {
		block = seabird.NewBoldBlock(block)
	}

	if styles&styleSpoiler != 0 {
		block = seabird.NewSpoilerBlock(block)
	}

	return block
}

// trimSegments returns the segments without their first n bytes of text and
// without leading or trailing whitespace.
func trimSegments(segments []segment, n int) []segment {
	var trimmed []segment

	for _, seg := range segments {
		if n >= len(seg.text) {
			n -= len(seg.text)
			continue
		}

		seg.text = seg.text[n:]
		n = 0

		if len(trimmed) == 0 {
			seg.text = strings.TrimLeftFunc(seg.text, unicode.IsSpace)
		}

		if seg.text != "" {
			trimmed = append(trimmed, seg)
		}
	}

	for len(trimmed) > 0 {
		last := &trimmed[len(trimmed)-1]

		last.text = strings.TrimRightFunc(last.text, unicode.IsSpace)
		if last.text != "" {
			break
		}

		trimmed = trimmed[:len(trimmed)-1]
	}

	return trimmed
}
