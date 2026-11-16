package seabird_irc

// IRC formatting control codes.
const (
	codeBold      = "\x02"
	codeItalic    = "\x1D"
	codeUnderline = "\x1F"
	codeStrike    = "\x1E"
	codeMono      = "\x11"
	codeReset     = "\x0F"
	// codeColor starts a mIRC color, followed by optional foreground and
	// background numbers.
	codeColor = "\x03"
	// codeHexColor starts a hex color, followed by optional RRGGBB values.
	codeHexColor = "\x04"
	// codeReverse swaps foreground and background colors.
	codeReverse = "\x16"
	// codeSpoiler is black text on a black background.
	codeSpoiler = "\x0301,01"
)

// style is a set of formatting flags.
type style uint8

const (
	styleBold style = 1 << iota
	styleItalic
	styleUnderline
	styleStrike
	styleMono
	styleSpoiler
)

// segment is a piece of text with a single style.
type segment struct {
	text  string
	style style
}
