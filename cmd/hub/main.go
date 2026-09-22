// probe-hub：serve 起服务；其余子命令直接操作数据库，供 serve 之外的运维动作。
//
// 直接改表的子命令绕过了 auth 的内存映射：token 映射在 hub 启动时自库重建，
// 所以 node create / delete / rotate-token 在 hub 运行期间需要重启才生效。
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "node":
		err = runNode(os.Args[2:])
	case "window":
		err = runWindow(os.Args[2:])
	case "stats":
		err = runStats(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: probe-hub <command> [flags]

commands:
  serve                     start the hub
  node create|list|delete|rotate-token
  window open|close|show    manage the registration window
  stats                     row counts per table
  version`)
}
