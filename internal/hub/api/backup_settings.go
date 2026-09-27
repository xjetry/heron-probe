package api

import (
	"slices"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/s3"
	"github.com/xjetry/probe/internal/hub/store"
	"google.golang.org/protobuf/proto"
)

func cleanBackup(in *probev1.BackupSettings) (*store.BackupSettingsUpdate, error) {
	if in == nil {
		return nil, nil
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"endpoint", in.Endpoint, 2048}, {"bucket", in.Bucket, 63}, {"region", in.Region, 64}, {"access_key", in.AccessKey, 128}, {"secret", in.GetSecret(), 4096}, {"prefix", in.Prefix, 512},
	} {
		if len(f.value) > f.max {
			return nil, invalid("backup.%s must be at most %d bytes", f.name, f.max)
		}
	}
	if in.Endpoint != "" {
		if err := s3.ValidateEndpoint(in.Endpoint); err != nil {
			return nil, invalid("%s", err)
		}
	}
	if in.Bucket != "" {
		if err := s3.ValidateBucket(in.Bucket); err != nil {
			return nil, invalid("%s", err)
		}
	}
	for _, f := range []struct{ name, value string }{{"region", in.Region}, {"access_key", in.AccessKey}} {
		if err := s3.ValidateSigningIdentifier(f.name, f.value); err != nil {
			return nil, invalid("%s", err)
		}
	}
	for _, f := range []struct {
		name     string
		value    *uint32
		min, max uint32
	}{
		{"config_interval_s", in.ConfigIntervalS, 60, 86400}, {"metrics_interval_s", in.MetricsIntervalS, 3600, 604800}, {"config_keep", in.ConfigKeep, 1, 1000}, {"metrics_keep", in.MetricsKeep, 1, 1000},
	} {
		if f.value != nil && (*f.value < f.min || *f.value > f.max) {
			return nil, invalid("backup.%s must be in [%d, %d]; got %d", f.name, f.min, f.max, *f.value)
		}
	}
	if len(in.Channels) > 100 {
		return nil, invalid("backup.channels must contain at most 100 IDs")
	}
	channels := slices.Clone(in.Channels)
	slices.Sort(channels)
	for i, id := range channels {
		if id <= 0 || i > 0 && channels[i-1] == id {
			return nil, invalid("backup.channels must contain distinct positive IDs")
		}
	}
	region := in.Region
	if region == "" {
		region = "auto"
	}
	return &store.BackupSettingsUpdate{Endpoint: in.Endpoint, Bucket: in.Bucket, Region: region, AccessKey: in.AccessKey, Secret: in.Secret, Prefix: in.Prefix, ConfigIntervalS: in.ConfigIntervalS, MetricsIntervalS: in.MetricsIntervalS, ConfigKeep: in.ConfigKeep, MetricsKeep: in.MetricsKeep, Channels: channels}, nil
}

// secret 不进入读侧消息；GetSettings 与 UpdateSettings 的回显共用此允许列表。
func backupProto(b store.BackupSettings) *probev1.BackupSettings {
	return &probev1.BackupSettings{Endpoint: b.Target.Endpoint, Bucket: b.Target.Bucket, Region: b.Target.Region, AccessKey: b.Target.AccessKey, Prefix: b.Prefix, ConfigIntervalS: proto.Uint32(b.ConfigIntervalS), MetricsIntervalS: proto.Uint32(b.MetricsIntervalS), ConfigKeep: proto.Uint32(b.ConfigKeep), MetricsKeep: proto.Uint32(b.MetricsKeep), Channels: b.Channels}
}
