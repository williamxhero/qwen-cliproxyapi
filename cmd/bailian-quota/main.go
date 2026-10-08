package main

import (
	"context"
	"os"
	"os/signal"

	"qwen-cliproxyapi/internal/bailianquota"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := bailianquota.RunCLI(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
