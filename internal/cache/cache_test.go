package cache

import (
	"testing"
	"time"
)

func TestStore_SetAndGet(t *testing.T) {
	s := New(nil)
	defer s.Close()

	s.Set("key1", []byte("value1"), 0)
	val, found := s.Get("key1")
	if !found {
		t.Fatal("expected found")
	}
	if string(val) != "value1" {
		t.Errorf("expected 'value1', got %q", val)
	}
}

func TestStore_GetMissing(t *testing.T) {
	s := New(nil)
	defer s.Close()

	_, found := s.Get("nope")
	if found {
		t.Error("expected not found")
	}
}

func TestStore_Delete(t *testing.T) {
	s := New(nil)
	defer s.Close()

	s.Set("key1", []byte("value1"), 0)
	deleted := s.Delete("key1")
	if !deleted {
		t.Error("expected deleted=true")
	}
	_, found := s.Get("key1")
	if found {
		t.Error("expected not found after delete")
	}
}

func TestStore_DeleteMissing(t *testing.T) {
	s := New(nil)
	defer s.Close()

	deleted := s.Delete("nope")
	if deleted {
		t.Error("expected deleted=false for missing key")
	}
}

func TestStore_TTLExpiry(t *testing.T) {
	s := New(nil)
	defer s.Close()

	s.Set("key1", []byte("value1"), 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	_, found := s.Get("key1")
	if found {
		t.Error("expected key to be expired")
	}
}

func TestStore_NoTTL_NeverExpires(t *testing.T) {
	s := New(nil)
	defer s.Close()

	s.Set("key1", []byte("value1"), 0)
	time.Sleep(50 * time.Millisecond)
	_, found := s.Get("key1")
	if !found {
		t.Error("expected key with no TTL to still exist")
	}
}

func TestStore_Overwrite(t *testing.T) {
	s := New(nil)
	defer s.Close()

	s.Set("key1", []byte("v1"), 0)
	s.Set("key1", []byte("v2"), 0)
	val, _ := s.Get("key1")
	if string(val) != "v2" {
		t.Errorf("expected 'v2', got %q", val)
	}
}
