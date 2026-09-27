package alert

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // 夏令时用例要真实时区库；嵌入的库让结论不随测试机的系统时区库变化。

	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
)

func expiryRule() store.AlertRule {
	return store.AlertRule{Name: "到期", Kind: store.KindExpiry, Enabled: true, AllNodes: true, DaysBefore: 7}
}

func date(s string) time.Time {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// "合法日期"的口径：四位年、两位月日、日子真实存在，前后不带任何字符。
func TestParseDateAcceptsOnlyRealYYYYMMDD(t *testing.T) {
	for _, ok := range []string{"2026-02-28", "2028-02-29", "0000-01-01", "9999-12-31"} {
		if _, err := ParseDate(ok); err != nil {
			t.Errorf("ParseDate(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "2026-02-29", "2026-02-30", "2026-13-01", "2026-1-05", "2026-01-5", "20260105", "2026/01/05",
		" 2026-01-05", "2026-01-05 ", "2026-01-05T00:00:00Z", "10000-01-01", "２０２６-01-05"} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("ParseDate(%q) accepted", bad)
		}
	}
}

// 同一时刻在不同时区是不同的日历日：UTC 16:30 在东八区已是次日。
func TestTodayTakesTheCalendarDayInTheZone(t *testing.T) {
	now := time.Date(2026, 9, 24, 16, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		loc  *time.Location
		want string
	}{
		{time.UTC, "2026-09-24"},
		{time.FixedZone("UTC+8", 8*3600), "2026-09-25"},
		{zone(t, "America/Los_Angeles"), "2026-09-24"},
	} {
		if got := Today(now, c.loc); !got.Equal(date(c.want)) {
			t.Errorf("Today in %s = %s, want %s", c.loc, got.Format(time.DateOnly), c.want)
		}
	}
}

// 剩余天数是日历日之差：跨夏令时不差一天，跨几千年也不因 time.Duration 饱和而算错。
func TestDaysLeftCountsCalendarDays(t *testing.T) {
	today := date("2026-09-27")
	for _, c := range []struct {
		expiresOn string
		want      int
	}{
		{"2026-10-01", 4},
		{"2026-09-27", 0},
		{"2026-09-20", -7},
		{"2027-03-28", 182}, // 跨过北半球夏令时的开始与结束
		{"9999-12-31", 2912173},
		{"0000-01-01", -740251},
	} {
		got, ok := DaysLeft(c.expiresOn, today)
		if !ok || got != c.want {
			t.Errorf("DaysLeft(%s) = %d %v, want %d", c.expiresOn, got, ok, c.want)
		}
	}
	for _, none := range []string{"", "2026-02-30"} {
		if got, ok := DaysLeft(none, today); ok {
			t.Errorf("DaysLeft(%q) = %d, want none", none, got)
		}
	}
}

func TestCycleMonthsCoversEveryStoredCycle(t *testing.T) {
	want := map[store.BillingCycle]int{store.CycleMonthly: 1, store.CycleQuarterly: 3, store.CycleSemiannual: 6,
		store.CycleYearly: 12, store.CycleBiennial: 24, store.CycleTriennial: 36}
	cycles := store.BillingCycles()
	if len(cycles) != len(want) {
		t.Fatalf("store.BillingCycles() = %v, want the %d cycles of §9.4", cycles, len(want))
	}
	for _, c := range cycles {
		if got, ok := cycleMonths(c); !ok || got != want[c] {
			t.Errorf("cycleMonths(%s) = %d %v, want %d", c, got, ok, want[c])
		}
	}
	for _, c := range []store.BillingCycle{store.CycleNone, "weekly"} {
		if got, ok := cycleMonths(c); ok {
			t.Errorf("cycleMonths(%q) = %d, want no renewal", c, got)
		}
	}
}

// 推后到不早于今天为止；每一步从上一步的结果推后，钳到月末之后不回弹。
func TestRenewedExpiry(t *testing.T) {
	monthly := store.Billing{Cycle: store.CycleMonthly, AutoRenew: true}
	with := func(b store.Billing, expiresOn string) store.Billing { b.ExpiresOn = expiresOn; return b }
	for _, c := range []struct {
		name  string
		b     store.Billing
		today string
		want  string
	}{
		{"one month", with(monthly, "2026-09-10"), "2026-09-24", "2026-10-10"},
		{"several months, clamped then drifting", with(monthly, "2026-01-31"), "2026-04-01", "2026-04-28"},
		{"leap February", with(monthly, "2028-01-31"), "2028-02-15", "2028-02-29"},
		{"yearly from February 29", store.Billing{Cycle: store.CycleYearly, AutoRenew: true, ExpiresOn: "2024-02-29"}, "2025-03-01", "2026-02-28"},
		{"quarterly lands on today", store.Billing{Cycle: store.CycleQuarterly, AutoRenew: true, ExpiresOn: "2026-06-24"}, "2026-09-24", "2026-09-24"},
		{"triennial", store.Billing{Cycle: store.CycleTriennial, AutoRenew: true, ExpiresOn: "2020-01-15"}, "2026-09-24", "2029-01-15"},
		{"from year 0", with(monthly, "0000-01-31"), "0000-03-01", "0000-03-29"},
	} {
		got, ok := renewedExpiry(c.b, date(c.today))
		if !ok || got != c.want {
			t.Errorf("%s: renewedExpiry = %q %v, want %q", c.name, got, ok, c.want)
		}
	}
	for _, c := range []struct {
		name string
		b    store.Billing
	}{
		{"auto renew off", store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-10"}},
		{"no cycle", store.Billing{AutoRenew: true, ExpiresOn: "2026-09-10"}},
		{"unknown cycle", store.Billing{Cycle: "weekly", AutoRenew: true, ExpiresOn: "2026-09-10"}},
		{"no date", with(monthly, "")},
		{"unreadable date", with(monthly, "2026-09-31")},
		{"due today", with(monthly, "2026-09-24")},
		{"in the future", with(monthly, "2026-10-01")},
	} {
		if got, ok := renewedExpiry(c.b, date("2026-09-24")); ok {
			t.Errorf("%s: renewed to %s", c.name, got)
		}
	}
}

// 下一个日界是本地日期变化的第一个时刻：严格晚于 now，本地日期是明天，再早 1 纳秒还是今天。
// 夏令时在零点开始的时区里零点不存在，新的一天从 01:00 开始。
func TestNextDayStartIsTheFirstInstantOfTheNextLocalDay(t *testing.T) {
	for _, c := range []struct {
		loc  *time.Location
		now  string // loc 里的本地时刻
		want string // loc 里的本地时刻与偏移
	}{
		{time.UTC, "2026-09-27T00:00:00", "2026-09-28T00:00:00Z"},
		{time.FixedZone("UTC+8", 8*3600), "2026-09-24T20:00:00", "2026-09-25T00:00:00+08:00"},
		{zone(t, "America/New_York"), "2026-03-07T22:00:00", "2026-03-08T00:00:00-05:00"},
		{zone(t, "America/Santiago"), "2026-09-05T22:00:00", "2026-09-06T01:00:00-03:00"},
		{zone(t, "America/Santiago"), "2026-04-04T22:00:00", "2026-04-05T00:00:00-04:00"},
		{zone(t, "America/Havana"), "2026-03-07T22:00:00", "2026-03-08T01:00:00-04:00"},
		{zone(t, "America/Havana"), "2026-10-31T22:00:00", "2026-11-01T00:00:00-04:00"},
	} {
		now, err := time.ParseInLocation("2006-01-02T15:04:05", c.now, c.loc)
		if err != nil {
			t.Fatal(err)
		}
		got := nextDayStart(now, c.loc)
		if got.Format(time.RFC3339) != c.want {
			t.Errorf("%s from %s: next day starts %s, want %s", c.loc, c.now, got.Format(time.RFC3339), c.want)
		}
		today, tomorrow := Today(now, c.loc), Today(now, c.loc).AddDate(0, 0, 1)
		if !got.After(now) || !Today(got, c.loc).Equal(tomorrow) || !Today(got.Add(-time.Nanosecond), c.loc).Equal(today) {
			t.Errorf("%s from %s: %s is not the first instant of the next local day", c.loc, c.now, got.Format(time.RFC3339Nano))
		}
	}
}

func TestNextExpiry(t *testing.T) {
	firing, recoveredTr := store.TransitionFiring, store.TransitionRecovered
	for _, c := range []struct {
		name   string
		cur    store.AlertState
		o      ExpiryObservation
		want   store.AlertState
		wantTr *store.Transition
	}{
		{"no date, never fired", store.StateOK, ExpiryObservation{DaysBefore: 7}, store.StateOK, nil},
		{"date cleared while firing", store.StateFiring, ExpiryObservation{DaysBefore: 7}, store.StateOK, &recoveredTr},
		{"outside the window", store.StateOK, ExpiryObservation{HasExpiry: true, DaysLeft: 8, DaysBefore: 7}, store.StateOK, nil},
		{"enters the window on its edge", store.StateOK, ExpiryObservation{HasExpiry: true, DaysLeft: 7, DaysBefore: 7}, store.StateFiring, &firing},
		{"already expired", store.StateOK, ExpiryObservation{HasExpiry: true, DaysLeft: -3, DaysBefore: 7}, store.StateFiring, &firing},
		{"stays firing", store.StateFiring, ExpiryObservation{HasExpiry: true, DaysLeft: -1, DaysBefore: 7}, store.StateFiring, nil},
		{"renewed out of the window", store.StateFiring, ExpiryObservation{HasExpiry: true, DaysLeft: 8, DaysBefore: 7}, store.StateOK, &recoveredTr},
		{"no pending stage", store.StatePending, ExpiryObservation{HasExpiry: true, DaysLeft: 1, DaysBefore: 7}, store.StateFiring, &firing},
	} {
		got, tr := NextExpiry(c.cur, c.o)
		if got != c.want || (tr == nil) != (c.wantTr == nil) || (tr != nil && *tr != *c.wantTr) {
			t.Errorf("%s: NextExpiry = %s %v, want %s %v", c.name, got, tr, c.want, c.wantTr)
		}
	}
}

func (f *fixture) billing(t *testing.T, id int64, b store.Billing) {
	t.Helper()
	_, err := f.st.UpdateNode(t.Context(), id, store.NodeEdit{Name: fmt.Sprintf("node%d", id), TrafficResetDay: 1, Billing: b})
	must(t, err)
}

func (f *fixture) expiresOn(t *testing.T, id int64) string {
	t.Helper()
	n, err := f.st.GetNode(t.Context(), id)
	must(t, err)
	return n.Billing.ExpiresOn
}

func (f *fixture) sweepExpiry(t *testing.T) { t.Helper(); must(t, f.e.SweepExpiry(t.Context())) }

// 夹具的今天是东八区的 2026-09-24。触发与恢复的文案按 §9.2 原文，没有到期日的节点不产生状态。
func TestSweepExpiryFiresAndRecoversWithSpecSummaries(t *testing.T) {
	f := newFixture(t)
	node1, node2 := f.ids[0], f.ids[1]
	r := f.rule(t, expiryRule())
	for _, step := range []struct {
		expiresOn string
		state     store.AlertState
		summary   string
		value     float64
	}{
		{"2026-09-28", store.StateFiring, "节点 node1 将于 2026-09-28 到期（剩 4 天，规则 到期）", 4},
		{"2026-11-01", store.StateOK, "节点 node1 到期日已更新为 2026-11-01（规则 到期）", 38},
		{"2026-09-17", store.StateFiring, "节点 node1 已于 2026-09-17 到期（已过期 7 天，规则 到期）", -7},
		{"", store.StateOK, "节点 node1 已清除到期日（规则 到期）", 0},
	} {
		f.billing(t, node1, store.Billing{ExpiresOn: step.expiresOn})
		before := len(f.events(t))
		f.sweepExpiry(t)
		f.sweepExpiry(t)
		events := f.events(t)
		if len(events) != before+1 {
			t.Fatalf("expires_on %q: %d new events, want exactly 1 across two sweeps", step.expiresOn, len(events)-before)
		}
		if ev := events[0]; ev.NodeID != node1 || ev.Summary != step.summary || ev.Value != step.value {
			t.Fatalf("expires_on %q: event %+v, want summary %q value %v", step.expiresOn, ev, step.summary, step.value)
		}
		wantState(t, f.e, r.ID, node1, step.state)
		wantState(t, f.e, r.ID, node2, "")
	}
}

// 今天取 hub 时区的日历日：UTC 15:59 与 16:00 之间东八区跨过零点，剩余天数从 8 变成 7，规则在这一刻触发。
func TestSweepExpiryCountsDaysInTheHubZone(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-10-02"})
	f.clk.SetWall(time.Date(2026, 9, 24, 15, 59, 0, 0, time.UTC))
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], "")
	f.clk.SetWall(time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC))
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
}

