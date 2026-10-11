package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		if err := sandboxExec(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	configPath := flag.String("config", "mini-cloud.json", "gateway configuration file")
	flag.Parse()
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := generateManagedFiles(cfg, localSourceDir()); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := NewGateway(cfg).Run(ctx); err != nil {
		log.Fatal(err)
	}
}
