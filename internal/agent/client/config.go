package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/xjetry/probe/internal/agent/prober"
)

// Config 是 agent 的配置文件。Hub、Token、Name 由 register 写入；其余字段是宿主机的本地策略（§4.8、§5.7），
// 只由 configure 或手工修改，hub 改不了：agent 运行期不写配置，下行消息里也没有对应字段。
type Config struct {
	Hub          string   `json:"hub"`
	Token        string   `json:"token"`
	Name         string   `json:"name"`
	InsecureHTTP bool     `json:"insecure_http,omitempty"`
	ProbeAllow   []string `json:"probe_allow,omitempty"`
	ProbeDeny    []string `json:"probe_deny,omitempty"`
}

// LoadConfig 读取并校验配置，run 只经过它。
func LoadConfig(path string) (Config, error) {
	c, err := ReadConfig(path)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

// ReadConfig 只解析不校验，供 configure 修正一份当前不合规的配置（例如升级后才被拒的明文 hub 地址）。
// 未知字段是错误：本地策略字段拼错时静默忽略，宿主机以为拒绝了的地址实际放行。
func ReadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Validate 是 run 与 configure 共用的准入：hub 地址的传输规则与本地探测策略。
func (c Config) Validate() error {
	if err := CheckHub(c.Hub, c.InsecureHTTP); err != nil {
		return err
	}
	_, err := c.Policy()
	return err
}

// Policy 解析本地探测策略；字段为空即只有默认拒绝集（prober.Policy 的零值）。
func (c Config) Policy() (prober.Policy, error) { return prober.ParsePolicy(c.ProbeAllow, c.ProbeDeny) }

// CheckHub 是 hub 地址的传输规则（§5.7），register 在发请求之前、run 在加载配置时调用。https 总是接受；
// http 只在主机是 loopback IP 字面量、或本地显式放行（insecure）时接受：明文链路上的中间人与 hub 失守等价。
// localhost 这类名字要经解析，不在豁免内。
func CheckHub(raw string, insecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("hub %q must be an absolute https:// URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && ip.Unmap().IsLoopback() {
			return nil
		}
		if insecure {
			return nil
		}
		return fmt.Errorf("hub %q uses plain http, so the node token and metrics would travel unencrypted; use https, or accept this explicitly with `probe-agent register --insecure-http` or `probe-agent configure --insecure-http=true`", raw)
	}
	return fmt.Errorf("hub %q must be an https:// URL", raw)
}

// SaveConfig 先写临时文件再 rename：token 不会以部分写入的状态落盘。
//
// 权限不变式：落盘后的配置对其他用户不可读。它由"临时文件一定是本次独占
// 创建的"承载：先删掉可能残留的同名临时文件，再以 O_EXCL 创建——WriteFile
// 的权限参数只对新建文件生效，残留的 0644 临时文件会带着旧权限被 rename 成配置。
//
// 属主不变式：目标已存在时，新文件沿用它的属主与属组。安装脚本把配置交给服务用户，root 执行 configure
// 后若换成 root 属主的 0600 文件，服务就读不到配置；改不了属主（非 root 改别人的文件）时报错而不是留下读不到的配置。
func SaveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	owner := -1
	group := -1
	if st, err := os.Stat(path); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			owner, group = int(sys.Uid), int(sys.Gid)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if owner >= 0 {
		if err := f.Chown(owner, group); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("keep the owner of %s: %w", path, err)
		}
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
