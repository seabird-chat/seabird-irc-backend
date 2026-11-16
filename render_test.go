package seabird_irc

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestRenderBlock(t *testing.T) {
	text := seabird.NewTextBlock

	tests := []struct {
		name string
		in   *pb.Block
		want []string
	}{
		{"text", text("hello"), []string{"hello"}},
		{
			"bold",
			seabird.NewContainerBlock(text("a "), seabird.NewBoldBlock(text("b")), text(" c")),
			[]string{"a \x02b\x02 c"},
		},
		{
			"nested styles",
			seabird.NewBoldBlock(text("a"), seabird.NewItalicsBlock(text("b")), text("c")),
			[]string{"\x02a\x1Db\x1Dc\x0F"},
		},
		{
			"italics inside bold toggles off without reset",
			seabird.NewBoldBlock(seabird.NewItalicsBlock(text("a")), text(" b")),
			[]string{"\x02\x1Da\x1D b\x0F"},
		},
		{
			"spoiler removal resets and keeps other styles",
			seabird.NewBoldBlock(seabird.NewSpoilerBlock(text("x")), text("5")),
			[]string{"\x02\x0301,01x\x0F\x025\x0F"},
		},
		{
			"style spans newline",
			seabird.NewBoldBlock(text("a\nb")),
			[]string{"\x02a\x0F", "\x02b\x0F"},
		},
		{
			"strike underline code spoiler",
			seabird.NewContainerBlock(
				seabird.NewStrikethroughBlock(text("s")),
				seabird.NewUnderlineBlock(text("u")),
				seabird.NewInlineCodeBlock("c"),
				seabird.NewSpoilerBlock(text("x")),
			),
			[]string{"\x1Es\x1F\x1Eu\x1F\x11c\x11\x0301,01x\x0F"},
		},
		{
			"link",
			seabird.NewLinkBlock("https://example.com", text("site")),
			[]string{"site (https://example.com)"},
		},
		{
			"link text is url",
			seabird.NewLinkBlock("https://example.com", text("https://example.com")),
			[]string{"https://example.com"},
		},
		{
			"heading on own line",
			seabird.NewContainerBlock(text("before"), seabird.NewHeadingBlock(1, text("Title")), text("after")),
			[]string{"before", "\x02Title\x0F", "after"},
		},
		{
			"list",
			seabird.NewContainerBlock(
				text("Items:"),
				seabird.NewListBlock(text("one"), seabird.NewBoldBlock(text("two\nmore"))),
			),
			[]string{"Items:", "- one", "- \x02two\x0F", "  \x02more\x0F"},
		},
		{
			"blockquote",
			seabird.NewBlockquoteBlock(text("a\nb")),
			[]string{"> a", "> b"},
		},
		{
			"fenced code",
			seabird.NewFencedCodeBlock("go", "x := 1\ny := 2\n"),
			[]string{"\x11x := 1\x0F", "\x11y := 2\x0F"},
		},
		{
			"timestamp",
			seabird.NewTimestampBlock(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)),
			[]string{"2024-01-02 03:04:05 UTC"},
		},
		{
			"control characters stripped",
			text("a\x02b\x03c\r\td"),
			[]string{"abc d"},
		},
		{
			"empty lines skipped",
			text("a\n\nb"),
			[]string{"a", "b"},
		},
		{
			"unknown block uses plain",
			&pb.Block{Plain: "fallback"},
			[]string{"fallback"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderLines("", tt.in)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderBlockLongLine(t *testing.T) {
	long := strings.Repeat("word ", 300)
	got := renderLines("", seabird.NewBoldBlock(seabird.NewTextBlock(long)))

	if len(got) < 3 {
		t.Fatalf("expected line to be split, got %d lines", len(got))
	}

	var joined strings.Builder
	for i, l := range got {
		if len(l) > maxLineBytes {
			t.Errorf("line of %d bytes exceeds limit", len(l))
		}

		// Splitting at a space can leave a line slightly short, but exact
		// accounting should never waste more than a word's worth.
		if i < len(got)-1 && len(l) < maxLineBytes-10 {
			t.Errorf("line of %d bytes is under-filled", len(l))
		}

		if !strings.HasPrefix(l, "\x02") || !strings.HasSuffix(l, "\x0F") {
			t.Errorf("line not self-contained: %q", l)
		}

		joined.WriteString(strings.Trim(l, "\x02\x0F"))
		joined.WriteString(" ")
	}

	if strings.Fields(joined.String())[0] != "word" ||
		len(strings.Fields(joined.String())) != 300 {
		t.Errorf("text lost or altered while splitting")
	}
}

func TestRenderBlockLongMultibyte(t *testing.T) {
	long := strings.Repeat("é", 1000)
	got := renderLines("", seabird.NewTextBlock(long))

	total := 0
	for _, l := range got {
		if !utf8.ValidString(l) {
			t.Errorf("invalid UTF-8 in %q", l)
		}

		if len(l) > maxLineBytes {
			t.Errorf("line of %d bytes exceeds limit", len(l))
		}

		total += utf8.RuneCountInString(l)
	}

	if total != 1000 {
		t.Errorf("got %d runes, want 1000", total)
	}
}

func TestOutgoingLinesFallback(t *testing.T) {
	got := renderLines("a\nb", nil)
	if strings.Join(got, "|") != "a|b" {
		t.Errorf("got %q", got)
	}

	got = renderLines("ignored", seabird.NewBoldBlock(seabird.NewTextBlock("x")))
	if strings.Join(got, "|") != "\x02x\x0F" {
		t.Errorf("got %q", got)
	}
}

// unknownBlock builds a block whose oneof field number this version of the
// protobuf code doesn't know about, as a newer seabird-core might send.
func unknownBlock(t *testing.T, plain string) *pb.Block {
	t.Helper()

	var raw []byte
	raw = protowire.AppendTag(raw, 1, protowire.BytesType)
	raw = protowire.AppendString(raw, plain)
	raw = protowire.AppendTag(raw, 99, protowire.BytesType)
	raw = protowire.AppendBytes(raw, []byte("future"))

	b := &pb.Block{}
	if err := proto.Unmarshal(raw, b); err != nil {
		t.Fatal(err)
	}

	if b.Inner != nil || b.Plain != plain {
		t.Fatalf("unexpected decode: %+v", b)
	}

	return b
}

func TestRenderBlockUnknownNested(t *testing.T) {
	text := seabird.NewTextBlock

	got := renderLines("", seabird.NewContainerBlock(
		seabird.NewBoldBlock(text("known")),
		text(" "),
		unknownBlock(t, "fallback\x02"),
		text(" "),
		seabird.NewItalicsBlock(text("after")),
	))

	want := "\x02known\x02 fallback \x1Dafter\x0F"
	if strings.Join(got, "|") != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Unknown block inside a formatting block inherits the formatting.
	got = renderLines("", seabird.NewBoldBlock(unknownBlock(t, "x")))
	if strings.Join(got, "|") != "\x02x\x0F" {
		t.Errorf("got %q", got)
	}

	// Link text of an unknown type still shows.
	got = renderLines("", seabird.NewLinkBlock("https://e.com", unknownBlock(t, "site")))
	if strings.Join(got, "|") != "site (https://e.com)" {
		t.Errorf("got %q", got)
	}
}

func TestRenderWrappedPrefixes(t *testing.T) {
	long := strings.Repeat("word ", 200)

	got := renderLines("", seabird.NewBlockquoteBlock(seabird.NewTextBlock(long)))
	if len(got) < 2 {
		t.Fatalf("expected wrapping, got %d lines", len(got))
	}

	for _, l := range got {
		if !strings.HasPrefix(l, "> ") || len(l) > maxLineBytes {
			t.Errorf("bad quote line %q", l)
		}
	}

	got = renderLines("", seabird.NewListBlock(seabird.NewTextBlock(long), seabird.NewTextBlock("b")))
	if !strings.HasPrefix(got[0], "- ") || !strings.HasPrefix(got[1], "  word") ||
		got[len(got)-1] != "- b" {
		t.Errorf("bad list wrapping: %q", got)
	}

	got = renderLines("", seabird.NewListBlock(seabird.NewBlockquoteBlock(seabird.NewTextBlock("a\nb"))))
	if strings.Join(got, "|") != "- > a|  > b" {
		t.Errorf("bad nested prefixes: %q", got)
	}
}

func TestRenderDeepNesting(t *testing.T) {
	b := seabird.NewTextBlock(strings.Repeat("word ", 200))
	for range 100 {
		b = seabird.NewBlockquoteBlock(b)
	}

	for _, l := range renderLines("", b) {
		if len(l) > maxLineBytes {
			t.Fatalf("line of %d bytes exceeds limit", len(l))
		}
	}
}

func TestStyleTransition(t *testing.T) {
	tests := []struct {
		name     string
		from, to style
		want     string
	}{
		{"same", styleBold, styleBold, ""},
		{"on", 0, styleBold | styleItalic, "\x02\x1D"},
		{"off", styleBold | styleItalic, styleBold, "\x1D"},
		{"swap", styleBold, styleItalic, "\x02\x1D"},
		{"spoiler on", styleBold, styleBold | styleSpoiler, "\x0301,01"},
		{"spoiler off", styleBold | styleSpoiler, styleBold, "\x0F\x02"},
		{"spoiler off to none", styleSpoiler, 0, "\x0F"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := styleTransition(tt.from, tt.to); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
