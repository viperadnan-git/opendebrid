package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
	"github.com/viperadnan-git/opendebrid/internal/config"
	"github.com/viperadnan-git/opendebrid/internal/database"
)

func migrateCmd() *cli.Command {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:    "database-url",
			Usage:   "PostgreSQL connection string",
			Sources: cli.EnvVars("OD_DATABASE_URL"),
		},
	}

	return &cli.Command{
		Name:  "migrate",
		Usage: "Run database migrations",
		Commands: []*cli.Command{
			{
				Name:  "up",
				Usage: "Apply all pending migrations",
				Flags: flags,
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load(cmd.String("config"))
					if err != nil {
						return fmt.Errorf("load config: %w", err)
					}
					if v := cmd.String("database-url"); v != "" {
						cfg.Database.URL = v
					}
					if cfg.Database.URL == "" {
						return fmt.Errorf("database URL is required (set OD_DATABASE_URL or --database-url)")
					}

					pool, err := database.Connect(ctx, cfg.Database.URL, cfg.Database.MaxConnections)
					if err != nil {
						return fmt.Errorf("connect to database: %w", err)
					}
					defer pool.Close()

					return database.Migrate(ctx, pool)
				},
			},
			{
				Name:  "down",
				Usage: "Roll back the last migration",
				Flags: flags,
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load(cmd.String("config"))
					if err != nil {
						return fmt.Errorf("load config: %w", err)
					}
					if v := cmd.String("database-url"); v != "" {
						cfg.Database.URL = v
					}
					if cfg.Database.URL == "" {
						return fmt.Errorf("database URL is required (set OD_DATABASE_URL or --database-url)")
					}

					pool, err := database.Connect(ctx, cfg.Database.URL, cfg.Database.MaxConnections)
					if err != nil {
						return fmt.Errorf("connect to database: %w", err)
					}
					defer pool.Close()

					return database.MigrateDown(ctx, pool)
				},
			},
		},
	}
}
