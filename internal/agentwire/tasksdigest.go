package agentwire

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"slices"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"google.golang.org/protobuf/proto"
)

// TasksDigest 是任务清单对账摘要的唯一算法，agent 上报（ReportRequest.tasks_digest）与
// hub 决定要不要重发清单调用同一函数，两侧不能各自持有一份。
// 编码对象是清单本身而不含版本计数：版本在备份恢复后可能与 agent 持有的内容错位，
// 按计数对账会让旧配置一直留在 agent 上，按内容摘要不会。
// 任务按 task_id 升序（收到顺序不影响结果），各自确定性 protobuf 编码，前缀 8 字节
// 大端长度后依次拼接，对整串取 SHA-256。长度前缀使拼接边界无歧义：少了它，
// 两组不同的任务序列可能拼出同一串字节。
// 空清单返回空串的 SHA-256，与"尚未收到任何清单"（调用方不带该字段）严格区分。
func TasksDigest(tasks []*heronv1.ProbeTask) []byte {
	sorted := slices.SortedFunc(slices.Values(tasks), func(a, b *heronv1.ProbeTask) int { return cmp.Compare(a.GetId(), b.GetId()) })
	h := sha256.New()
	var size [8]byte
	for _, t := range sorted {
		// 清单经 protobuf 解码而来，字符串字段已是合法 UTF-8，确定性编码不会失败。
		b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(t)
		binary.BigEndian.PutUint64(size[:], uint64(len(b)))
		h.Write(size[:])
		h.Write(b)
	}
	return h.Sum(nil)
}
