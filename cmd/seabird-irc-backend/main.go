package main

import (
	"os"

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

	nick := Env(logger, "IRC_NICK")
	user := EnvDefault("IRC_USER", nick)
	name := EnvDefault("IRC_NAME", user)

	config := seabird_irc.IRCConfig{
		Logger:      logger,
		IRCHost:     Env(logger, "IRC_HOST"),
		Nick:        nick,
		User:        user,
		Name:        name,
		SeabirdHost: Env(logger, "SEABIRD_HOST"),
		Token:       Env(logger, "SEABIRD_TOKEN"),
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
