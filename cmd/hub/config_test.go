package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
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

func TestServeUsageShowsRetentionMinima(t *testing.T) {
	output, _ := hubCommand(t, "serve", "--help").CombinedOutput()
	for _, tc := range []struct {
		flag    string
		minimum time.Duration
		want    string
	}{
		{"retention-1m", store.MinRetentionM1, "6h0m0s"},
		{"retention-5m", store.MinRetentionM5, "168h0m0s"},
		{"retention-1h", store.MinRetentionH1, "168h0m0s"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			_, section, _ := strings.Cut(string(output), "-"+tc.flag+" duration\n")
			section, _, _ = strings.Cut(section, "\n  -")
			if !strings.Contains(section, "minimum "+tc.minimum.String()+")") {
				t.Errorf("usage differs from Validate minimum %v: %q", tc.minimum, section)
			}
			// 独立的对外取值防止常量和帮助一起漂移后自证正确。
			if !strings.Contains(section, "minimum "+tc.want+")") {
				t.Errorf("usage minimum changed: got %q, want minimum %s", section, tc.want)
			}
		})
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
		{"--retention-1m", "5h59m59s", "minimum is 6h"},
		{"--retention-1m", "40d", `invalid value "40d"`},
		{"--retention-5m", "167h59m59s", "minimum is 168h"},
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
