package api

import (
	"fmt"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// projectionFixtures 造几组消息：Src 是 int32 a = 1，Pair 是 a = 1、b = 2，Painted 是 a = 1 与枚举 c = 2；其余各在一处
// 与它们不对齐（OtherEnumField 是与 EnumField 不对齐），或（PairPublic、PaintedPublic、MsgField、EnumField 之外）按 newProjection
// 的某一条约束写错。
func projectionFixtures(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	i32 := descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
	i64 := descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum()
	single := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	many := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	field := func(name string, number int32, typ *descriptorpb.FieldDescriptorProto_Type, label *descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Type: typ, Label: label, JsonName: proto.String(name)}
	}
	typed := func(name string, typ *descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
		f := field(name, 1, typ, single)
		f.TypeName = proto.String(typeName)
		return f
	}
	msg := func(name string, fs ...*descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
		return &descriptorpb.DescriptorProto{Name: proto.String(name), Field: fs}
	}
	// reserve 给 m 加 reserved：numbers 是 {start, end} 闭区间，names 是字段名。
	reserve := func(m *descriptorpb.DescriptorProto, numbers [][2]int32, names ...string) *descriptorpb.DescriptorProto {
		for _, r := range numbers {
			m.ReservedRange = append(m.ReservedRange, &descriptorpb.DescriptorProto_ReservedRange{Start: proto.Int32(r[0]), End: proto.Int32(r[1] + 1)})
		}
		m.ReservedName = names
		return m
	}
	a := func() *descriptorpb.FieldDescriptorProto { return field("a", 1, i32, single) }
	colorC := field("c", 2, descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), single)
	colorC.TypeName = proto.String(".projfix.Color")
	// proto3 optional 由一个合成 oneof 承载 presence。
	optional := msg("WithPresence", a())
	optional.Field[0].Proto3Optional, optional.Field[0].OneofIndex = proto.Bool(true), proto.Int32(0)
	optional.OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_a")}}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("projection_fixtures.proto"), Package: proto.String("projfix"), Syntax: proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{Name: proto.String("Color"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("COLOR_UNSPECIFIED"), Number: proto.Int32(0)}}},
			{Name: proto.String("Shade"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("SHADE_UNSPECIFIED"), Number: proto.Int32(0)}}},
		},
		MessageType: []*descriptorpb.DescriptorProto{
			msg("Src", a()),
			msg("Renamed", field("z", 1, i32, single)),
			msg("Renumbered", field("a", 2, i32, single)),
			msg("Wider", field("a", 1, i64, single)),
			msg("Repeated", field("a", 1, i32, many)),
			optional,
			msg("MsgField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), ".projfix.Src")),
			msg("EnumField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), ".projfix.Color")),
			msg("OtherEnumField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), ".projfix.Shade")),
			msg("Painted", a(), colorC),
			reserve(msg("PaintedPublic", a()), [][2]int32{{2, 2}}, "c"),
			msg("PaintedUnreserved", a()),
			msg("Pair", a(), field("b", 2, i32, single)),
			reserve(msg("PairPublic", a()), [][2]int32{{2, 2}}, "b"),
			msg("PairUnreserved", a()),
			reserve(msg("PairNumberOnly", a()), [][2]int32{{2, 2}}),
			reserve(msg("PairNameOnly", a()), nil, "b"),
			reserve(msg("PairStaleName", a()), [][2]int32{{2, 2}}, "b", "c"),
			reserve(msg("PairStaleNumber", a()), [][2]int32{{2, 2}, {5, 5}}, "b"),
			reserve(msg("PairWideRange", a()), [][2]int32{{2, 536870911}}, "b"),
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestNewProjectionRejectsMisalignedFields(t *testing.T) {
	fd := projectionFixtures(t)
	desc := func(name string) protoreflect.MessageDescriptor { return fd.Messages().ByName(protoreflect.Name(name)) }
	const hideB = `projfix.Pair.b is not public, so projfix.%s must reserve both its number 2 and its name "b"`
	for _, c := range []struct{ dst, src, want string }{
		{"Renamed", "Src", "has no counterpart named z"},
		{"Renumbered", "Src", "projfix.Renumbered.a is field 2 but projfix.Src.a is field 1"},
		{"Wider", "Src", "does not match"},
		{"WithPresence", "Src", "does not match"},
		{"Repeated", "Src", "only singular scalar fields"},
		{"Src", "Repeated", "only singular scalar fields"},
		{"MsgField", "MsgField", "only singular scalar fields"},
		{"OtherEnumField", "EnumField", "projfix.OtherEnumField.a uses enum projfix.Shade but projfix.EnumField.a uses enum projfix.Color"},
		{"PaintedUnreserved", "Painted", `projfix.Painted.c is not public, so projfix.PaintedUnreserved must reserve both its number 2 and its name "c"`},
		{"PairUnreserved", "Pair", fmt.Sprintf(hideB, "PairUnreserved")},
		{"PairNumberOnly", "Pair", fmt.Sprintf(hideB, "PairNumberOnly")},
		{"PairNameOnly", "Pair", fmt.Sprintf(hideB, "PairNameOnly")},
		{"PairStaleName", "Pair", `projfix.PairStaleName reserves name "c", which is not a non-public field of projfix.Pair`},
		{"PairStaleNumber", "Pair", "projfix.PairStaleNumber reserves number 5, which is not a non-public field of projfix.Pair"},
		{"PairWideRange", "Pair", "projfix.PairWideRange reserves numbers 2 to 536870911, more than the 1 non-public fields of projfix.Pair"},
	} {
		t.Run(c.dst+"<-"+c.src, func(t *testing.T) {
			expectPanic(t, c.want, func() { newProjection(dynamicpb.NewMessageType(desc(c.dst)), desc(c.src)) })
		})
	}
	newProjection(dynamicpb.NewMessageType(desc("Src")), desc("Src"))
	newProjection(dynamicpb.NewMessageType(desc("PairPublic")), desc("Pair"))
	newProjection(dynamicpb.NewMessageType(desc("EnumField")), desc("EnumField"))
	newProjection(dynamicpb.NewMessageType(desc("PaintedPublic")), desc("Painted"))
}

// 只复制源里存在的字段：optional 缺失仍是缺失，显式的 0 仍是 0；目标没有的字段（boot_id）不出现。
func TestProjectionKeepsPresenceAndDropsUndeclaredFields(t *testing.T) {
	p := newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor())
	got := p.apply(&probev1.Metrics{BootId: "b", MemUsed: proto.Uint64(0), Load1: proto.Float64(0.5)}).(*probev1.PublicMetrics)
	if want := (&probev1.PublicMetrics{MemUsed: proto.Uint64(0), Load1: proto.Float64(0.5)}); !proto.Equal(got, want) || got.CpuPct != nil {
		t.Fatalf("projected = %v, want %v", got, want)
	}
}

// PublicBilling 由 Billing 投影：周期按编号原样复制，自动续期不出现，days_left 的缺失与 0 各自保留。期望值从 JSON 读入、
// days_left 经反射取：两个消息对不齐时本测试照常编译，红在构造投影的 panic 上。
func TestPublicBillingProjectsFromBilling(t *testing.T) {
	p := newProjection((&probev1.PublicBilling{}).ProtoReflect().Type(), (&probev1.Billing{}).ProtoReflect().Descriptor())
	got := p.apply(&probev1.Billing{Price: "12.50", Currency: "USD", BillingCycle: probev1.BillingCycle_BILLING_CYCLE_YEARLY,
		ExpiresOn: "2026-10-01", AutoRenew: true, DaysLeft: proto.Int32(0)})
	want := &probev1.PublicBilling{}
	if err := protojson.Unmarshal([]byte(`{"price": "12.50", "currency": "USD", "billingCycle": "BILLING_CYCLE_YEARLY", "expiresOn": "2026-10-01", "daysLeft": 0}`), want); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("projected = %v, want %v", got, want)
	}
	noDate := p.apply(&probev1.Billing{Price: "5", Currency: "EUR"}).ProtoReflect()
	if noDate.Has(noDate.Descriptor().Fields().ByName("days_left")) {
		t.Fatalf("no expiry date projected days_left: %v", noDate.Interface())
	}
}
