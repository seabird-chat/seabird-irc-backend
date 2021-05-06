module github.com/seabird-chat/seabird-irc-backend

go 1.13

require (
	github.com/go-irc/irc/v4 v4.0.0-alpha.7
	github.com/go-irc/ircx v0.0.0-20210506174733-5a5960d66823
	github.com/joho/godotenv v1.3.0
	github.com/mattn/go-isatty v0.0.12
	github.com/rs/zerolog v1.21.0
	github.com/seabird-chat/seabird-go v0.4.0
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c
)

// replace github.com/go-irc/ircx => ../../ircx
