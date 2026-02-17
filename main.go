package main

import (
	"context"
	"os"

	"github.com/viperadnan-git/opendebrid/cmd"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).
		With().Timestamp().Logger()

	if err := cmd.App().Run(context.Background(), os.Args); err != nil {
		log.Fatal().Err(err).Msg("application failed")
	}
}
