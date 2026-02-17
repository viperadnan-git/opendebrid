package cmd

import (
	"context"
	"fmt"

	"github.com/opendebrid/opendebrid/internal/config"
	"github.com/opendebrid/opendebrid/internal/controller"
	"github.com/urfave/cli/v3"
)

func controllerCmd() *cli.Command {
	return &cli.Command{
		Name:  "controller",
		Usage: "Run as controller node (full features + local engine)",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "database-url",
				Usage:   "PostgreSQL connection string",
				Sources: cli.EnvVars("OD_DATABASE_URL"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.Load(cmd.String("config"))
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			if v := cmd.String("database-url"); v != "" {
				cfg.Database.URL = v
			}

			if cfg.Database.URL == "" {
				return fmt.Errorf("database URL is required (set OD_DATABASE_URL env or database.url in config)")
			}

			return controller.Run(ctx, cfg)
		},
	}
}
