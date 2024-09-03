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
	"path/filepath"
	"time"
)

const (
	chunkSize = 1024 * 1024 // 1MB
)

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize: chunkSize,
	}
	wd string
)

func init() {
	var err error
	if wd, err = os.Getwd(); err != nil {
		log.Fatal("failed to get working directory", "err", err)
	}
}

type (
	Header struct {
		// Name is the name of the file.
		Name string `json:"name"`
		// Size is the size of the file in bytes.
		Size int `json:"size"`
		// LastModified is the last modified time of the file.
		LastModified int64 `json:"lastModified"`
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

	p := filepath.Join(wd, header.Name)

	// check if file is inside wd
	if relPath, err := filepath.Rel(wd, p); err != nil || relPath == ".." || relPath[:2] == ".." {
		log.Error("file is outside working directory", "path", p)
		return
	}

	// create parent directories
	if err := os.MkdirAll(filepath.Dir(p), 0777); err != nil {
		log.Error("failed to create parent directories", "err", err)
		return
	}

	// create file
	f, err := os.Create(p)
	if err != nil {
		log.Error("failed to create file", "err", err)
		return
	}
	defer f.Close()

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
			_, err = f.Write(p)
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

	// set last modified time
	lastModified := time.UnixMilli(header.LastModified)
	if err := os.Chtimes(p, lastModified, lastModified); err != nil {
		log.Error("failed to set last modified time", "err", err)
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
	path := flag.String("path", ".", "path to save files")
	flag.Parse()

	if *path != "." {
		if err := os.MkdirAll(*path, 0777); err != nil {
			log.Fatal("failed to create directory", "path", path, "err", err)
			return
		}
		if err := os.Chdir(*path); err != nil {
			log.Fatal("failed to change directory", "err", err)
			return
		}
		var err error
		if wd, err = os.Getwd(); err == nil {
			log.Info("working directory", "path", wd)
		}
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.FileServerFS(public.Fs).ServeHTTP(w, r)
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
