package main

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/cmd"
)

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "3:04:05PM"}).
		With().Timestamp().Logger()

	if err := cmd.App().Run(context.Background(), os.Args); err != nil {
		log.Fatal().Err(err).Msg("application failed")
	}
}
