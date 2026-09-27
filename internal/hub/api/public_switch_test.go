package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func TestPublicSwitchSettings(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	if !currentSettings(t, h).GetPublicEnabled() {
		t.Fatal("never saved: public_enabled must be true")
	}
	for _, enabled := range []bool{false, true, false} {
		in := validSettings()
		in.PublicEnabled = enabled
		if got := saveSettings(t, h, in).GetPublicEnabled(); got != enabled {
			t.Fatalf("saved public_enabled = %v, want %v", got, enabled)
		}
		if got := currentSettings(t, h).GetPublicEnabled(); got != enabled {
			t.Fatalf("read public_enabled = %v, want %v", got, enabled)
		}
	}
}

func TestPublicSwitchAllMethodsAndNodePreservation(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "public")
	h.setPublic(t, id, "public", true)
	private, _ := h.createNode(t, "private")
	methods := probev1.File_probe_v1_public_proto.Services().ByName("PublicService").Methods()
	if methods.Len() == 0 {
		t.Fatal("PublicService has no methods")
	}
	for _, enabled := range []bool{false, true} {
		in := validSettings()
		in.PublicEnabled = enabled
		saveSettings(t, h, in)
		for i := 0; i < methods.Len(); i++ {
			m := methods.Get(i)
			// 所有现有请求共享这些查询字段；空消息忽略未知字段，新增方法也自动经过总闸断言。
			body := fmt.Sprintf(`{"nodeId":"%d","from":"%d","to":"%d","maxPoints":100}`, id, h.clk.Now().Unix()-3600, h.clk.Now().Unix())
			got := pubPost(t, h, string(m.Name()), body, nil)
			want := http.StatusNotFound
			if enabled {
				want = http.StatusOK
			}
			if got.status != want || (!enabled && !strings.Contains(string(got.body), `"code":"not_found"`)) {
				t.Errorf("%s enabled=%v: status=%d body=%s, want %d", m.Name(), enabled, got.status, got.body, want)
			}
		}
		for node, want := range map[int64]bool{id: true, private: false} {
			got, err := h.store.NodeIsPublic(t.Context(), node)
			if err != nil || got != want {
				t.Fatalf("node %d public changed: %v %v, want %v", node, got, err, want)
			}
		}
	}
	snap, err := h.publicClient().GetSnapshot(t.Context(), connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
	if err != nil || len(snap.Msg.Nodes) != 1 || snap.Msg.Nodes[0].Id != id {
		t.Fatalf("reopened snapshot lost public node: %v %v", snap, err)
	}
}

func TestPublicSwitchSnapshotCacheWindow(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	first := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	if first.status != 200 {
		t.Fatalf("prime snapshot: %d %s", first.status, first.body)
	}
	h.clk.Advance(500 * time.Millisecond)
	saveSettings(t, h, &probev1.Settings{Theme: "auto", PublicEnabled: false})
	cached := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	if cached.status != 200 || !bytes.Equal(cached.body, first.body) {
		t.Fatalf("snapshot inside 1s cache window: %d %s", cached.status, cached.body)
	}
	h.clk.Advance(time.Second)
	expired := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	if expired.status != 404 || expired.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("expired snapshot: %d %s %v", expired.status, expired.body, expired.header)
	}
}

func TestPublicSwitchStillRateLimits(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	saveSettings(t, h, &probev1.Settings{Theme: "auto"})
	for i := 0; i <= publicBurst; i++ {
		got := pubGet(t, h, "GetSite", jsonQuery("{}"), nil)
		want := 404
		if i == publicBurst {
			want = 429
		}
		if got.status != want || got.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("closed request %d: %d %s %v, want %d", i, got.status, got.body, got.header, want)
		}
		if i == publicBurst && !strings.Contains(string(got.body), "resource_exhausted") {
			t.Fatalf("limit response: %s", got.body)
		}
	}
}

func TestPublicSwitchDoesNotReadDatabase(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	saveSettings(t, h, &probev1.Settings{Theme: "auto"})
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	got := pubGet(t, h, "GetSite", jsonQuery("{}"), nil)
	if got.status != 404 {
		t.Fatalf("gate needs database: %d %s", got.status, got.body)
	}
}
