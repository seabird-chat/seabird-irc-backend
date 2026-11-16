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
	for _, l := range doc {
		out = append(out, l.wrap()...)
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
func (d *document) text(s string, st style) {
	// Block text must not carry its own IRC formatting or protocol bytes.
	for i, piece := range strings.Split(stripControlChars(s), "\n") {
		if i > 0 {
			d.newLine()
		}

		if piece != "" {
			l := d.currentLine()
			l.segments = append(l.segments, segment{text: piece, style: st})
		}
	}
}

// appendIndented renders a block on its own lines, with firstPrefix on the
// first and restPrefix on the others and on any wrapped continuations.
func (d *document) appendIndented(b *pb.Block, st style, firstPrefix, restPrefix string) {
	var nested document
	nested.walk(b, st)

	first := true
	for _, l := range nested {
		if len(l.segments) == 0 {
			continue
		}

		prefix := restPrefix
		if first {
			prefix = firstPrefix
			first = false
		}

		l.prefix = joinPrefix(prefix, l.prefix)
		l.wrapPrefix = joinPrefix(restPrefix, l.wrapPrefix)

		d.breakLine()
		*d.currentLine() = l
		d.newLine()
	}
}

// walk renders a block and everything under it into the document. The style
// is the formatting inherited from the blocks above.
func (d *document) walk(b *pb.Block, st style) {
	switch v := b.GetInner().(type) {
	case *pb.Block_Text:
		d.text(v.Text.GetText(), st)

	case *pb.Block_Italics:
		d.walk(v.Italics.GetInner(), st|styleItalic)

	case *pb.Block_Bold:
		d.walk(v.Bold.GetInner(), st|styleBold)

	case *pb.Block_Underline:
		d.walk(v.Underline.GetInner(), st|styleUnderline)

	case *pb.Block_Strikethrough:
		d.walk(v.Strikethrough.GetInner(), st|styleStrike)

	case *pb.Block_Spoiler:
		d.walk(v.Spoiler.GetInner(), st|styleSpoiler)

	case *pb.Block_InlineCode:
		d.text(v.InlineCode.GetText(), st|styleMono)

	case *pb.Block_FencedCode:
		d.breakLine()
		d.text(strings.TrimRight(v.FencedCode.GetText(), "\n"), st|styleMono)
		d.breakLine()

	case *pb.Block_Heading:
		d.breakLine()
		d.walk(v.Heading.GetInner(), st|styleBold)
		d.breakLine()

	case *pb.Block_Blockquote:
		d.appendIndented(v.Blockquote.GetInner(), st, "> ", "> ")

	case *pb.Block_List:
		for _, item := range v.List.GetInner() {
			d.appendIndented(item, st, "- ", "  ")
		}

	case *pb.Block_Link:
		url := v.Link.GetUrl()
		inner := v.Link.GetInner()
		if inner == nil || inner.GetPlain() == url {
			d.text(url, st)
			break
		}

		d.walk(inner, st)
		d.text(" ("+url+")", st)

	case *pb.Block_Timestamp:
		if ts := v.Timestamp.GetInner(); ts != nil {
			d.text(ts.AsTime().UTC().Format(time.DateTime)+" UTC", st)
		}

	case *pb.Block_Container:
		for _, inner := range v.Container.GetInner() {
			d.walk(inner, st)
		}

	default:
		// Unknown block type from a newer protocol version: fall back to the
		// plain text core computed for it.
		d.text(b.GetPlain(), st)
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
func (l logicalLine) wrap() []string {
	var (
		out     []string
		buf     strings.Builder
		cur     style
		hasText bool
	)

	buf.WriteString(l.prefix)

	// endLine closes any open formatting, emits the buffered line if it has
	// text, and starts the next one with the wrap prefix.
	endLine := func() {
		if !hasText {
			return
		}

		if cur != 0 {
			buf.WriteString(codeReset)
			cur = 0
		}

		out = append(out, buf.String())
		buf.Reset()
		buf.WriteString(l.wrapPrefix)
		hasText = false
	}

	for _, seg := range l.segments {
		text := seg.text

		for text != "" {
			// Space left for text once we've written the codes to switch
			// styles, and kept room for the reset that will close this line.
			// Prefixes are capped, so there is always room for some text on an
			// empty line.
			codes := styleTransition(cur, seg.style)
			avail := maxLineBytes - buf.Len() - len(codes)
			if seg.style != 0 {
				avail -= len(codeReset)
			}

			if avail < utf8.UTFMax {
				endLine()
				continue
			}

			chunk := text
			if len(text) > avail {
				cut := splitPoint(text, avail)
				chunk = text[:cut]
				text = strings.TrimLeft(text[cut:], " ")
			} else {
				text = ""
			}

			buf.WriteString(codes)
			buf.WriteString(chunk)
			cur = seg.style
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
