package deploy

import (
	"os"
	"strings"
	"testing"
)

// linuxBrokenRelease 发布一个"起不来"的 agent 版本：包里的 heron-agent 带 heron-test-broken 标记，
// linuxStubs 的 systemctl start 见到它就返回 0 却不建进程，回滚用例因此走到启动确认失败的分支。
// 发布形状与 linuxRelease 相同，只换掉二进制内容，其余（单元、OpenRC 脚本、更新器包）保持一致。
func (e *env) linuxBrokenRelease(arch, version string) {
	e.t.Helper()
	e.updaterPackage(e.dist, arch, version)
	unit, err := os.ReadFile("systemd/heron-agent.service")
	if err != nil {
		e.t.Fatal(err)
	}
	openrc, err := os.ReadFile("openrc/heron-agent")
	if err != nil {
		e.t.Fatal(err)
	}
	e.pack(e.dist, "heron-agent_linux_"+arch+".tar.gz", []packFile{
		{"heron-agent", fakeAgent + "# " + version + " " + arch + "\n# heron-test-broken\n", 0o755},
		{"heron-agent.service", string(unit), 0o644},
		{"heron-agent.openrc", string(openrc), 0o755},
	})
	e.publish(version)
}

// hubBrokenRelease 与 hubRelease 同形，发布一个带 heron-test-broken 标记的 hub；hubStubs 的 start
// 在退出前把三个库文件改写成新内容（模拟候选 hub 打开库时的 schema 迁移），再返回 0 却不建进程。
func (e *env) hubBrokenRelease(version string) {
	e.t.Helper()
	e.updaterPackage(e.dist, "amd64", version)
	unit, err := os.ReadFile("systemd/heron-hub.service")
	if err != nil {
		e.t.Fatal(err)
	}
	e.pack(e.dist, "heron-hub_linux_amd64.tar.gz", []packFile{
		{"heron-hub", "#!/bin/sh\n# " + version + " amd64\n# heron-test-broken\n", 0o755},
		{"heron-hub.service", string(unit), 0o644},
	})
	e.publish(version)
}

// 升级到起不来的新 agent：安装器把上一版换回来并按 systemd 重启，stderr 说明已回滚，服务重新在跑，
// .bak 随成功回滚消失（首次安装没有旧版本，另有用例）。
func TestLinuxRollsBackWhenTheNewAgentDoesNotStart(t *testing.T) {
	t.Parallel()
	e := newLinuxInstalled(t)
	e.linuxBrokenRelease("amd64", "v2")
	out, code := e.linuxInstall()
	if code == 0 || !strings.Contains(out, "the new heron-agent did not start; restored the previous binary") {
		t.Fatalf("upgrade to a binary that does not start must roll back: exit %d:\n%s", code, out)
	}
	if got := e.file("usr/local/bin/heron-agent"); !strings.Contains(got, "# v1 amd64") {
		t.Fatalf("the previous binary must be back, got %q", got)
	}
	if e.exists("usr/local/bin/heron-agent.bak") {
		t.Fatal("the .bak must not survive a successful rollback")
	}
	if !e.exists("proc/4242/status") {
		t.Fatal("the previous agent must be running again")
	}
}

// 首次安装没有旧二进制：起不来时没有可回滚的版本，安装器只报启动失败，不伪造一条回滚消息、也不留下 .bak。
func TestLinuxFirstInstallOfABrokenAgentDoesNotRollBack(t *testing.T) {
	t.Parallel()
	e := newLinuxHost(t)
	e.appendTo("etc/passwd", "heron-agent:x:480:480::/nonexistent:/usr/sbin/nologin\n")
	e.appendTo("etc/group", "heron-agent:x:480:\n")
	e.linuxBrokenRelease("amd64", "v1")
	out, code := e.linuxInstall()
	if code == 0 {
		t.Fatalf("a first install of a binary that does not start must fail:\n%s", out)
	}
	if strings.Contains(out, "restored the previous binary") || e.exists("usr/local/bin/heron-agent.bak") {
		t.Fatalf("a first install has nothing to roll back:\n%s", out)
	}
}

// 升级到起不来的新 hub：旧二进制与三个库文件一起换回来（候选 hub 启动时已迁移 schema，只换二进制会把 hub
// 留在旧程序打不开新库的状态），并按 systemd 重启；服务重新在跑，.bak 随成功回滚消失。
func TestHubRollsBackBinaryAndDatabaseWhenTheNewHubDoesNotStart(t *testing.T) {
	t.Parallel()
	e := newHubInstalled(t)
	for rel, body := range map[string]string{
		"heron.db":     "old-schema\n",
		"heron.db-wal": "old-wal\n",
		"heron.db-shm": "old-shm\n",
	} {
		e.put(hubData+"/"+rel, body)
	}
	e.hubBrokenRelease("v2")
	out, code := e.hubInstall()
	if code == 0 || !strings.Contains(out, "the new heron-hub did not start; restored the previous binary and database") {
		t.Fatalf("upgrade to a binary that does not start must roll back: exit %d:\n%s", code, out)
	}
	if got := e.file("usr/local/bin/heron-hub"); !strings.Contains(got, "# v1 amd64") {
		t.Fatalf("the previous binary must be back, got %q", got)
	}
	for rel, want := range map[string]string{
		"heron.db":     "old-schema\n",
		"heron.db-wal": "old-wal\n",
		"heron.db-shm": "old-shm\n",
	} {
		if got := e.file(hubData + "/" + rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	for _, rel := range []string{"usr/local/bin/heron-hub.bak", hubData + "/heron.db.bak", hubData + "/heron.db-wal.bak", hubData + "/heron.db-shm.bak"} {
		if e.exists(rel) {
			t.Errorf("%s must not survive a successful rollback", rel)
		}
	}
	if !e.exists("proc/4242/status") {
		t.Fatal("the previous hub must be running again")
	}
}

// 候选 hub 新建的 WAL/SHM 在备份时并不存在：回滚删掉它们，别把新 schema 的页留给旧库；旧库本身照旧换回。
func TestHubRollbackRemovesWalFilesTheCandidateCreated(t *testing.T) {
	t.Parallel()
	e := newHubInstalled(t)
	e.put(hubData+"/heron.db", "old-schema\n")
	e.hubBrokenRelease("v2")
	out, code := e.hubInstall()
	if code == 0 || !strings.Contains(out, "restored the previous binary and database") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := e.file(hubData + "/heron.db"); got != "old-schema\n" {
		t.Fatalf("heron.db = %q, want the old schema", got)
	}
	for _, rel := range []string{"heron.db-wal", "heron.db-shm"} {
		if e.exists(hubData + "/" + rel) {
			t.Errorf("%s was created by the candidate hub and must not outlive the rollback", rel)
		}
	}
}
