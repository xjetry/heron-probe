package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xjetry/heron-probe/internal/clock"
)

type statFunc func(string) (fs.FileInfo, error)

func (f statFunc) Stat(path string) (fs.FileInfo, error) { return f(path) }

func TestStorageStatsWALObservation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int64
		err    error
		absent bool
	}{
		{name: "present", size: 12345},
		{name: "empty"},
		{name: "absent", err: fs.ErrNotExist, absent: true},
		{name: "permission", err: fs.ErrPermission},
		{name: "io", err: errors.New("input/output error")},
		{name: "long", err: errors.New(strings.Repeat("错", 300) + "\xff")},
		{name: "invalid_utf8", err: errors.New("bad \xff path")},
		{name: "empty_error", err: errors.New("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := openAt(t)
			fixture := filepath.Join(t.TempDir(), "fixture-wal")
			if tc.err == nil {
				if err := os.WriteFile(fixture, make([]byte, tc.size), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			clk := s.clk.(*clock.Fake)
			at := clk.Now().Add(time.Second)
			s.files = statFunc(func(got string) (fs.FileInfo, error) {
				calls++
				if got != path+"-wal" {
					t.Fatalf("stat path = %q, want %q", got, path+"-wal")
				}
				clk.Advance(time.Second)
				if tc.name == "empty_error" {
					return nil, tc.err
				}
				if tc.err != nil {
					return nil, fmt.Errorf("observe: %w", &fs.PathError{Op: "stat", Path: fixture, Err: tc.err})
				}
				return os.Stat(fixture)
			})
			stats, err := s.StorageStats(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got := stats.WAL
			if calls != 1 || got.ObservedAt != at.Unix() {
				t.Fatalf("stat calls = %d, observed_at = %d, want 1 / %d", calls, got.ObservedAt, at.Unix())
			}
			if got.Absent != tc.absent {
				t.Fatalf("absent = %v, want %v; WAL = %+v", got.Absent, tc.absent, got)
			}
			if tc.err == nil {
				if got.Bytes == nil || *got.Bytes != tc.size || got.Error != "" {
					t.Fatalf("present WAL = %+v, want %d bytes", got, tc.size)
				}
			} else if got.Bytes != nil {
				t.Fatalf("failed stat has bytes = %d, want unknown", *got.Bytes)
			}
			if tc.absent {
				if got.Error != "" {
					t.Fatalf("ENOENT became read error: %q", got.Error)
				}
			} else if tc.err != nil {
				if got.Error == "" || len(got.Error) > 512 || !utf8.ValidString(got.Error) {
					t.Fatalf("invalid bounded error (%d bytes): %q", len(got.Error), got.Error)
				}
			}
		})
	}
}

func TestWALLifecycleObservation(t *testing.T) {
	s, path := openAt(t)
	if _, _, err := s.CreateNode(t.Context(), "wal", Billing{}, hash(99)); err != nil {
		t.Fatal(err)
	}
	stats, err := s.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if stats.WAL.Bytes == nil || *stats.WAL.Bytes != info.Size() || info.Size() <= 0 {
		t.Fatalf("write observation = %+v, stat bytes = %d", stats.WAL, info.Size())
	}
	t.Logf("after write: WAL exists, bytes=%d", info.Size())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path + "-wal")
	switch {
	case err == nil:
		t.Logf("after normal Close: WAL exists, bytes=%d", info.Size())
	case errors.Is(err, fs.ErrNotExist):
		t.Log("after normal Close: WAL absent")
	default:
		t.Fatalf("after normal Close: stat failed: %v", err)
	}
}