// 同一次扫描里先推后、再评估：已触发的告警随续期直接恢复，推后的日期落库并记一行日志。
func TestSweepExpiryRenewsBeforeEvaluating(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.restart(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-20"})
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-20", AutoRenew: true})
	f.sweepExpiry(t)
	if got := f.expiresOn(t, f.ids[0]); got != "2026-10-20" {
		t.Fatalf("expires_on after renewal = %s, want 2026-10-20", got)
	}
	events := f.events(t)
	if len(events) != 2 || events[0].Transition != store.TransitionRecovered || events[0].Summary != "节点 node1 到期日已更新为 2026-10-20（规则 到期）" {
		t.Fatalf("events after renewal: %+v", events)
	}
	wantState(t, f.e, r.ID, f.ids[0], store.StateOK)
	if !strings.Contains(logs.String(), `msg="node expiry renewed" node_id=1 node=node1 cycle=monthly from=2026-09-20 to=2026-10-20`) {
		t.Fatalf("renewal log line missing:\n%s", logs.String())
	}
}

// 周期为空或读不懂时扫描自己不推后，不依赖保存入口拦下这种组合。
func TestSweepExpiryDoesNotRenewWithoutAUsableCycle(t *testing.T) {
	f := newFixture(t)
	for _, cycle := range []store.BillingCycle{store.CycleNone, "weekly"} {
		f.billing(t, f.ids[0], store.Billing{Cycle: cycle, ExpiresOn: "2026-08-31", AutoRenew: true})
		f.sweepExpiry(t)
		if got := f.expiresOn(t, f.ids[0]); got != "2026-08-31" {
			t.Fatalf("cycle %q renewed to %s", cycle, got)
		}
	}
}

// 库里读不懂的到期日（只有手改会产生）既不触发也不恢复：已触发的状态原样保留，开着自动续期也不推后，每次扫描对
// 每个这样的节点记一行 Warn，不去重：扫描两次就是两行。
func TestSweepExpirySkipsUnreadableDates(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.restart(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, AutoRenew: true, ExpiresOn: "2026-09-31"})
	f.billing(t, f.ids[1], store.Billing{ExpiresOn: "2026/09/25"})
	f.sweepExpiry(t)
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	wantState(t, f.e, r.ID, f.ids[1], "")
	if events := f.events(t); len(events) != 1 {
		t.Fatalf("events = %+v, want only the first firing", events)
	}
	if got := f.expiresOn(t, f.ids[0]); got != "2026-09-31" {
		t.Fatalf("unreadable date renewed to %s", got)
	}
	for _, want := range []string{"node_id=1 expires_on=2026-09-31", "node_id=2 expires_on=2026/09/25"} {
		line := `level=WARN msg="node expires_on is not a YYYY-MM-DD date; expiry rules skip this node" ` + want
		if n := strings.Count(logs.String(), line); n != 2 {
			t.Fatalf("%d lines of %q, want 2:\n%s", n, line, logs.String())
		}
	}
}

