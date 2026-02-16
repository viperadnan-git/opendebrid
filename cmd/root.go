package cmd

import (
	"github.com/urfave/cli/v3"
)

func App() *cli.Command {
	return &cli.Command{
		Name:  "opendebrid",
		Usage: "Self-hosted multi-node debrid service",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "Path to TOML config file",
				Sources: cli.EnvVars("OD_CONFIG_PATH"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "Log level (debug, info, warn, error)",
				Value:   "info",
				Sources: cli.EnvVars("OD_LOGGING_LEVEL"),
			},
		},
		Commands: []*cli.Command{
			controllerCmd(),
			workerCmd(),
		},
	}
}
