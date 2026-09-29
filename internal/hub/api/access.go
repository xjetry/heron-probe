package api

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

// accessTable 从服务描述符读出每个方法声明的准入口径，键为 Connect 过程路径。
// 未声明或取值不认识即 panic：描述符来自生成代码，只有改了 proto 却漏标时才会发生，
// 而那时 hub 在构造处理器时就起不来——未声明的方法不可能随发布出去被默认放行或默认拒绝。
func accessTable(svc protoreflect.ServiceDescriptor) map[string]heronv1.Access {
	methods := svc.Methods()
	table := make(map[string]heronv1.Access, methods.Len())
	for i := 0; i < methods.Len(); i++ {
		m := methods.Get(i)
		level, _ := proto.GetExtension(m.Options(), heronv1.E_Access).(heronv1.Access)
		switch level {
		case heronv1.Access_ACCESS_LOGIN, heronv1.Access_ACCESS_READ, heronv1.Access_ACCESS_SESSION:
		default:
			panic(fmt.Sprintf("%s does not declare a known heron.v1.access (got %v)", m.FullName(), level))
		}
		table["/"+string(svc.FullName())+"/"+string(m.Name())] = level
	}
	return table
}