// 作用域外的节点不评估；停用的规则不评估。
func TestSweepExpiryHonoursScopeAndEnabled(t *testing.T) {
	f := newFixture(t)
	scoped := expiryRule()
	scoped.AllNodes, scoped.NodeIDs = false, []int64{f.ids[1]}
	scoped = f.rule(t, scoped)
	disabled := expiryRule()
	disabled.Enabled = false
	disabled = f.rule(t, disabled)
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.sweepExpiry(t)
	wantState(t, f.e, scoped.ID, f.ids[0], "")
	wantState(t, f.e, disabled.ID, f.ids[0], "")
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
}

// 另一个进程删掉的节点（probe-hub node delete 删了库里的状态行，没经过 Forget）不再是候选，内存里的状态随扫描清掉。
func TestSweepExpiryDropsStatesOfNodesDeletedElsewhere(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	must(t, f.st.DeleteNode(t.Context(), f.ids[0]))
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], "")
}

// 保存启用的到期规则即评估一次：不等下一个日界。停用的规则保存后没有状态。
func TestSaveRuleEvaluatesEnabledExpiryRules(t *testing.T) {
	f := newFixture(t)
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-27"})
	disabled := expiryRule()
	disabled.Enabled = false
	disabled = f.rule(t, disabled)
	wantState(t, f.e, disabled.ID, f.ids[0], "")
	enabled := f.rule(t, expiryRule())
	wantState(t, f.e, enabled.ID, f.ids[0], store.StateFiring)
	enabled.DaysBefore = 2
	f.rule(t, enabled)
	wantState(t, f.e, enabled.ID, f.ids[0], store.StateOK)
}

