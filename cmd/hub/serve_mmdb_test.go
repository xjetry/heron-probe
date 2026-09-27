package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
)

func TestServeRejectsBadMMDBBeforeOpeningDatabase(t *testing.T) {
	for _, kind := range []string{"missing", "text"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "country.mmdb")
			if kind == "text" {
				if err := os.WriteFile(path, []byte("not an mmdb database"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			db := filepath.Join(t.TempDir(), "hub.db")
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := runServeWith(ctx, []string{"--db", db, "--listen", "127.0.0.1:0", "--geo-mmdb", path},
				clock.NewFake(time.Now()), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "--geo-mmdb") {
				t.Errorf("bad mmdb startup error = %v, want --geo-mmdb, path %q and reason", err, path)
			}
			if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("bad mmdb touched database: %v", err)
			}
		})
	}
}

func TestServeMMDBTakesPriorityAndEchoesBackend(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		io.WriteString(w, "DE")
	}))
	defer srv.Close()
	clk := clock.NewFake(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC))
	path := "../../internal/hub/geo/testdata/country.mmdb"
	client, _, stop := startAlertHub(t, clk, func(st *store.Store) {
		if _, err := st.SaveSettings(t.Context(), store.SiteSettings{Theme: store.DefaultTheme}, store.GeoUpdate{Enabled: proto.Bool(true), URL: proto.String(srv.URL + "/{ip}")}); err != nil {
			t.Fatal(err)
		}
		id, _, err := st.CreateNode(t.Context(), "public", []byte("node"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.WriteMinuteBatch(t.Context(), metric.Batch{Rows: []metric.Row{{NodeID: id, TS: clk.Now().Unix(), Bucket: metric.NewBucket(), LastSeen: clk.Now(), Source: "8.8.8.8"}}}); err != nil {
			t.Fatal(err)
		}
	}, "--geo-mmdb", path)
	settings, err := client.GetSettings(t.Context(), connect.NewRequest(&probev1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if s := settings.Msg.Settings; s.GeoBackend != probev1.GeoBackend_GEO_BACKEND_MMDB || s.GeoMmdbPath != path {
		t.Errorf("serve backend = %v path = %q, want MMDB %q", s.GeoBackend, s.GeoMmdbPath, path)
	}
	var country string
	testwait.Until(t, 10*time.Millisecond, func() bool {
		nodes, err := client.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		country = nodes.Msg.Nodes[0].Country
		return country != ""
	}, "serve did not resolve the public address")
	stop()
	if country != "US" {
		t.Errorf("serve country = %q, want local US, not HTTP DE", country)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("HTTP requests with mmdb configured = %d, want 0", n)
	}
}
