package main

import (
	"context"
	_ "embed"
	"log"
	"os"

	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name: "ripple",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "mastodon-server",
				Required: true,
				Sources:  cli.EnvVars("MASTODON_SERVER"),
			},
			&cli.StringFlag{
				Name:     "mastodon-client-id",
				Required: true,
				Sources:  cli.EnvVars("MASTODON_CLIENT_ID"),
			},
			&cli.StringFlag{
				Name:     "mastodon-client-secret",
				Required: true,
				Sources:  cli.EnvVars("MASTODON_CLIENT_SECRET"),
			},
			&cli.StringFlag{
				Name:     "mastodon-access-token",
				Required: true,
				Sources:  cli.EnvVars("MASTODON_ACCESS_TOKEN"),
			},
			&cli.StringFlag{
				Name:     "anthropic-api-key",
				Required: true,
				Sources:  cli.EnvVars("ANTHROPIC_API_KEY"),
			},
			&cli.StringFlag{
				Name:    "cloudflare-ai-gateway",
				Value:   "",
				Sources: cli.EnvVars("CLOUDFLARE_AI_GATEWAY"),
			},
		},
		Commands: []*cli.Command{
			{
				Name:   "check-notifications",
				Action: RunCheckNotifications,
			},
			/*{
				Name:   "quote",
				Action: RunQuote,
			},
			{
				Name:   "reply",
				Action: RunReply,
			},*/
		},
		DefaultCommand: "check-notifications",
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
