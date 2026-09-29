package client

import (
	"hash/fnv"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"google.golang.org/protobuf/proto"
)

// FactsHash 是 agent 侧的静态信息摘要：hub 只比较、不重算，所以只需在
// 同一 agent 内稳定。确定性序列化保证同样的 Facts 得到同样的字节。
func FactsHash(f *heronv1.Facts) uint64 {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(f)
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}
