package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
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
		// Mime contains the MIME type of the file
		Mime string `json:"mime"`
	}

	TransmissionReceiver struct {
		conn *websocket.Conn
	}

	TransmissionSignal string
)

const (
	SignalNone  TransmissionSignal = ""
	SignalReady TransmissionSignal = "READY"
	SignalEOF   TransmissionSignal = "EOF"
	SignalEOT   TransmissionSignal = "EOT"
)

func (t TransmissionSignal) Into() []byte {
	return []byte(t)
}

func (t *TransmissionReceiver) Error(message any) {
	switch v := message.(type) {
	case string:
		_ = t.conn.WriteMessage(websocket.CloseProtocolError, websocket.FormatCloseMessage(websocket.CloseNormalClosure, v))
	case error:
		_ = t.conn.WriteMessage(websocket.CloseProtocolError, websocket.FormatCloseMessage(websocket.CloseNormalClosure, v.Error()))
	}
}

var (
	EotError = errors.New("end of portal transmission")
)

func (t *TransmissionReceiver) End() error {
	return EotError
}

func (t *TransmissionReceiver) Read() ([]byte, TransmissionSignal, error) {
	ty, b, err := t.conn.ReadMessage()
	if err != nil {
		return nil, SignalNone, err
	}

	switch ty {
	case websocket.TextMessage:
		str := string(b[:])
		return nil, TransmissionSignal(str), nil
	case websocket.BinaryMessage:
		return b, SignalNone, nil
	}

	return nil, SignalNone, nil
}

func (t *TransmissionReceiver) Signal(signal TransmissionSignal) error {
	return t.conn.WriteMessage(websocket.TextMessage, signal.Into())
}

func (t *TransmissionReceiver) ReadHeader() (header Header, err error) {
	_, b, err := t.conn.ReadMessage()
	if err != nil {
		return header, errors.Join(errors.New("failed to read message expected header"), err)
	}

	if s := TransmissionSignal(b[:]); s == SignalEOT {
		log.Debug("received EOT")
		return header, EotError
	}

	if err := json.Unmarshal(b, &header); err != nil {
		log.Error("unmarshalling failed", "err", err)
		return header, errors.Join(errors.New("failed to unmarshal header"), err)
	}

	return header, nil
}

func (t *TransmissionReceiver) CreateFileWriter(h Header) (*os.File, error) {
	p := filepath.Join(wd, h.Name)

	// check if file is inside wd
	if relPath, err := filepath.Rel(wd, p); err != nil || relPath == ".." || relPath[:2] == ".." {
		log.Error("file is outside working directory", "path", p)
		return nil, fmt.Errorf("file is outside working directory %s", p)
	}

	// create parent directories
	parentDir := filepath.Dir(p)
	if err := os.MkdirAll(parentDir, 0777); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to create parent directory %s", p), err)
	}

	// create file
	f, err := os.Create(p)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to create file %s", p), err)
	}
	log.Debug("file created", "path", p)
	return f, nil
}

func (t *TransmissionReceiver) Copy(dst io.Writer) error {
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

		if _, err := io.Copy(dst, gzipReader); err != nil {
			t.Error(errors.Join(errors.New("failed to copy data"), err))
		} else {
			if err := t.Signal(SignalEOF); err != nil {
				log.Error("Failed to signal EOF", "err", err)
			}
		}
	}()

	// Read and decompress chunks as they arrive
	for {
		message, s, err := t.Read()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				log.Debug("WebSocket closed normally.")
				return nil
			} else {
				return errors.Join(errors.New("failed to read data"), err)
			}
		}

		if s == SignalEOF {
			log.Debug("received EOF")
			break
		}

		if s == SignalEOT {
			log.Debug("received EOT")
			return t.End()
		}

		// Write the compressed chunk to the pipe, which the gzip reader will decompress
		if _, err = pipeWriter.Write(message); err != nil {
			return errors.Join(errors.New("failed to write data to compression pipe"), err)
		}
	}

	if err := pipeWriter.Close(); err != nil {
		return errors.Join(errors.New("failed to close compression pipe"), err)
	}

	return nil
}

func (t *TransmissionReceiver) Process() error {
	h, err := t.ReadHeader()
	if err != nil {
		return errors.Join(errors.New("failed to read header"), err)
	}
	if len(h.Name) == 0 {
		return errors.New("received invalid header")
	}
	log.Debug("received", "header", h)

	if err := t.Signal(SignalReady); err != nil {
		return errors.Join(errors.New("failed to send READY signal"), err)
	}

	f, err := t.CreateFileWriter(h)
	if err != nil {
		return errors.Join(errors.New("failed to create file"), err)
	}
	defer func() {
		if err = f.Close(); err != nil {
			log.Error("failed to close file", "err", err, "file", f.Name())
		}

		// set last modified time
		lastModified := time.UnixMilli(h.LastModified)
		log.Debug("set last modified time", "file", f.Name(), "last_modified", lastModified)
		if err := os.Chtimes(f.Name(), lastModified, lastModified); err != nil {
			log.Error("failed to set last modified time", "err", err)
		}
	}()

	copyErr := t.Copy(f)

	if copyErr != nil {
		log.Error("failed to copy file", "err", copyErr)
	} else {
		log.Info("successfully copied file", "dst", f.Name())
	}

	return copyErr
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("failed to upgrade connection", "err", err)
		return
	}
	defer conn.Close()

	t := TransmissionReceiver{
		conn: conn,
	}

	for {
		if err := t.Process(); err != nil {
			if errors.Is(err, EotError) {
				break
			}
			log.Error("portal protocol failed", "err", err)
			t.Error(err)
			<-time.After(time.Second)
			break
		}
	}
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

// cli options
var (
	port  *int
	path  *string
	debug *bool
)

func parseOptions() {
	port = flag.Int("port", 0, "port to listen on")
	path = flag.String("path", ".", "path to save files")
	debug = flag.Bool("debug", false, "enable debug logging")
	flag.Parse()
}

func applyPath() {
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
}

func applyLogger() {
	if *debug {
		log.SetLevel(log.DebugLevel)
		log.Debug("debug logging enabled")
	} else {
		log.SetLevel(log.InfoLevel)
	}
}

func setupHttpHandlers() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.FileServerFS(public.Fs).ServeHTTP(w, r)
	})
	http.HandleFunc("/ws", wsHandler)
}

func main() {
	parseOptions()
	applyPath()
	applyLogger()

	setupHttpHandlers()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatal("failed to listen", "err", err)
		return
	}

	log.Debug("server started", "addr", ln.Addr())

	if publicIP, err := getPublicIP(); err == nil {
		fmt.Printf("Portal available at http://%s:%d\n", publicIP, ln.Addr().(*net.TCPAddr).Port)
	}

	if err := http.Serve(ln, nil); err != nil {
		log.Fatal("failed to serve", "err", err)
		return
	}
}
