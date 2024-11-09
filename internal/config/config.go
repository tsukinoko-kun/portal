package config

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/charmbracelet/log"
)

var (
	Addr  string
	Path  string
	Debug bool
)

func init() {
	port := flag.Int("port", 0, "port to listen on")
	flag.StringVar(&Path, "path", ".", "path to serve")
	flag.BoolVar(&Debug, "debug", false, "enable debug logging")

	flag.Parse()

	Addr = fmt.Sprintf(":%d", *port)

	if p, err := filepath.Abs(Path); err == nil {
		Path = p
	} else {
		log.Fatal(err)
	}
}
