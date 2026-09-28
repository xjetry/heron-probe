package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// NewToken 生成节点 token 或注册窗口 key：32 字节随机数，hex 编码。
// token 是高熵随机数，SHA-256 足够——不需要抗字典攻击的慢哈希，而慢哈希撑不住
// 每秒数百次的上报校验。
func NewToken() (string, [32]byte) {
	var b [32]byte
	rand.Read(b[:])
	plain := hex.EncodeToString(b[:])
	return plain, HashToken(plain)
}

func HashToken(plain string) [32]byte { return sha256.Sum256([]byte(plain)) }

// isTokenShaped 判定 s 是否具有 NewToken 明文的形状：64 个小写十六进制字符（hex.EncodeToString 只产生小写）。
// 形状不符的字符串不可能是 NewToken 签发的 token，鉴权可以不查库就拒绝它。
func isTokenShaped(s string) bool {
	if len(s) != 2*32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
