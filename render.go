package seabird_irc

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seabird-chat/seabird-go/pb"
)

// maxLineBytes is the most bytes of message we put in a single PRIVMSG, leaving
// room under IRC's 512-byte line limit for the prefix the server adds.
const maxLineBytes = 400

// toggleCodes are the formatting codes that switch a style on when it is off,
// and off when it is on.
var toggleCodes = []struct {
	flag style
	code string
}{
	{styleBold, codeBold},
	{styleItalic, codeItalic},
	{styleUnderline, codeUnderline},
	{styleStrike, codeStrike},
	{styleMono, codeMono},
}

// maxPrefixBytes caps the length of a line's prefix and wrapPrefix, so deep
// nesting can't crowd out the text.
const maxPrefixBytes = 64

// logicalLine is a line of output before wrapping.
type logicalLine struct {
	// prefix starts the first IRC line. wrapPrefix starts each further IRC
	// line created by wrapping. They are the same for a blockquote ("> ") but
	// differ for a list item ("- " then "  ").
	prefix     string
	wrapPrefix string

	segments []segment
}

// document is the result of walking a block tree. The zero value is an empty
// message.
type document []logicalLine

// renderLines returns the IRC lines to send for a message. If root is set it is
// rendered with IRC formatting codes, otherwise text is split on newlines.
func renderLines(text string, root *pb.Block) []string {
	if root == nil {
		return splitLines(text)
	}

	var doc document
	doc.walk(root, 0)

	var out []string
	for _, line := range doc {
		out = append(out, line.wrap()...)
	}

	return out
}

// currentLine returns the line being built, creating the first one if the
// document is empty.
func (d *document) currentLine() *logicalLine {
	if len(*d) == 0 {
		*d = append(*d, logicalLine{})
	}

	return &(*d)[len(*d)-1]
}

// newLine unconditionally starts a new line.
func (d *document) newLine() {
	d.currentLine()
	*d = append(*d, logicalLine{})
}

// breakLine starts a new line unless the current one is empty.
func (d *document) breakLine() {
	if len(d.currentLine().segments) > 0 {
		d.newLine()
	}
}

// text adds s to the current line with the given style. Newlines in s start
// new lines, and other control characters are removed.
func (d *document) text(s string, inherited style) {
	// Block text must not carry its own IRC formatting or protocol bytes.
	for i, piece := range strings.Split(stripControlChars(s), "\n") {
		if i > 0 {
			d.newLine()
		}

		if piece != "" {
			line := d.currentLine()
			line.segments = append(line.segments, segment{text: piece, style: inherited})
		}
	}
}

// appendIndented renders a block on its own lines, with firstPrefix on the
// first and restPrefix on the others and on any wrapped continuations.
func (d *document) appendIndented(
	block *pb.Block,
	inherited style,
	firstPrefix, restPrefix string,
) {
	var nested document
	nested.walk(block, inherited)

	first := true
	for _, line := range nested {
		if len(line.segments) == 0 {
			continue
		}

		prefix := restPrefix
		if first {
			prefix = firstPrefix
			first = false
		}

		line.prefix = joinPrefix(prefix, line.prefix)
		line.wrapPrefix = joinPrefix(restPrefix, line.wrapPrefix)

		d.breakLine()
		*d.currentLine() = line
		d.newLine()
	}
}

