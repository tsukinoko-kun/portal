package setup

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
)

var (
	Wd string
)

func init() {
	var err error
	if Wd, err = os.Getwd(); err != nil {
		log.Fatal("failed to get working directory", "err", err)
	}

	parseOptions()
	applyPath()
	applyLogger()
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
	p := filepath.Join(Wd, h.Name)

	// check if file is inside wd
	if relPath, err := filepath.Rel(Wd, p); err != nil || relPath == ".." || relPath[:2] == ".." {
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

// cli options
var (
	Port  *int
	Path  *string
	Debug *bool
)

func parseOptions() {
	Port = flag.Int("port", 0, "port to listen on")
	Path = flag.String("path", ".", "path to save files")
	Debug = flag.Bool("debug", false, "enable debug logging")
	flag.Parse()
}

func applyPath() {
	if *Path != "." {
		if err := os.MkdirAll(*Path, 0777); err != nil {
			log.Fatal("failed to create directory", "path", Path, "err", err)
			return
		}
		if err := os.Chdir(*Path); err != nil {
			log.Fatal("failed to change directory", "err", err)
			return
		}
		var err error
		if Wd, err = os.Getwd(); err == nil {
			log.Info("working directory", "path", Wd)
		}
	}
}

func applyLogger() {
	if *Debug {
		log.SetLevel(log.DebugLevel)
		log.Debug("debug logging enabled")
	} else {
		log.SetLevel(log.InfoLevel)
	}
}
