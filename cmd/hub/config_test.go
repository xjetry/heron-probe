package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTTL(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", 30 * time.Second, false},
		{"45s", 45 * time.Second, false},
		{"10s", 10 * time.Second, false},
		{"9s", 0, true},
		{"banana", 0, true},
	}
	for _, c := range cases {
		got, err := parseTTL(c.in)
		if (err != nil) != c.err || got != c.want {
			t.Fatalf("parseTTL(%q) = %v, %v; want %v, err=%v", c.in, got, err, c.want, c.err)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	if !isLoopback("127.0.0.1:8080") || !isLoopback("[::1]:8080") || !isLoopback("localhost:8080") {
		t.Fatal("loopback addresses misclassified")
	}
	if isLoopback("0.0.0.0:8080") || isLoopback(":8080") || isLoopback("10.0.0.1:8080") {
		t.Fatal("non-loopback addresses misclassified")
	}
}

func TestServeRejectsInvalidRetention(t *testing.T) {
	t.Setenv("PROBE_OFFLINE_AFTER", "30s")
	for _, tc := range []struct{ flag, value, want string }{
		{"--retention-1m", "30m", "minimum is 1h"},
		{"--retention-1m", "40d", `invalid value "40d"`},
		{"--retention-5m", "23h", "minimum is 24h"},
		{"--retention-1h", "167h", "minimum is 168h"},
		{"--retention-1m", "800h", "must not shrink"},
		{"--retention-5m", "9000h", "must not shrink"},
	} {
		t.Run(tc.flag+"/"+tc.value, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "t.db")
			err := runServe([]string{"--db", db, "--listen", "127.0.0.1:65536", tc.flag, tc.value})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("retention validation: err=%v want=%q", err, tc.want)
			}
			if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("invalid retention touched database: %v", err)
			}
		})
	}
}
