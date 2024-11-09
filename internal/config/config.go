package config

import (
	"flag"
	"fmt"
)

var (
	Addr string
	Path string
)

func init() {
	port := flag.Int("port", 0, "port to listen on")
	flag.StringVar(&Path, "path", ".", "path to serve")

	flag.Parse()

	Addr = fmt.Sprintf(":%d", *port)
}
