package s3

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAWSVectors(t *testing.T) {
	for _, name := range []string{"get-vanilla", "post-x-www-form-urlencoded", "get-header-key-duplicate", "get-vanilla-query-order-key-case"} {
		t.Run(name, func(t *testing.T) {
			read := func(ext string) string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join("testdata", name, name+"."+ext))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			head, body, _ := strings.Cut(read("req"), "\n\n")
			lines := strings.Split(head, "\n")
			parts := strings.Fields(lines[0])
			req, err := http.NewRequest(parts[0], "https://example.amazonaws.com"+parts[1], bytes.NewBufferString(body))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range lines[1:] {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					t.Fatalf("invalid fixture header: %q", line)
				}
				if strings.EqualFold(k, "host") {
					req.Host = v
				} else {
					req.Header.Add(k, v)
				}
			}
			canonical, signed := canonicalRequest(req, hashHex([]byte(body)))
			if want := read("creq"); canonical != want {
				t.Errorf("canonical request mismatch\ngot: %q\nwant: %q", canonical, want)
			}
			sts, authorization := authorization(req, canonical, signed, "us-east-1", "service", "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY")
			if want := read("sts"); sts != want {
				t.Errorf("string to sign mismatch\ngot: %q\nwant: %q", sts, want)
			}
			if want := read("authz"); authorization != want {
				t.Errorf("signature mismatch\ngot: %q\nwant: %q", authorization, want)
			}
		})
	}
}
