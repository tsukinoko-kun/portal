package main

import (
	"compress/gzip"
	"flag"
	"fmt"
	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
	"github.com/tsukinoko-kun/portal/public"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var (
	upgrader = websocket.Upgrader{}
	wd       string
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
		_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to read header"))
		return
	}
	log.Debug("received header", "header", header)

	p := filepath.Join(wd, header.Name)

	// check if file is inside wd
	if relPath, err := filepath.Rel(wd, p); err != nil || relPath == ".." || relPath[:2] == ".." {
		log.Error("file is outside working directory", "path", p)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("file is outside working directory"))
		return
	}

	// create parent directories
	if err := os.MkdirAll(filepath.Dir(p), 0777); err != nil {
		log.Error("failed to create parent directories", "err", err)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to create parent directories"))
		return
	}

	// create file
	f, err := os.Create(p)
	if err != nil {
		log.Error("failed to create file", "err", err)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to create file"))
		return
	}
	defer f.Close()
	log.Debug("file created", "path", p)

	// Send READY signal to start receiving file chunks
	err = conn.WriteMessage(websocket.TextMessage, []byte("READY"))
	if err != nil {
		log.Error("failed to write message", "err", err)
		return
	}
	log.Debug("READY signal sent")

	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	log.Debug("pipe created")

	wg := sync.WaitGroup{}
	wg.Add(1)

	go func() {
		defer wg.Done()
		log.Debug("creating Gzip reader")
		gzipReader, err := gzip.NewReader(pipeReader)
		if err != nil {
			log.Error("Failed to create Gzip reader", "err", err)
			return
		}
		defer gzipReader.Close()
		log.Debug("Gzip reader created")

		if _, err := io.Copy(f, gzipReader); err != nil {
			log.Error("failed to copy data", "err", err)
			_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to copy data"))
		} else {
			_ = conn.WriteMessage(websocket.TextMessage, []byte("EOF"))
		}
	}()

	// Read and decompress chunks as they arrive
	compressetSize := atomic.Int32{}
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				log.Debug("WebSocket closed normally.")
			} else {
				log.Error("WebSocket read", "err", err)
				_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to read message"))
				return
			}
			break
		}
		log.Debug("received chunk", "size", len(message))

		// check if message is "EOF"
		if len(message) == 3 && message[0] == 'E' && message[1] == 'O' && message[2] == 'F' {
			log.Debug("EOF received")
			break
		}

		// update compressed size
		compressetSize.Add(int32(len(message)))

		// Write the compressed chunk to the pipe, which the gzip reader will decompress
		if _, err = pipeWriter.Write(message); err != nil {
			log.Error("writing to pipe", "err", err)
			_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to write to pipe"))
			return
		} else {
			log.Debug("chunk written to pipe", "size", len(message))
		}
	}

	log.Info("written", "size", compressetSize.Load(), "full size", header.Size)

	if err := pipeWriter.Close(); err != nil {
		log.Error("failed to close pipe writer", "err", err)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to close pipe writer"))
	} else {
		log.Debug("pipe writer closed")
	}

	// set last modified time
	lastModified := time.UnixMilli(header.LastModified)
	if err := os.Chtimes(p, lastModified, lastModified); err != nil {
		log.Error("failed to set last modified time", "err", err)
	}

	log.Info("file written", "name", header.Name)
	wg.Wait()
}

func getPublicIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ip, ok := addr.(*net.IPNet); ok && !ip.IP.IsLoopback() {
			if ip.IP.To4() != nil {
				return ip.IP.String(), nil
			}
		}
	}

	for _, addr := range addrs {
		if ip, ok := addr.(*net.IPNet); ok && !ip.IP.IsLoopback() {
			if ip.IP.To16() != nil {
				return ip.IP.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no public IP found")
}

func main() {
	port := flag.Int("port", 0, "port to listen on")
	path := flag.String("path", ".", "path to save files")
	debug := flag.Bool("debug", false, "enable debug logging")
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

	if *debug {
		log.SetLevel(log.DebugLevel)
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
