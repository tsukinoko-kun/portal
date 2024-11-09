package net

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
	"github.com/tsukinoko-kun/portal/internal/config"
	"github.com/tsukinoko-kun/portal/internal/public"
)

type (
	Header struct {
		// Name is the name of the file.
		Name string `json:"name"`
		// Size is the size of the file in bytes.
		Size int `json:"size"`
		// LastModified is the last modified time of the file.
		LastModified int64 `json:"lastModified"`
		// Mime contains the MIME type of the file
		Mime string `json:"mime"`
	}
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins; customize this as needed
	},
}

func UploadHandler(w http.ResponseWriter, r *http.Request) {
	// Upgrade the connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("failed to upgrade connection", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	for {
		// Step 1: Receive and decode the file header
		var header Header
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Error("failed to read message", "err", err)
			return
		}

		// Check if the message is "EOT" (end of transmission)
		if string(message) == "EOT" {
			log.Debug("end of transmission")
			break
		}

		if err := json.Unmarshal(message, &header); err != nil {
			log.Error("failed to unmarshal header", "err", err)
			return
		}

		// Normalize and verify the file path
		filePath := normalizePath(header.Name)

		// Ensure the file path is within the server's working directory
		if !isInWorkingDir(filePath) {
			log.Error("file path outside working directory")
			conn.WriteMessage(websocket.TextMessage, []byte("File path outside working directory"))
			return
		}

		// Create the target directory if needed
		if err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm); err != nil {
			log.Error("failed to create directory", "err", err)
			conn.WriteMessage(websocket.TextMessage, []byte("Directory creation error"))
			return
		}

		// Open the target file for writing
		file, err := os.Create(filePath)
		if err != nil {
			log.Error("failed to create file", "err", err)
			conn.WriteMessage(websocket.TextMessage, []byte("File creation error"))
			return
		}
		defer file.Close()

		// Send "READY" message to the client
		conn.WriteMessage(websocket.TextMessage, []byte("READY"))

		// Step 2: Receive and write file chunks
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Error("failed to read message", "err", err)
				return
			}

			// Check for EOF
			if string(message) == "EOF" {
				log.Debug("end of file")
				break
			}

			// Write the chunk to the file
			if _, err := file.Write(message); err != nil {
				log.Error("failed to write file chunk", "err", err)
				return
			}
		}

		// Optionally, set the file's last modified time
		modTime := time.Unix(header.LastModified, 0)
		if err := os.Chtimes(filePath, modTime, modTime); err != nil {
			log.Error("failed to set last modified time", "err", err)
		}

		log.Info("file copy successful", "file", filePath)
	}
}

// normalizePath cleans and returns the absolute path of the file.
func normalizePath(name string) string {
	return filepath.Join(config.Path, filepath.Clean(name))
}

// isInWorkingDir checks if a path is within the server's current working directory.
func isInWorkingDir(path string) bool {
	wd, err := os.Getwd()
	if err != nil {
		log.Error("failed to get working directory", "err", err)
		return false
	}
	relPath, err := filepath.Rel(wd, path)
	if err != nil || relPath == ".." || relPath == "." || relPath[0] == '/' || filepath.IsAbs(relPath) {
		return false
	}
	return true
}

// StartServer starts the WebSocket server, prints IP/port, and handles graceful shutdown.
func StartServer() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", UploadHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.FileServerFS(public.Fs).ServeHTTP(w, r)
	})

	// Start the server on a random available port
	server := &http.Server{
		Addr:    config.Addr,
		Handler: mux,
	}

	// Listen on a random port and retrieve IP/port information
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}
	defer listener.Close()

	// Print IP + port and hostname + port
	ip, err := getLocalIP()
	if err != nil {
		return fmt.Errorf("failed to get local IP: %v", err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	host, _ := os.Hostname()
	fmt.Printf("http://%s:%s\nhttp://%s:%s\n", ip, port, host, port)

	// Signal handling for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-quit
		fmt.Println("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Warn("Server forced to shutdown", "err", err)
		}
	}()

	// Start the server
	return server.Serve(listener)
}

// getLocalIP retrieves the local IP address of the computer.
func getLocalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() && ip.To4() != nil {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no connected network interface found")
}
