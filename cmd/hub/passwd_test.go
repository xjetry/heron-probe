package main

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xjetry/probe/internal/hub/auth"
)

func pipeWith(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { io.WriteString(w, content); w.Close() }()
	t.Cleanup(func() { r.Close() })
	return r
}

func TestPasswdReadsOneLineFromNonTerminalStdin(t *testing.T) {
	db := filepath.Join(t.TempDir(), "t.db")
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, "a sufficiently long password\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	st, a, err := openOffline(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	phc, found, err := st.AdminPasswordHash(context.Background())
	if err != nil || !found || !strings.HasPrefix(phc, "$argon2id$") {
		t.Fatalf("stored password is not argon2id: found=%v err=%v", found, err)
	}
	if _, err := a.Login(context.Background(), "a sufficiently long password", netip.MustParseAddr("127.0.0.1")); err != nil {
		t.Fatalf("login with the password just set: %v", err)
	}
}

func TestPasswdRejectsShortPassword(t *testing.T) {
	db := filepath.Join(t.TempDir(), "t.db")
	err := runPasswdWith([]string{"--db", db}, pipeWith(t, "short\n"), io.Discard)
	if !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("err = %v, want ErrWeakPassword", err)
	}
}

func TestReadPasswordLineBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"one-line", "first line\nignored line\n", "first line"},
		{"CRLF-and-spaces", "  前后保留空白的密码  \r\n", "  前后保留空白的密码  "},
		{"EOF", "no final newline", "no final newline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readPassword(pipeWith(t, tc.input), io.Discard)
			if err != nil || got != tc.want {
				t.Fatalf("password line=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}
}
