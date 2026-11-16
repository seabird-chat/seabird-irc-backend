package seabird_irc

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
)

func TestParseSegments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []segment
	}{
		{"empty", "", nil},
		{"plain", "hello", []segment{{"hello", 0}}},
		{"multibyte", "héllo ✓", []segment{{"héllo ✓", 0}}},
		{
			"bold",
			"a \x02b\x02 c",
			[]segment{{"a ", 0}, {"b", styleBold}, {" c", 0}},
		},
		{
			"nested toggles",
			"\x02a\x1Db\x1Dc\x02",
			[]segment{{"a", styleBold}, {"b", styleBold | styleItalic}, {"c", styleBold}},
		},
		{
			"all toggles",
			"\x02\x1D\x1F\x1E\x11x",
			[]segment{{"x", styleBold | styleItalic | styleUnderline | styleStrike | styleMono}},
		},
		{"reset", "\x02a\x0Fb", []segment{{"a", styleBold}, {"b", 0}}},
		{"unchanged style merges", "a\x02\x02b", []segment{{"ab", 0}}},
		{"unterminated", "\x02a", []segment{{"a", styleBold}}},
		{"only codes", "\x02\x1D\x0F", nil},
		{"reverse stripped", "a\x16b", []segment{{"ab", 0}}},
		{"color dropped", "\x0304red", []segment{{"red", 0}}},
		{"color with background", "\x0304,05red", []segment{{"red", 0}}},
		{"one digit color", "\x034red", []segment{{"red", 0}}},
		{"digit after two digit color", "\x03045", []segment{{"5", 0}}},
		{"comma without background", "\x034,x", []segment{{",x", 0}}},
		{"bare color code", "\x0304a\x03b", []segment{{"ab", 0}}},
		{"hex color stripped", "\x04FF0000red", []segment{{"red", 0}}},
		{"hex color with background", "\x04FF0000,00FF00red", []segment{{"red", 0}}},
		{"invalid hex color kept", "\x04FFx", []segment{{"FFx", 0}}},
		{
			"spoiler",
			"a\x0301,01secret\x03b",
			[]segment{{"a", 0}, {"secret", styleSpoiler}, {"b", 0}},
		},
		{"spoiler digit text", "\x0301,015", []segment{{"5", styleSpoiler}}},
		{
			"spoiler ends when colors differ",
			"\x0301,01a\x0302b",
			[]segment{{"a", styleSpoiler}, {"b", 0}},
		},
		{
			"spoiler continues when colors still match",
			"\x0302,02a\x0302b",
			[]segment{{"ab", styleSpoiler}},
		},
		{"default colors are not a spoiler", "\x0399,99a", []segment{{"a", 0}}},
		{
			"default foreground ends spoiler",
			"\x0301,01a\x0399b",
			[]segment{{"a", styleSpoiler}, {"b", 0}},
		},
		{"default foreground with background", "\x0399,05a", []segment{{"a", 0}}},
		{
			"reset clears spoiler",
			"\x0301,01a\x0Fb",
			[]segment{{"a", styleSpoiler}, {"b", 0}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSegments(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSegmentsBlock(t *testing.T) {
	text := seabird.NewTextBlock

	tests := []struct {
		name string
		in   string
		want proto.Message
	}{
		{"empty", "", text("")},
		{"plain", "hello", text("hello")},
		{
			"mixed",
			"a \x02b\x02 c",
			seabird.NewContainerBlock(text("a "), seabird.NewBoldBlock(text("b")), text(" c")),
		},
		{
			"nested styles",
			"\x02\x1Dx",
			seabird.NewBoldBlock(seabird.NewItalicsBlock(text("x"))),
		},
		{"inline code", "\x11x", seabird.NewInlineCodeBlock("x")},
		{"spoiler", "\x0301,01x", seabird.NewSpoilerBlock(text("x"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := segmentsBlock(parseSegments(tt.in))
			if !proto.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// Text we render should parse back into the same blocks.
func TestRenderParseRoundTrip(t *testing.T) {
	text := seabird.NewTextBlock

	blocks := []proto.Message{
		text("plain"),
		seabird.NewContainerBlock(text("a "), seabird.NewBoldBlock(text("b")), text(" c")),
		seabird.NewBoldBlock(seabird.NewItalicsBlock(text("both"))),
		seabird.NewContainerBlock(
			seabird.NewStrikethroughBlock(text("s")),
			seabird.NewUnderlineBlock(text("u")),
			seabird.NewInlineCodeBlock("c"),
			seabird.NewSpoilerBlock(text("x")),
		),
	}

	for _, want := range blocks {
		lines := renderLines("", want.(*pb.Block))
		if len(lines) != 1 {
			t.Fatalf("expected one line, got %q", lines)
		}

		got := segmentsBlock(parseSegments(strings.Join(lines, "")))
		if !proto.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", lines[0], got, want)
		}
	}
}

func TestTrimSegments(t *testing.T) {
	segments := []segment{{"bot: ", 0}, {" hi ", styleBold}, {"there  ", 0}}

	got := trimSegments(segments, 4)
	want := []segment{{"hi ", styleBold}, {"there", 0}}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// The input is left alone.
	if segments[0].text != "bot: " {
		t.Errorf("input modified: %q", segments)
	}

	if got := trimSegments([]segment{{"bot: ", 0}}, 4); len(got) != 0 {
		t.Errorf("got %q, want nothing", got)
	}
}

func TestCutToken(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		want     token
		wantRest string
	}{
		{"text", "hello", token{text: "hello"}, ""},
		{"text before code", "hi\x02there", token{text: "hi"}, "\x02there"},
		{"toggle", "\x02bold", token{code: codeBold, fg: noColor, bg: noColor}, "bold"},
		{"reset", "\x0Fx", token{code: codeReset, fg: noColor, bg: noColor}, "x"},
		{"reverse", "\x16x", token{code: codeReverse, fg: noColor, bg: noColor}, "x"},
		{"bare color", "\x03x", token{code: codeColor, fg: noColor, bg: noColor}, "x"},
		{"foreground", "\x034x", token{code: codeColor, fg: 4, bg: noColor}, "x"},
		{"two digit foreground", "\x03045", token{code: codeColor, fg: 4, bg: noColor}, "5"},
		{"background", "\x0304,05x", token{code: codeColor, fg: 4, bg: 5}, "x"},
		{"comma without background", "\x034,x", token{code: codeColor, fg: 4, bg: noColor}, ",x"},
		{"background without foreground", "\x03,5x", token{code: codeColor, fg: noColor, bg: noColor}, ",5x"},
		{"hex color", "\x04FF0000x", token{code: codeHexColor, fg: noColor, bg: noColor}, "x"},
		{"hex color with background", "\x04FF0000,00ff00x", token{code: codeHexColor, fg: noColor, bg: noColor}, "x"},
		{"short hex color", "\x04FF00x", token{code: codeHexColor, fg: noColor, bg: noColor}, "FF00x"},
		{"invalid hex color", "\x04GG0000x", token{code: codeHexColor, fg: noColor, bg: noColor}, "GG0000x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rest := cutToken(tt.in)
			if got != tt.want || rest != tt.wantRest {
				t.Errorf("got (%+v, %q), want (%+v, %q)", got, rest, tt.want, tt.wantRest)
			}
		})
	}
}
