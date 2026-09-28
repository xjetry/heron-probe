package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// 编码边界样本独立于预算数字；业务校验可以拒绝它们，但不得借此缩小解码上界。
func settingsValueGenerators(channelIDs []string) map[string]func() any {
	escaped := func(n int) func() any { return func() any { return strings.Repeat("\x01", n) } }
	boolean := func() any { return false }
	uint32Value := func() any { return ^uint32(0) }
	return map[string]func() any{
		"title":          escaped(maxTitleBytes),
		"theme":          func() any { return slices.MaxFunc(themes, func(a, b string) int { return len(a) - len(b) }) },
		"accent_color":   func() any { return "#112233" },
		"logo":           func() any { return strings.Repeat("A", maxLogoBytes) },
		"custom_css":     escaped(maxCSSBytes),
		"public_enabled": boolean,
		"geo_enabled":    boolean,
		"geo_url":        escaped(maxGeoURLBytes),
		"geo_backend": func() any {
			values := probev1.GeoBackend(0).Descriptor().Values()
			longest := ""
			for i := 0; i < values.Len(); i++ {
				if name := string(values.Get(i).Name()); len(name) > len(longest) {
					longest = name
				}
			}
			return longest
		},
		"geo_mmdb_path":             escaped(maxMMDBPathBytes),
		"backup.endpoint":           escaped(maxEndpointBytes),
		"backup.bucket":             escaped(maxBucketBytes),
		"backup.region":             escaped(maxRegionBytes),
		"backup.access_key":         escaped(maxAccessKeyBytes),
		"backup.secret":             escaped(maxSecretBytes),
		"backup.prefix":             escaped(maxPrefixBytes),
		"backup.config_interval_s":  uint32Value,
		"backup.metrics_interval_s": uint32Value,
		"backup.config_keep":        uint32Value,
		"backup.metrics_keep":       uint32Value,
		"backup.notify.channel_ids": func() any { return channelIDs },
		"backup.has_secret":         boolean,
	}
}

func boundaryChannelIDs() []string {
	ids := make([]string, maxBackupChannels)
	for i := range ids {
		ids[i] = fmt.Sprint(int64(1<<63-1) - int64(i))
	}
	return ids
}

// descriptor 决定请求字段全集，不能按预算表的键组装，否则表漏项会同时缩短请求而掩盖缺陷。
func settingsBody(t *testing.T, generators map[string]func() any) []byte {
	t.Helper()
	var object func(protoreflect.MessageDescriptor, string) map[string]any
	object = func(md protoreflect.MessageDescriptor, prefix string) map[string]any {
		out := make(map[string]any)
		for i := 0; i < md.Fields().Len(); i++ {
			field := md.Fields().Get(i)
			name := string(field.Name())
			path := prefix + name
			if field.Kind() == protoreflect.MessageKind {
				out[name] = object(field.Message(), path+".")
			} else {
				generate, ok := generators[path]
				if !ok {
					t.Fatalf("missing settings value generator: %s", path)
				}
				out[name] = generate()
			}
		}
		return out
	}
	body, err := json.Marshal(map[string]any{"settings": object((&probev1.Settings{}).ProtoReflect().Descriptor(), "")})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSettingsBudgetTableCoversEveryField(t *testing.T) {
	paths := make(map[string]bool)
	var visit func(protoreflect.MessageDescriptor, string)
	visit = func(md protoreflect.MessageDescriptor, prefix string) {
		for i := 0; i < md.Fields().Len(); i++ {
			field := md.Fields().Get(i)
			path := prefix + string(field.Name())
			if field.Kind() == protoreflect.MessageKind {
				visit(field.Message(), path+".")
			} else {
				paths[path] = true
			}
		}
	}
	visit((&probev1.Settings{}).ProtoReflect().Descriptor(), "")
	generators := settingsValueGenerators(boundaryChannelIDs())
	for path := range paths {
		if _, ok := settingsBudget[path]; !ok {
			t.Errorf("settingsBudget missing leaf %s", path)
		}
		if _, ok := generators[path]; !ok {
			t.Errorf("settings generators missing leaf %s", path)
		}
	}
	for path := range settingsBudget {
		if !paths[path] {
			t.Errorf("settingsBudget has unknown leaf %s", path)
		}
	}
	for path := range generators {
		if !paths[path] {
			t.Errorf("settings generators have unknown leaf %s", path)
		}
	}
}

func TestSettingsBudgetEntriesAreExact(t *testing.T) {
	for path, generate := range settingsValueGenerators(boundaryChannelIDs()) {
		t.Run(path, func(t *testing.T) {
			encoded, err := json.Marshal(generate())
			if err != nil {
				t.Fatal(err)
			}
			if got, want := len(encoded), settingsBudget[path]; got != want {
				t.Errorf("%s encoded=%d budget=%d difference=%d", path, got, want, got-want)
			}
		})
	}
}

func TestSettingsBudgetIsTheWorstBody(t *testing.T) {
	body := settingsBody(t, settingsValueGenerators(boundaryChannelIDs()))
	if len(body) != maxSettingsBody {
		t.Fatalf("boundary body=%d budget=%d difference=%d", len(body), maxSettingsBody, len(body)-maxSettingsBody)
	}
	t.Logf("boundary body=%d budget=%d", len(body), maxSettingsBody)
}
