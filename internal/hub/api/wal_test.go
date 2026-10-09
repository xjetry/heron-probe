package api

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

func TestWALObservationWireStates(t *testing.T) {
	t.Parallel()
	zero, size := int64(0), int64(12345)
	for _, tc := range []struct {
		name string
		wal  store.WALObservation
		json string
	}{
		{"present", store.WALObservation{ObservedAt: 123, Bytes: &size}, `{"wal":{"observedAt":"123","bytes":"12345"}}`},
		{"empty", store.WALObservation{ObservedAt: 123, Bytes: &zero}, `{"wal":{"observedAt":"123","bytes":"0"}}`},
		{"absent", store.WALObservation{ObservedAt: 123, Absent: true}, `{"wal":{"observedAt":"123","absent":true}}`},
		{"error", store.WALObservation{ObservedAt: 123, Error: "permission denied"}, `{"wal":{"observedAt":"123","error":"permission denied"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := &heronv1.GetStorageStatsResponse{Wal: walObservationProto(tc.wal)}
			want := &heronv1.GetStorageStatsResponse{}
			if err := protojson.Unmarshal([]byte(tc.json), want); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(got, want) {
				t.Fatalf("WAL projection = %v, want %v", got, want)
			}
			for _, codec := range []struct {
				name      string
				marshal   func(proto.Message) ([]byte, error)
				unmarshal func([]byte, proto.Message) error
			}{{"protobuf", proto.Marshal, proto.Unmarshal}, {"json", protojson.Marshal, protojson.Unmarshal}} {
				data, err := codec.marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				back := &heronv1.GetStorageStatsResponse{}
				if err := codec.unmarshal(data, back); err != nil {
					t.Fatal(err)
				}
				if !proto.Equal(back, want) {
					t.Fatalf("%s roundtrip = %v, want %v", codec.name, back, want)
				}
			}
		})
	}
	old := &heronv1.GetStorageStatsResponse{}
	if err := protojson.Unmarshal([]byte(`{"dbBytes":"123"}`), old); err != nil {
		t.Fatal(err)
	}
	if old.Wal != nil {
		t.Fatalf("old hub response acquired WAL observation: %v", old.Wal)
	}
}

func TestGetStorageStatsIncludesWALObservation(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	h.login(t)
	h.createNode(t, "wal")
	resp, err := h.admin.GetStorageStats(t.Context(), connect.NewRequest(&heronv1.GetStorageStatsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	w := resp.Msg.Wal
	if w == nil || w.GetBytes() == 0 || w.ObservedAt != h.clk.Now().Unix() {
		t.Fatalf("WAL response = %v, want present with hub time", w)
	}
}
