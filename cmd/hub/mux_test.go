package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/api"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/store"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

var anonymousProcedures = map[string]bool{"/probe.v1.AgentService/Register": true, "/probe.v1.AdminService/Login": true}

func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := auth.New(st, clk, slog.Default())
	l := live.New(clk, 30*time.Second)
	svc, err := ingest.New(ingest.Config{TTL: 30 * time.Second}, l, st, a, clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin := api.New(api.Config{ReportInterval: 10 * time.Second}, st, a, l, svc, clk, slog.Default())
	return newMux(mountOf(svc.Handler()), mountOf(admin.Handler()))
}

// 注册表提供方法全集，真实挂载点必须让所有未列入匿名清单的方法经过鉴权。
func TestMuxRejectsAnonymousProcedures(t *testing.T) {
	srv := httptest.NewServer(newTestMux(t))
	t.Cleanup(srv.Close)
	seen := map[string]bool{}
	count := 0
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != "probe.v1" {
			return true
		}
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			for j := 0; j < svc.Methods().Len(); j++ {
				path := "/" + string(svc.FullName()) + "/" + string(svc.Methods().Get(j).Name())
				count++
				seen[path] = true
				t.Run(path, func(t *testing.T) {
					resp, err := srv.Client().Post(srv.URL+path, "application/json", strings.NewReader("{}"))
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					if anonymousProcedures[path] {
						return
					}
					if resp.StatusCode != http.StatusUnauthorized {
						t.Fatalf("%s: status %d, want 401", path, resp.StatusCode)
					}
					var body struct {
						Code string `json:"code"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.Code != "unauthenticated" {
						t.Fatalf("%s: code %q, want unauthenticated", path, body.Code)
					}
				})
			}
		}
		return true
	})
	if count == 0 {
		t.Fatal("enumerated no procedures")
	}
	for path := range anonymousProcedures {
		if !seen[path] {
			t.Errorf("anonymous allowlist path not enumerated: %s", path)
		}
	}
}
