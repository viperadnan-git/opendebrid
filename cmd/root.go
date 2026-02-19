package cmd

import (
	"github.com/urfave/cli/v3"
)

var version = "dev"

func App() *cli.Command {
	return &cli.Command{
		Name:    "opendebrid",
		Version: version,
		Usage:   "Self-hosted debrid — distributed by design. Download anything, anywhere on your own infrastructure.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "Path to TOML config file",
				Sources: cli.EnvVars("OPENDEBRID_CONFIG_PATH"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "Log level (debug, info, warn, error)",
				Value:   "info",
				Sources: cli.EnvVars("OPENDEBRID_LOGGING_LEVEL"),
			},
		},
		Commands: []*cli.Command{
			controllerCmd(),
			workerCmd(),
			migrateCmd(),
		},
	}
}