// 恢复文案按离开窗口的原因。日期没变、只是提前天数调小：已不在提醒窗口内，第一次恢复前重启过一次（日期从库里读回），
// 第二次没有（日期来自内存）。firing 期间改过日期再调小提前天数：日期与触发时不同，写到期日已更新。每一步保存规则
// 之前先照常扫描一次：留在 firing 的扫描不改记下的日期。
func TestSweepExpiryRecoverySummaryFollowsTheReason(t *testing.T) {
	f := newFixture(t)
	node1 := f.ids[0]
	f.billing(t, node1, store.Billing{ExpiresOn: "2026-09-27"})
	r := f.rule(t, expiryRule())
	f.restart(t)
	for _, step := range []struct {
		expiresOn  string
		daysBefore int
		state      store.AlertState
		summary    string
		value      float64
	}{
		{"2026-09-27", 2, store.StateOK, "节点 node1 已不在提醒窗口内（规则 到期）", 3},
		{"2026-09-27", 7, store.StateFiring, "节点 node1 将于 2026-09-27 到期（剩 3 天，规则 到期）", 3},
		{"2026-09-27", 2, store.StateOK, "节点 node1 已不在提醒窗口内（规则 到期）", 3},
		{"2026-09-27", 7, store.StateFiring, "节点 node1 将于 2026-09-27 到期（剩 3 天，规则 到期）", 3},
		{"2026-09-29", 7, store.StateFiring, "", 0},
		{"2026-09-29", 4, store.StateOK, "节点 node1 到期日已更新为 2026-09-29（规则 到期）", 5},
	} {
		f.billing(t, node1, store.Billing{ExpiresOn: step.expiresOn})
		f.sweepExpiry(t)
		before := len(f.events(t))
		r.DaysBefore = step.daysBefore
		r = f.rule(t, r)
		events := f.events(t)
		switch {
		case step.summary == "" && len(events) != before:
			t.Fatalf("%s with days_before %d: %d new events, want none", step.expiresOn, step.daysBefore, len(events)-before)
		case step.summary != "" && (len(events) != before+1 || events[0].Summary != step.summary || events[0].Value != step.value):
			t.Fatalf("%s with days_before %d: events %+v, want one new event %q value %v", step.expiresOn, step.daysBefore, events, step.summary, step.value)
		}
		wantState(t, f.e, r.ID, node1, step.state)
	}
}

