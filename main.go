package main

import (
	"log"
	"os"

	"github.com/tsukinoko-kun/portal/internal/config"
	"github.com/tsukinoko-kun/portal/internal/net"
)

func main() {
	if err := os.Chdir(config.Path); err != nil {
		log.Fatal(err)
	}

	if err := net.StartServer(); err != nil {
		log.Fatal(err)
	}
}
