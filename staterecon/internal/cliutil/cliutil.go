package cliutil

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

// Run prepares the shared CLI environment and executes the provided app.
func Run(app *cli.App, args []string, fatalMessage string) {
	_ = godotenv.Load()
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	if err := app.Run(args); err != nil {
		log.WithError(err).Fatal(fatalMessage)
	}
}

// SignalContext returns a context canceled on SIGINT/SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