// walk renders a block and everything under it into the document. The inherited
// style is the formatting from the blocks above.
func (d *document) walk(block *pb.Block, inherited style) {
	switch v := block.GetInner().(type) {
	case *pb.Block_Text:
		d.text(v.Text.GetText(), inherited)

	case *pb.Block_Italics:
		d.walk(v.Italics.GetInner(), inherited|styleItalic)

	case *pb.Block_Bold:
		d.walk(v.Bold.GetInner(), inherited|styleBold)

	case *pb.Block_Underline:
		d.walk(v.Underline.GetInner(), inherited|styleUnderline)

	case *pb.Block_Strikethrough:
		d.walk(v.Strikethrough.GetInner(), inherited|styleStrike)

	case *pb.Block_Spoiler:
		d.walk(v.Spoiler.GetInner(), inherited|styleSpoiler)

	case *pb.Block_InlineCode:
		d.text(v.InlineCode.GetText(), inherited|styleMono)

	case *pb.Block_FencedCode:
		d.breakLine()
		d.text(strings.TrimRight(v.FencedCode.GetText(), "\n"), inherited|styleMono)
		d.breakLine()

	case *pb.Block_Heading:
		d.breakLine()
		d.walk(v.Heading.GetInner(), inherited|styleBold)
		d.breakLine()

	case *pb.Block_Blockquote:
		d.appendIndented(v.Blockquote.GetInner(), inherited, "> ", "> ")

	case *pb.Block_List:
		for _, item := range v.List.GetInner() {
			d.appendIndented(item, inherited, "- ", "  ")
		}

	case *pb.Block_Link:
		url := v.Link.GetUrl()
		inner := v.Link.GetInner()
		if inner == nil || inner.GetPlain() == url {
			d.text(url, inherited)
			break
		}

		d.walk(inner, inherited)
		d.text(" ("+url+")", inherited)

	case *pb.Block_Timestamp:
		if timestamp := v.Timestamp.GetInner(); timestamp != nil {
			d.text(timestamp.AsTime().UTC().Format(time.DateTime)+" UTC", inherited)
		}

	case *pb.Block_Container:
		for _, inner := range v.Container.GetInner() {
			d.walk(inner, inherited)
		}

	default:
		// Unknown block type from a newer protocol version: fall back to the
		// plain text core computed for it.
		d.text(block.GetPlain(), inherited)
	}
}

// styleTransition returns the formatting codes needed to go from one style to
// another.
func styleTransition(from, to style) string {
	var sb strings.Builder

	// A spoiler is a color, which can't be toggled off. A bare color code would
	// swallow a following digit, so removing it takes a full reset.
	if from&styleSpoiler != 0 && to&styleSpoiler == 0 {
		sb.WriteString(codeReset)
		from = 0
	}

	for _, tc := range toggleCodes {
		if (from^to)&tc.flag != 0 {
			sb.WriteString(tc.code)
		}
	}

	if to&styleSpoiler != 0 && from&styleSpoiler == 0 {
		sb.WriteString(codeSpoiler)
	}

	return sb.String()
}

// wrap encodes the line as IRC lines of at most maxLineBytes, including
// prefixes and formatting codes. Each returned line is self-contained:
// formatting open at a wrap is closed at the end of the line and reopened on
// the next.
func (line logicalLine) wrap() []string {
	var (
		out     []string
		buf     strings.Builder
		current style
		hasText bool
	)

	buf.WriteString(line.prefix)

	// endLine closes any open formatting, emits the buffered line if it has
	// text, and starts the next one with the wrap prefix.
	endLine := func() {
		if !hasText {
			return
		}

		if current != 0 {
			buf.WriteString(codeReset)
			current = 0
		}

		out = append(out, buf.String())
		buf.Reset()
		buf.WriteString(line.wrapPrefix)
		hasText = false
	}

	for _, seg := range line.segments {
		text := seg.text

		for text != "" {
			// Space left for text once we've written the codes to switch
			// styles, and kept room for the reset that will close this line.
			// Prefixes are capped, so there is always room for some text on an
			// empty line.
			codes := styleTransition(current, seg.style)
			available := maxLineBytes - buf.Len() - len(codes)
			if seg.style != 0 {
				available -= len(codeReset)
			}

			if available < utf8.UTFMax {
				endLine()
				continue
			}

			chunk := text
			if len(text) > available {
				cut := splitPoint(text, available)
				chunk = text[:cut]
				text = strings.TrimLeft(text[cut:], " ")
			} else {
				text = ""
			}

			buf.WriteString(codes)
			buf.WriteString(chunk)
			current = seg.style
			hasText = true

			if text != "" {
				endLine()
			}
		}
	}

	endLine()

	return out
}

// joinPrefix returns prefix followed by inner. If that would exceed
// maxPrefixBytes it returns inner alone.
func joinPrefix(prefix, inner string) string {
	if len(prefix)+len(inner) > maxPrefixBytes {
		return inner
	}

	return prefix + inner
}
