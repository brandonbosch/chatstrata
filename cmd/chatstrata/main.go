// Command chatstrata is a personal, queryable archive of AI conversations.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/brandonbosch/chatstrata/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
