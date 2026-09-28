package backup

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/theme"
)

// 配置层的 runMu 串行化远端写入，避免旧上传晚于新上传覆盖同一对象。
// 数据库写入不等待网络；完成标记只匹配本轮读到的随机写入标识，新包由后续唤醒或周期补传。
func (m *Manager) syncThemes(ctx context.Context, cfg store.BackupSettings, client objectStore) (string, int, string) {
	prefix := strings.TrimRight(cfg.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += "theme/"
	listCtx, cancelList := context.WithTimeout(ctx, retentionBudget)
	objects, err := client.ListObjectsV2(listCtx, prefix, maxListedObjects)
	cancelList()
	if err != nil {
		return failure("theme_list", err)
	}
	remote := make(map[string]bool, len(objects))
	for _, o := range objects {
		name, inside := strings.CutPrefix(o.Key, prefix)
		id, zip := strings.CutSuffix(name, ".zip")
		if inside && zip && theme.ValidID(id) {
			remote[o.Key] = true
		}
	}
	themes, err := m.st.ThemeBackupEntries(ctx)
	if err != nil {
		return failure("theme_read", err)
	}
	var firstCategory, firstDetail string
	var firstCode int
	record := func(stage string, err error) {
		if firstCategory == "" {
			firstCategory, firstCode, firstDetail = failure(stage, err)
		}
	}
	for _, theme := range themes {
		key := prefix + theme.ID + ".zip"
		exists := remote[key]
		delete(remote, key)
		// 缺包仅由清单快照判定；旧主题和恢复空壳都保留远端对象，由状态列表提示补传。
		if !theme.HasPackage {
			continue
		}
		if exists && theme.Uploaded {
			continue
		}
		content, err := m.st.ThemeBackupContent(ctx, theme.ID, theme.Revision)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			record("theme_read", err)
			continue
		}
		uploadCtx, cancelUpload := context.WithTimeout(ctx, uploadBudget(cfg, "config", int64(len(content))))
		err = client.PutObject(uploadCtx, key, bytes.NewReader(content))
		cancelUpload()
		if err != nil {
			record("theme_upload", err)
			continue
		}
		if err := m.st.MarkThemeUploaded(ctx, theme.ID, theme.Revision); err != nil {
			record("theme_record", err)
		}
	}
	deleteCtx, cancelDelete := context.WithTimeout(ctx, retentionBudget)
	defer cancelDelete()
	for key := range remote {
		if err := client.DeleteObject(deleteCtx, key); err != nil {
			record("theme_delete", err)
		}
	}
	return firstCategory, firstCode, firstDetail
}
