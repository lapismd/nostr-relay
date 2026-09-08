package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"zooid/zooid"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	zooid.Start(ctx)
}
