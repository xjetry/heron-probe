package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const purego = "github.com/ebitengine/purego"

func deps(t *testing.T, goos, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=arm64", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s (GOOS=%s): %v\n%s", pkg, goos, err, out)
	}
	return strings.Fields(string(out))
}

// darwin 的采集依赖只在带 darwin 约束的文件里引用，不链入 Linux 二进制（spec §2）。
// purego 在 Linux 上也能编译：不带 darwin 约束的文件（darwinraw.go 就是一个）一旦引用它，
// 各平台照样编译通过，Linux 产物却链入了它。这里按依赖图检查，缺陷在引入它的那次提交就红。
// darwin 一侧必须看得见 purego：否则这条检查对"列表为空"与"确实没有"分不出来。
func TestPuregoOnlyLinksIntoDarwin(t *testing.T) {
	if !slices.Contains(deps(t, "darwin", "github.com/xjetry/probe/cmd/agent"), purego) {
		t.Fatal("darwin agent does not depend on purego; the Linux check below would pass vacuously")
	}
	for _, pkg := range []string{"github.com/xjetry/probe/cmd/agent", "github.com/xjetry/probe/cmd/hub"} {
		if slices.Contains(deps(t, "linux", pkg), purego) {
			t.Fatalf("%s links %s on linux", pkg, purego)
		}
	}
}
