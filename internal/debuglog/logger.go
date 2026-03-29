package debuglog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Component string    `json:"component"`
	Action    string    `json:"action"`
	Namespace string    `json:"namespace,omitempty"`
	Channel   string    `json:"channel,omitempty"`
	Key       string    `json:"key,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
}

type Logger interface {
	Log(event Event)
	Close() error
}

type NopLogger struct{}

func NewNopLogger() *NopLogger { return &NopLogger{} }
func (n *NopLogger) Log(Event) {}
func (n *NopLogger) Close() error { return nil }

type Rotation int

const (
	RotationDaily  Rotation = iota
	RotationHourly
)

type FileLogger struct {
	dir      string
	rotation Rotation
	mu       sync.Mutex
	file     *os.File
	current  string
}

func NewFileLogger(dir string, rotation Rotation) (*FileLogger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	return &FileLogger{dir: dir, rotation: rotation}, nil
}

func (f *FileLogger) Log(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n')

	f.mu.Lock()
	defer f.mu.Unlock()

	name := f.fileName(e.Timestamp)
	if name != f.current {
		if f.file != nil {
			f.file.Close()
		}
		file, err := os.OpenFile(filepath.Join(f.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		f.file = file
		f.current = name
	}
	f.file.Write(data)
}

func (f *FileLogger) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file != nil {
		err := f.file.Close()
		f.file = nil
		f.current = ""
		return err
	}
	return nil
}

func (f *FileLogger) fileName(t time.Time) string {
	switch f.rotation {
	case RotationHourly:
		return fmt.Sprintf("nikodis-%s.log", t.Format("2006-01-02T15"))
	default:
		return fmt.Sprintf("nikodis-%s.log", t.Format("2006-01-02"))
	}
}
