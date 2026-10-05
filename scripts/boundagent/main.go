// Command boundagent 取回绑定版本的 agent 产物（spec §14.1）。只发 hub 的 release 用 fetch 把绑定版本已验签的
// install.sh 与 install-macos.sh 原样放进本次 dist/：这两个脚本内嵌的是绑定版本的版本号、下载目录与哈希，
// latest 的安装命令因此永远装 hub 绑定的 agent。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/xjetry/heron-probe/internal/releasesig"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "fetch" {
		fmt.Fprintln(stderr, "usage: boundagent fetch -version vY -dir DIR -linux-arches \"<arches>\" -darwin-arches \"<arches>\"")
		return 2
	}
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "bound agent version (AGENT_VERSION)")
	dir := fs.String("dir", "", "directory to write the installers into")
	linuxArches := fs.String("linux-arches", "", "space-separated Linux architectures of the agent bundle")
	darwinArches := fs.String("darwin-arches", "", "space-separated darwin architectures of the agent bundle")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	// 架构清单也必填：缺了它 agent 组的完整性核对退化为只查两个脚本，门禁形同虚设。
	if *version == "" || *dir == "" || *linuxArches == "" || *darwinArches == "" {
		fmt.Fprintln(stderr, "boundagent fetch: -version, -dir, -linux-arches and -darwin-arches are all required")
		return 2
	}
	// 下载地址与受信公钥不给命令行开关：换信任根必须是仓库里一次可审阅的改动，不是发版命令的参数。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err := fetchInstallers(ctx, &http.Client{}, officialDownloads, releasesig.Trusted(), *version, *dir,
		strings.Fields(*linuxArches), strings.Fields(*darwinArches))
	if err != nil {
		fmt.Fprintln(stderr, "boundagent:", err)
		return 1
	}
	return 0
}
