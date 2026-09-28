package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xjetry/probe/internal/hub/store"
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
		if strings.HasPrefix(o.Key, prefix) {
			remote[o.Key] = true
		}
	}
	themes, err := m.st.ListThemes(ctx)
	if err != nil {
		return failure("theme_read", err)
	}
	for _, theme := range themes {
		key := prefix + theme.ID + ".zip"
		exists := remote[key]
		delete(remote, key)
		p, err := m.st.ThemeBackupPackage(ctx, theme.ID)
		if errors.Is(err, store.ErrNotFound) {
			return "theme_package", 0, fmt.Sprintf("theme %q has no original package; upload it again", theme.ID)
		}
		if err != nil {
			return failure("theme_read", err)
		}
		if exists && p.Uploaded {
			continue
		}
		uploadCtx, cancelUpload := context.WithTimeout(ctx, uploadBudget(cfg, "config", int64(len(p.Content))))
		err = client.PutObject(uploadCtx, key, bytes.NewReader(p.Content))
		cancelUpload()
		if err != nil {
			return failure("theme_upload", err)
		}
		if err := m.st.MarkThemeUploaded(ctx, theme.ID, p.Revision); err != nil {
			return failure("theme_record", err)
		}
	}
	deleteCtx, cancelDelete := context.WithTimeout(ctx, retentionBudget)
	defer cancelDelete()
	for key := range remote {
		if err := client.DeleteObject(deleteCtx, key); err != nil {
			return failure("theme_delete", err)
		}
	}
	return "", 0, ""
}
