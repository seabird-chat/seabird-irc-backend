package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"

	seabird_irc "github.com/seabird-chat/seabird-irc-backend"
)

func EnvDefault(key string, def string) string {
	if ret, ok := os.LookupEnv(key); ok {
		return ret
	}
	return def
}

func Env(logger zerolog.Logger, key string) string {
	ret, ok := os.LookupEnv(key)

	if !ok {
		logger.Fatal().Str("var", key).Msg("Required environment variable not found")
	}

	return ret
}

func main() {
	// Attempt to load from .env if it exists
	_ = godotenv.Load()

	var logger zerolog.Logger

	if isatty.IsTerminal(os.Stdout.Fd()) {
		logger = zerolog.New(zerolog.NewConsoleWriter())
	} else {
		logger = zerolog.New(os.Stdout)
	}

	logger = logger.With().Timestamp().Logger()
	logger.Level(zerolog.DebugLevel)

	nick := Env(logger, "IRC_NICK")
	user := EnvDefault("IRC_USER", nick)
	name := EnvDefault("IRC_NAME", user)

	nickCheckDuration, err := time.ParseDuration(EnvDefault("NICK_CHECK_DURATION", "1m"))
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to parse NICK_CHECK_DURATION")
	}

	ircDebug, err := strconv.ParseBool(EnvDefault("IRC_DEBUG", "false"))
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to parse IRC_DEBUG")
	}

	config := seabird_irc.IRCConfig{
		IRCID:             EnvDefault("IRC_ID", "seabird"),
		CommandPrefix:     EnvDefault("IRC_COMMAND_PREFIX", "!"),
		Logger:            logger,
		IRCHost:           Env(logger, "IRC_HOST"),
		Nick:              nick,
		User:              user,
		Name:              name,
		Pass:              EnvDefault("IRC_PASS", ""),
		Debug:             ircDebug,
		NickServPass:      EnvDefault("IRC_NICKSERV_PASS", ""),
		NickCheckDuration: nickCheckDuration,
		Channels:          strings.Split(EnvDefault("IRC_CHANNELS", ""), ","),
		SeabirdHost:       Env(logger, "SEABIRD_HOST"),
		SeabirdToken:      Env(logger, "SEABIRD_TOKEN"),
	}

	backend, err := seabird_irc.New(config)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to load backend")
	}

	err = backend.Run()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to run backend")
	}
}
