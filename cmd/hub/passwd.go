package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func runPasswd(args []string) error { return runPasswdWith(args, os.Stdin, os.Stderr) }

// runPasswdWith 只在 hub 主机上跑：没有经网络的首次设置页，也就没有"谁先访问谁
// 占有"的窗口。运行中的 hub 不需要重启——登录路径每次都读库，旧会话则在同一
// 事务里被清空。
func runPasswdWith(args []string, in *os.File, prompt io.Writer) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	plain, err := readPassword(in, prompt)
	if err != nil {
		return err
	}
	st, a, err := openOffline(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := a.SetPassword(context.Background(), plain); err != nil {
		return err
	}
	fmt.Fprintln(prompt, "admin password set; every existing session has been revoked")
	return nil
}

// readPassword 在终端上不回显地读两遍并比对；stdin 不是终端时读一行——供容器
// 初始化与脚本使用，仍不经网络。
func readPassword(in *os.File, prompt io.Writer) (string, error) {
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(prompt, "New admin password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(prompt)
	if err != nil {
		return "", err
	}
	fmt.Fprint(prompt, "Repeat: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(prompt)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
