package main

import (
	"flag"
	"fmt"
	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
	"github.com/tsukinoko-kun/portal/public"
	"net"
	"net/http"
	"os"
)

const (
	chunkSize = 1024 * 1024 // 1MB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize: chunkSize,
}

type (
	Header struct {
		// Name is the name of the file.
		Name string `json:"name"`
		// Size is the size of the file in bytes.
		Size int `json:"size"`
	}
)

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("failed to upgrade connection", "err", err)
		return
	}
	defer conn.Close()

	// Read header
	header := Header{}
	err = conn.ReadJSON(&header)
	if err != nil {
		log.Error("failed to read header", "err", err)
		return
	}
	log.Info("received header", "header", header)

	file, err := os.Create(header.Name)
	if err != nil {
		log.Error("failed to create file", "err", err)
		return
	}
	defer file.Close()

	// Send READY signal to start receiving file chunks
	err = conn.WriteMessage(websocket.TextMessage, []byte("READY"))
	if err != nil {
		log.Error("failed to write message", "err", err)
		return
	}

	// Read file chunk by chunk because the file might be too large to fit in memory
	for {
		messageType, p, err := conn.ReadMessage()
		if err != nil {
			if _, ok := err.(*websocket.CloseError); ok {
				log.Warn("connection closed by client", "err", err)
				return
			}
			log.Error("failed to read message", "err", err)
			return
		}

		if messageType == websocket.TextMessage && string(p) == "EOF" {
			break
		}

		if messageType == websocket.BinaryMessage {
			_, err = file.Write(p)
			if err != nil {
				log.Error("failed to write to file", "err", err)
				return
			}
		}

		err = conn.WriteMessage(websocket.TextMessage, []byte("READY"))
		if err != nil {
			log.Error("failed to write message", "err", err)
			return
		}
	}

	// send EOF to client to signal that the file has been received
	err = conn.WriteMessage(websocket.TextMessage, []byte("EOF"))
	if err != nil {
		log.Error("failed to write message", "err", err)
		return
	}

	log.Info("file written", "name", header.Name)
}

func getPublicIP() (string, error) {
	var publicIP string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if ip, ok := addr.(*net.IPNet); ok && !ip.IP.IsLoopback() {
			if ip.IP.To4() != nil { // IPv4
				publicIP = ip.IP.String()
				break
			}
		}
	}
	return publicIP, nil
}

func main() {
	port := flag.Int("port", 0, "port to listen on")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backdrop.webp":
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write(public.BackdropWebP)
		case "/favicon.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write(public.FaviconSVG)
		case "/index.css":
			w.Header().Set("Content-Type", "text/css")
			_, _ = w.Write(public.IndexCSS)
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(public.IndexHTML)
		case "/index.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(public.IndexJS)
		default:
			http.NotFound(w, r)
		}
	})
	http.HandleFunc("/ws", wsHandler)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatal("failed to listen", "err", err)
		return
	}

	log.Info("server started", "addr", ln.Addr())

	if publicIP, err := getPublicIP(); err == nil {
		fmt.Printf("Portal available at http://%s:%d\n", publicIP, ln.Addr().(*net.TCPAddr).Port)
	}

	if err := http.Serve(ln, nil); err != nil {
		log.Fatal("failed to serve", "err", err)
		return
	}
}
