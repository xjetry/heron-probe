package releasesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"slices"
)

// trusted 是正式产物的受信公钥：标准 base64 的 32 字节 Ed25519 公钥；私钥只在 release 流水线的
// Actions secret 里。公钥写在源码里而不经 ldflags 或 build tag 注入：换钥必然是仓库里一次可审阅的改动，
// 正式二进制里没有第二把。更新器只能由 root 安装器升级（spec §5.7），这里的增删要每台主机重跑安装器才生效。
var trusted = mustParse()

// Trusted 返回受信公钥的副本，调用方改动切片不影响后续验签。
func Trusted() []ed25519.PublicKey { return slices.Clone(trusted) }

func mustParse(encoded ...string) []ed25519.PublicKey {
	keys := make([]ed25519.PublicKey, 0, len(encoded))
	for _, s := range encoded {
		raw, err := base64.StdEncoding.Strict().DecodeString(s)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			panic("releasesig: trusted key is not a base64 Ed25519 public key: " + s)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys
}
