package debuglog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNopLogger_DoesNotPanic(t *testing.T) {
	l := NewNopLogger()
	l.Log(Event{
		Component: "broker",
		Action:    "publish",
		Channel:   "orders",
		MessageID: "msg-1",
	})
}

func TestFileLogger_WritesJSONLines(t *testing.T) {
	dir := t.TempDir()
	l, err := NewFileLogger(dir, RotationDaily)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	now := time.Date(2026, 3, 29, 14, 0, 0, 0, time.UTC)
	l.Log(Event{
		Timestamp: now,
		Component: "broker",
		Action:    "publish",
		Channel:   "orders",
		MessageID: "msg-1",
	})
	l.Log(Event{
		Timestamp: now,
		Component: "cache",
		Action:    "cache_set",
		Key:       "foo",
	})
	l.Close()

	data, err := os.ReadFile(filepath.Join(dir, "nikodis-2026-03-29.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var e Event
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Action != "publish" || e.Channel != "orders" {
		t.Errorf("unexpected event: %+v", e)
	}
}

func TestFileLogger_HourlyRotation(t *testing.T) {
	dir := t.TempDir()
	l, err := NewFileLogger(dir, RotationHourly)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.Log(Event{Timestamp: time.Date(2026, 3, 29, 14, 0, 0, 0, time.UTC), Component: "broker", Action: "publish"})
	l.Log(Event{Timestamp: time.Date(2026, 3, 29, 15, 0, 0, 0, time.UTC), Component: "broker", Action: "publish"})
	l.Close()

	files, _ := filepath.Glob(filepath.Join(dir, "nikodis-*.log"))
	if len(files) != 2 {
		t.Fatalf("expected 2 files for hourly rotation, got %d", len(files))
	}
}
