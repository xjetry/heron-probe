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
