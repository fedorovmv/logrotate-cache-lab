package writer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"logrotate-cache-lab/internal/record"
	"logrotate-cache-lab/internal/report"
)

type Config struct {
	RunID          string
	Path           string
	ListenAddress  string
	StatePath      string
	RecordBytes    int
	BufferBytes    int
	BytesPerSecond int64
	FlushInterval  time.Duration
	ResidentBytes  int64
}

type State = report.WriterState

type Writer struct {
	cfg      Config
	listener net.Listener
	ready    chan struct{}
	reopen   chan struct{}
	mu       sync.RWMutex
	state    State
}

func New(cfg Config) (*Writer, error) {
	if cfg.RunID == "" || cfg.Path == "" || cfg.RecordBytes <= 0 || cfg.BufferBytes <= 0 || cfg.BytesPerSecond <= 0 {
		return nil, fmt.Errorf("invalid writer configuration")
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 100 * time.Millisecond
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return nil, err
	}
	return &Writer{
		cfg:      cfg,
		listener: listener,
		ready:    make(chan struct{}),
		reopen:   make(chan struct{}, 1),
	}, nil
}

func (w *Writer) Address() string { return w.listener.Addr().String() }

func (w *Writer) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ready:
		return nil
	}
}

func (w *Writer) State() State {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

func (w *Writer) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(w.cfg.Path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(w.cfg.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	resident := make([]byte, w.cfg.ResidentBytes)
	for i := 0; i < len(resident); i += os.Getpagesize() {
		resident[i] = 1
	}
	defer runtime.KeepAlive(resident)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/state", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(w.State())
	})
	mux.HandleFunc("/reopen-logs", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "POST required", http.StatusMethodNotAllowed)
			return
		}
		select {
		case w.reopen <- struct{}{}:
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte("queued\n"))
		default:
			http.Error(response, "reopen already queued", http.StatusServiceUnavailable)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(w.listener) }()
	close(w.ready)

	generateTicker := time.NewTicker(10 * time.Millisecond)
	flushTicker := time.NewTicker(w.cfg.FlushInterval)
	defer generateTicker.Stop()
	defer flushTicker.Stop()
	var buffer bytes.Buffer
	var byteCredit int64
	lastGenerate := time.Now()

	flush := func() error {
		if buffer.Len() == 0 {
			return nil
		}
		data := buffer.Bytes()
		written := 0
		for written < len(data) {
			n, writeErr := file.Write(data[written:])
			written += n
			if writeErr != nil {
				return writeErr
			}
			if n == 0 {
				return errors.New("zero-byte log write")
			}
		}
		count := uint64(len(data) / w.cfg.RecordBytes)
		buffer.Reset()
		w.mu.Lock()
		w.state.Written += count
		w.mu.Unlock()
		return nil
	}

	finish := func(runErr error) error {
		if flushErr := flush(); runErr == nil {
			runErr = flushErr
		}
		if closeErr := file.Close(); runErr == nil {
			runErr = closeErr
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		serveErr := <-serverDone
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && runErr == nil {
			runErr = serveErr
		}
		if runErr != nil {
			w.mu.Lock()
			w.state.LastError = runErr.Error()
			w.mu.Unlock()
		}
		if w.cfg.StatePath != "" {
			if stateErr := report.WriteJSONAtomic(w.cfg.StatePath, w.State()); runErr == nil {
				runErr = stateErr
			}
		}
		return runErr
	}

	for {
		select {
		case <-ctx.Done():
			return finish(nil)
		case now := <-generateTicker.C:
			byteCredit += w.cfg.BytesPerSecond * now.Sub(lastGenerate).Nanoseconds() / int64(time.Second)
			lastGenerate = now
			for byteCredit >= int64(w.cfg.RecordBytes) {
				w.mu.Lock()
				w.state.Generated++
				sequence := w.state.Generated
				w.mu.Unlock()
				encoded, encodeErr := record.Encode(w.cfg.RunID, sequence, w.cfg.RecordBytes)
				if encodeErr != nil {
					return finish(encodeErr)
				}
				_, _ = buffer.Write(encoded)
				byteCredit -= int64(w.cfg.RecordBytes)
			}
			if buffer.Len() >= w.cfg.BufferBytes {
				if err := flush(); err != nil {
					return finish(err)
				}
			}
		case <-flushTicker.C:
			if err := flush(); err != nil {
				return finish(err)
			}
		case <-w.reopen:
			w.mu.Lock()
			w.state.ReopenRequests++
			w.mu.Unlock()
			if err := file.Close(); err != nil {
				return finish(err)
			}
			file, err = os.OpenFile(w.cfg.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
			if err != nil {
				w.mu.Lock()
				w.state.ReopenFailures++
				w.state.LastError = err.Error()
				w.mu.Unlock()
				return finish(fmt.Errorf("reopen log: %w", err))
			}
			if err := flush(); err != nil {
				return finish(err)
			}
		}
	}
}
