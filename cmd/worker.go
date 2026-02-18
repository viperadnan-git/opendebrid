package cmd

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v3"
	"github.com/viperadnan-git/opendebrid/internal/config"
	"github.com/viperadnan-git/opendebrid/internal/worker"
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
				Name:    "worker-token",
				Usage:   "Auth token for controller registration",
				Sources: cli.EnvVars("OD_NODE_AUTH_TOKEN"),
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
			if v := cmd.String("worker-token"); v != "" {
				cfg.Node.AuthToken = v
			}
			if v := cmd.String("log-level"); v != "" {
				cfg.Logging.Level = v
			}

			if cfg.Controller.URL == "" {
				return fmt.Errorf("OD_CONTROLLER_URL is required")
			}
			if cfg.Node.AuthToken == "" {
				return fmt.Errorf("OD_NODE_AUTH_TOKEN is required")
			}
			if cfg.Node.ID == "" {
				return fmt.Errorf("OD_NODE_ID is required for workers")
			}

			log.Info().Str("controller", cfg.Controller.URL).Msg("starting worker")

			return worker.Run(ctx, cfg)
		},
	}
}