// 一条规则对一个节点只提醒一次：从"将于"走到到期当天、再走到"已于"都不发第二条。到期当天写"剩 0 天"。
func TestSweepExpiryNotifiesOncePerEntry(t *testing.T) {
	f := newFixture(t)
	f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.billing(t, f.ids[1], store.Billing{ExpiresOn: "2026-09-24"})
	for day := range 4 {
		f.clk.SetWall(time.Date(2026, 9, 24+day, 12, 0, 0, 0, time.UTC))
		f.sweepExpiry(t)
	}
	var got []string
	for _, ev := range f.events(t) {
		got = append(got, ev.Summary)
	}
	slices.Sort(got)
	want := []string{"节点 node1 将于 2026-09-25 到期（剩 1 天，规则 到期）", "节点 node2 将于 2026-09-24 到期（剩 0 天，规则 到期）"}
	if !slices.Equal(got, want) {
		t.Fatalf("summaries over four days = %q, want %q", got, want)
	}
}

// 循环一启动就扫描一次：停机跨过的日界由它补上；取消后返回。
func TestRunExpirySweepSweepsOnStartAndStops(t *testing.T) {
	f := newFixture(t)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-01", AutoRenew: true})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); f.e.RunExpirySweep(ctx) }()
	testwait.Until(t, 10*time.Millisecond, func() bool { return f.expiresOn(t, f.ids[0]) == "2026-10-01" },
		"expires_on = %s, want 2026-10-01 after the startup sweep", testwait.When(func() string { return f.expiresOn(t, f.ids[0]) }))
	cancel()
	select {
	case <-done:
	case <-time.After(testwait.Bound):
		t.Fatal("RunExpirySweep did not return after cancel")
	}
}

