package main

import (
	"os"

	"github.com/charmbracelet/log"
	"github.com/tsukinoko-kun/portal/internal/config"
	"github.com/tsukinoko-kun/portal/internal/net"
)

func main() {
	if err := os.Chdir(config.Path); err != nil {
		log.Fatal(err)
	}

	if config.Debug {
		log.SetLevel(log.DebugLevel)
	}

	if err := net.StartServer(); err != nil {
		log.Fatal(err)
	}
}
