package main

import "github.com/tsukinoko-kun/portal/internal/net"

func main() {
	if err := net.Listen(); err != nil {
		panic(err)
	}
}