// 续期写回没有落库，说明快照之后这个节点的计费被改过：本轮不按过期的快照评估它，不触发也不推后；库里的值与快照
// 一致之后照常续期。触发器让续期的 UPDATE 落空，对 RenewExpiry 的效果与"扫描读快照之后、写回之前有人改了计费"
// 相同。
func TestSweepExpirySkipsANodeWhoseSnapshotIsStale(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-20", AutoRenew: true})
	db, err := sql.Open("sqlite", f.path)
	must(t, err)
	defer db.Close()
	_, err = db.ExecContext(t.Context(), "CREATE TRIGGER stale BEFORE UPDATE OF expires_on ON node BEGIN SELECT RAISE(IGNORE); END")
	must(t, err)
	f.sweepExpiry(t)
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("a node with a stale snapshot was evaluated: %+v", events)
	}
	wantState(t, f.e, r.ID, f.ids[0], "")
	_, err = db.ExecContext(t.Context(), "DROP TRIGGER stale")
	must(t, err)
	f.sweepExpiry(t)
	if got := f.expiresOn(t, f.ids[0]); got != "2026-10-20" {
		t.Fatalf("expires_on = %s, want 2026-10-20 once the snapshot matches", got)
	}
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("renewed node produced events: %+v", events)
	}
}

// tickingClock 的墙钟从 base 起随真实时间前进。RunExpirySweep 的定时器按真实时间走，墙钟随它一起跨过日界，用例走的
// 是"定时器等到日界再扫"这条路。
type tickingClock struct{ base, start time.Time }

func (c tickingClock) Now() time.Time      { return c.base.Add(time.Since(c.start)) }
func (c tickingClock) Mono() time.Duration { return time.Since(c.start) }

// 启动扫描之后，循环在 hub 时区的日界到点时再扫描一次：墙钟从东八区 2026-09-24 23:59:59.5 起走，约半秒后进入次日，
// 今天到期、开着自动续期的节点由零点之后的扫描推后。启动扫描读到的若还是 24 日，不推后它（到期日不早于今天）；循环
// 在启动扫描之前读钟，定时器定在 25 日零点，启动扫描再慢也不会把下一次推到 26 日。
func TestRunExpirySweepSweepsAgainAtTheDayBoundary(t *testing.T) {
	f := newFixture(t)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-24", AutoRenew: true})
	clk := tickingClock{base: time.Date(2026, 9, 24, 23, 59, 59, 500_000_000, f.loc), start: time.Now()}
	e := New(Config{TTL: 30 * time.Second, Location: f.loc}, f.st, f.l, clk, f.log)
	must(t, e.Load(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); e.RunExpirySweep(ctx) }()
	testwait.Until(t, 10*time.Millisecond, func() bool { return f.expiresOn(t, f.ids[0]) == "2026-10-24" },
		"expires_on = %s, want 2026-10-24 after the sweep at the day boundary", testwait.When(func() string { return f.expiresOn(t, f.ids[0]) }))
	cancel()
	select {
	case <-done:
	case <-time.After(testwait.Bound):
		t.Fatal("RunExpirySweep did not return after cancel")
	}
}

