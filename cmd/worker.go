package cmd

import (
	"context"
	"fmt"

	"github.com/opendebrid/opendebrid/internal/config"
	"github.com/opendebrid/opendebrid/internal/worker"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v3"
)

func workerCmd() *cli.Command {
	return &cli.Command{
		Name:  "worker",
		Usage: "Run as worker node (engines + file server, registers with controller)",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "controller-url",
				Usage:   "Controller endpoint (host:port)",
				Sources: cli.EnvVars("OD_CONTROLLER_URL"),
			},
			&cli.StringFlag{
				Name:    "controller-token",
				Usage:   "Auth token for controller registration",
				Sources: cli.EnvVars("OD_CONTROLLER_TOKEN"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.Load(cmd.String("config"))
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			if v := cmd.String("controller-url"); v != "" {
				cfg.Controller.URL = v
			}
			if v := cmd.String("controller-token"); v != "" {
				cfg.Controller.Token = v
			}
			if v := cmd.String("log-level"); v != "" {
				cfg.Logging.Level = v
			}

			if cfg.Controller.URL == "" {
				return fmt.Errorf("OD_CONTROLLER_URL is required")
			}
			if cfg.Controller.Token == "" {
				return fmt.Errorf("OD_CONTROLLER_TOKEN is required")
			}

			log.Info().Str("controller", cfg.Controller.URL).Msg("starting worker")

			return worker.Run(ctx, cfg)
		},
	}
}
