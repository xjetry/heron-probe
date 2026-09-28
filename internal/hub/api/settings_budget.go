package api

import (
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// settingsBudget 登记 Settings 每个叶字段的 JSON 解码上界，包含值自身的引号或方括号，
// 不含字段名与分隔符。字符串按字节上限及编码膨胀计，不要求边界样本通过业务校验。
// TestSettingsBudgetTableCoversEveryField 枚举 descriptor 核对表和样本的键，
// TestSettingsBudgetEntriesAreExact 与 TestSettingsBudgetIsTheWorstBody 核对编码长度及总和。
// 防漏项靠枚举与总和等式，不靠预算与业务合法值的贴合程度。
var settingsBudget = map[string]int{
	"title":                     6*maxTitleBytes + 2,
	"theme":                     len("light") + 2,
	"accent_color":              len("#112233") + 2,
	"logo":                      maxLogoBytes + 2,
	"custom_css":                6*maxCSSBytes + 2,
	"public_enabled":            len("false"),
	"geo_enabled":               len("false"),
	"geo_url":                   6*maxGeoURLBytes + 2,
	"geo_backend":               len("GEO_BACKEND_UNSPECIFIED") + 2,
	"geo_mmdb_path":             6*maxMMDBPathBytes + 2,
	"backup.endpoint":           6*maxEndpointBytes + 2,
	"backup.bucket":             6*maxBucketBytes + 2,
	"backup.region":             6*maxRegionBytes + 2,
	"backup.access_key":         6*maxAccessKeyBytes + 2,
	"backup.secret":             6*maxSecretBytes + 2,
	"backup.prefix":             6*maxPrefixBytes + 2,
	"backup.config_interval_s":  len("4294967295"),
	"backup.metrics_interval_s": len("4294967295"),
	"backup.config_keep":        len("4294967295"),
	"backup.metrics_keep":       len("4294967295"),
	"backup.notify.channel_ids": maxBackupChannels*maxChannelIDJSONBytes + 2 - 1,
	"backup.has_secret":         len("false"),
}

// budgetTotal 用 proto 原名计算字段名，避免较短的 camelCase 名低估允许的请求长度。
// 每个对象只在字段之间计逗号；各消息的大括号和最外层请求骨架各计一次。
func budgetTotal() int {
	var fieldsSize func(protoreflect.MessageDescriptor, string) int
	fieldsSize = func(md protoreflect.MessageDescriptor, prefix string) int {
		fields := md.Fields()
		total := max(0, fields.Len()-1)
		for i := 0; i < fields.Len(); i++ {
			field := fields.Get(i)
			name := string(field.Name())
			path := prefix + name
			total += len(name) + len(`"":`)
			if field.Kind() == protoreflect.MessageKind {
				total += len(`{}`) + fieldsSize(field.Message(), path+".")
			} else {
				total += settingsBudget[path]
			}
		}
		return total
	}
	return len(`{"settings":{}}`) + fieldsSize((&probev1.Settings{}).ProtoReflect().Descriptor(), "")
}

var maxSettingsBody = budgetTotal()