// switchClock 在 arm 之前一律返回 before；arm 之后第一次读仍返回 before，此后一律返回 after。
type switchClock struct {
	mu                sync.Mutex
	before, after     time.Time
	armed, firstTaken bool
}

func (c *switchClock) arm() { c.mu.Lock(); c.armed = true; c.mu.Unlock() }

func (c *switchClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.armed:
		return c.before
	case !c.firstTaken:
		c.firstTaken = true
		return c.before
	}
	return c.after
}

func (c *switchClock) Mono() time.Duration { return 0 }

// 一轮扫描跨过零点时，新一天的日界扫描不被跳过。钟在启动循环之前 arm：第一次读返回东八区 9 月 24 日零点前，此后一律
// 返回 25 日零点后。循环在扫描之前读钟，下一次触发定在 25 日零点；扫描读到的已是 25 日，今天到期、开着自动续期的节点
// 在时限内推后到 10 月 24 日。循环若在扫描之后才读钟，扫描拿到 24 日（不推后），循环拿到 25 日而定到 26 日零点，25 日
// 的扫描被跳过，节点要约 24 小时后才推后。
func TestRunExpirySweepDoesNotSkipADayBoundaryCrossedDuringASweep(t *testing.T) {
	f := newFixture(t)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-24", AutoRenew: true})
	clk := &switchClock{before: time.Date(2026, 9, 24, 23, 59, 59, 900_000_000, f.loc), after: time.Date(2026, 9, 25, 0, 0, 0, 100_000_000, f.loc)}
	e := New(Config{TTL: 30 * time.Second, Location: f.loc}, f.st, f.l, clk, f.log)
	must(t, e.Load(t.Context()))
	clk.arm()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); e.RunExpirySweep(ctx) }()
	testwait.Until(t, 10*time.Millisecond, func() bool { return f.expiresOn(t, f.ids[0]) == "2026-10-24" },
		"expires_on = %s, want 2026-10-24: the day boundary crossed during the sweep was skipped", testwait.When(func() string { return f.expiresOn(t, f.ids[0]) }))
	cancel()
	select {
	case <-done:
	case <-time.After(testwait.Bound):
		t.Fatal("RunExpirySweep did not return after cancel")
	}
}

// 载入与保存经同一个 CheckRule：绕过 Engine 直接写进库的越界到期规则载入时跳过并记一行 Warn，合法的照常载入并带着
// 提前天数。
func TestLoadChecksExpiryRules(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&logs, nil))
	bad := expiryRule()
	bad.DaysBefore = 400
	_, err := f.st.SaveAlertRule(t.Context(), bad)
	must(t, err)
	good, err := f.st.SaveAlertRule(t.Context(), expiryRule())
	must(t, err)
	f.restart(t)
	if rules := f.e.Rules(); len(rules) != 1 || rules[0].ID != good.ID || rules[0].DaysBefore != 7 {
		t.Fatalf("loaded rules = %+v, want only rule %d with days_before 7", rules, good.ID)
	}
	if want := `level=WARN msg="invalid alert rule skipped" rule_id=1 err="invalid: days_before must be between 1 and 365"`; strings.Count(logs.String(), want) != 1 {
		t.Fatalf("want one line %q in:\n%s", want, logs.String())
	}
}

func TestNewRequiresLocation(t *testing.T) {
	defer func() {
		if r := recover(); r != "alert.Config.Location must be set" {
			t.Fatalf("panic = %v", r)
		}
	}()
	New(Config{TTL: time.Second}, nil, nil, nil, nil)
}
