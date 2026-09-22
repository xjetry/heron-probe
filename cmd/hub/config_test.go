package main

import (
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
