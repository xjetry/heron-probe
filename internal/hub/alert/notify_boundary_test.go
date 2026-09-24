package alert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xjetry/probe/internal/hub/store"
)

func TestOutboundErrorsDoNotExposeCredentials(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cause         error
		token, reason string
	}{
		{"refused", syscall.ECONNREFUSED, "123456:SECRET-TOKEN", "connection refused"},
		{"timeout", context.DeadlineExceeded, "123456:SECRET-TOKEN", "deadline exceeded"},
		{"malformed", syscall.ECONNREFUSED, "SECRET%zz", "invalid URL escape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			const base = "http://127.0.0.1/private-key"
			client := NewHTTPClient()
			client.Transport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, tc.cause }}
			defer client.CloseIdleConnections()
			config, err := json.Marshal(TelegramConfig{BotToken: tc.token, ChatID: "chat"})
			must(t, err)
			c, err := f.e.SaveChannel(t.Context(), store.NotifyChannel{Name: "telegram", Kind: store.ChannelTelegram, Config: string(config)})
			must(t, err)
			q := NewQueue(f.st, f.e.Channels, client, base, f.clk, func(context.Context, time.Duration) error { return nil }, f.log)
			ev := queueEvent(t, f, c)
			q.Enqueue(ev)
			stop := startQueue(t, q)
			ds := awaitDeliveries(t, f, ev.ID, allDone)
			stop()
			testErr := q.SendTest(t.Context(), c)
			directErr := NewTelegram(TelegramConfig{BotToken: tc.token, ChatID: "chat"}, client, base).Send(t.Context(), messageForTest())
			if testErr == nil || directErr == nil {
				t.Fatal("unreachable target succeeded")
			}
			for source, text := range map[string]string{"last_error": ds[0].LastError, "SendTest": testErr.Error(), "Send": directErr.Error()} {
				if strings.Contains(text, tc.token) || strings.Contains(text, base) || strings.Contains(text, "/sendMessage") {
					t.Errorf("%s credential or URL leaked: %q", source, text)
				}
				if !strings.Contains(text, tc.reason) {
					t.Errorf("%s cause lost: %q", source, text)
				}
			}
		})
	}
}

func TestResponseSummaryIsValidUTF8AndRuneBounded(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"multibyte", "x" + strings.Repeat("网", 230), "x" + strings.Repeat("网", 199)},
		{"invalid", "x\xffy", "x�y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); _, _ = io.WriteString(w, tc.body) }))
			defer srv.Close()
			c := queueChannel(t, f, srv.URL)
			ev := queueEvent(t, f, c)
			q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
			q.Enqueue(ev)
			stop := startQueue(t, q)
			ds := awaitDeliveries(t, f, ev.ID, allDone)
			stop()
			if !utf8.ValidString(ds[0].LastError) {
				t.Fatalf("last_error has invalid UTF-8: %q", ds[0].LastError)
			}
			ch, err := ParseChannel(c, NewHTTPClient(), "")
			must(t, err)
			err = ch.Send(t.Context(), messageForTest())
			var response httpError
			if !errors.As(err, &response) {
				t.Fatalf("HTTP error=%v", err)
			}
			if response.body != tc.want || utf8.RuneCountInString(response.body) > 200 {
				t.Fatalf("summary=%q runes=%d", response.body, utf8.RuneCountInString(response.body))
			}
		})
	}
}

func TestQueueAccepts2xxWithInterruptedBodyOnce(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "short")
	}))
	defer srv.Close()
	ev := queueEvent(t, f, queueChannel(t, f, srv.URL))
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, func(context.Context, time.Duration) error { return nil }, f.log)
	q.Enqueue(ev)
	stop := startQueue(t, q)
	ds := awaitDeliveries(t, f, ev.ID, allDone)
	stop()
	if calls.Load() != 1 || ds[0].Attempts != 1 || !ds[0].OK || ds[0].LastError != "" {
		t.Fatalf("calls=%d delivery=%+v", calls.Load(), ds[0])
	}
}

func TestQueueDoesNotSendQueuedTerminalDelivery(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := queueChannel(t, f, srv.URL)
	first, last := queueEvent(t, f, c), queueEvent(t, f, c)
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
	q.Enqueue(first)
	if _, err := f.st.BeginDeliveryAttempt(t.Context(), first.Deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.UpdateDelivery(t.Context(), first.Deliveries[0].ID, false, true, "permanent failure", time.Time{}))
	q.Enqueue(last)
	stop := startQueue(t, q)
	awaitDeliveries(t, f, last.ID, allDone)
	stop()
	if calls.Load() != 1 {
		t.Fatalf("queued terminal delivery sent: total requests=%d want 1", calls.Load())
	}
}

func TestEnqueueCompletesWithBusyWorkerAndConcurrentProducers(t *testing.T) {
	f := newFixture(t)
	entered := make(chan struct{})
	client := NewHTTPClient()
	client.Timeout = time.Minute
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	c := queueChannel(t, f, "http://127.0.0.1/busy")
	ev := queueEvent(t, f, c)
	q := NewQueue(f.st, f.e.Channels, client, "", f.clk, nil, f.log)
	q.items = make(chan deliveryItem, 2)
	q.Enqueue(ev)
	stopWorker := startQueue(t, q)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not become busy")
	}
	for range cap(q.items) {
		q.Enqueue(ev)
	}
	start, abort, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			<-start
			for range 5000 {
				select {
				case <-abort:
					return
				default:
				}
				q.Enqueue(ev)
			}
		})
	}
	go func() { wg.Wait(); close(done) }()
	// 失败时放走阻塞的生产者；不能让故意破坏互斥的验证把协程遗留给后续测试。
	defer func() {
		close(abort)
		stopWorker()
		for {
			select {
			case <-done:
				return
			case <-q.items:
			}
		}
	}()
	close(start)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue blocked with busy worker and 8 producers")
	}
}
