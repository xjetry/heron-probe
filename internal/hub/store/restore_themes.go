package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xjetry/probe/internal/hub/theme"
)

type ThemeRestoreSummary struct {
	Restored []string `json:"restored"`
	Missing  []string `json:"missing"`
	Ignored  []string `json:"ignored"`
}

type themeRestorePlan struct {
	ids      []string
	packages map[string][]byte
	ignored  []string
}

func themeIDs(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT id FROM theme ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// 在打开目标之前校验整个目录，包括将被忽略的 zip。仅保留所需原包字节，
// 每次最多展开一个包；事务内不再读目录，避免校验后文件被替换而写入未经校验的字节。
func prepareThemeRestore(ctx context.Context, config, dir string) (*themeRestorePlan, error) {
	plan := &themeRestorePlan{packages: make(map[string][]byte)}
	if dir == "" {
		return plan, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("themes directory: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(config, "&mode=ro"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	plan.ids, err = themeIDs(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(plan.ids) > theme.MaxThemes {
		return nil, fmt.Errorf("config snapshot has more than %d themes", theme.MaxThemes)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("theme package %q is not a regular file", entry.Name())
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(f, theme.MaxPackageBytes+1))
		if err := errors.Join(err, f.Close()); err != nil {
			return nil, err
		}
		pkg, err := theme.Parse(content)
		if err != nil {
			return nil, fmt.Errorf("theme package %q: %w", entry.Name(), err)
		}
		id := strings.TrimSuffix(entry.Name(), ".zip")
		if !slices.Contains(plan.ids, id) {
			plan.ignored = append(plan.ignored, entry.Name())
			continue
		}
		if pkg.Manifest.ID != id {
			return nil, fmt.Errorf("theme package %q: id %q does not match %q", entry.Name(), pkg.Manifest.ID, id)
		}
		plan.packages[id] = content
	}
	return plan, nil
}

func restoreThemeContent(ctx context.Context, tx *sql.Tx, plan *themeRestorePlan) (ThemeRestoreSummary, error) {
	result := ThemeRestoreSummary{Ignored: plan.ignored}
	ids, err := themeIDs(ctx, tx)
	if err != nil {
		return result, err
	}
	if plan.ids != nil && !slices.Equal(ids, plan.ids) {
		return result, errors.New("config themes changed during restore preflight")
	}
	// 配置已整表覆盖，目标原有内容不再具有对应关系；缺包时也不能沿用目标的旧文件或旧 zip。
	for _, table := range []string{"theme_file", "theme_package"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM main."+table); err != nil {
			return result, err
		}
	}
	for _, id := range ids {
		content, ok := plan.packages[id]
		if !ok {
			if _, err := tx.ExecContext(ctx, "UPDATE main.theme SET enabled = 0 WHERE id = ?", id); err != nil {
				return result, err
			}
			result.Missing = append(result.Missing, id)
			continue
		}
		// 复用上传的解析器，不复制路径、CRC 或展开预算的校验。预检保留的原包不受目录后续变化影响。
		pkg, err := theme.Parse(content)
		if err != nil {
			return result, err
		}
		files := make([]ThemeFile, len(pkg.Files))
		for i, f := range pkg.Files {
			files[i] = ThemeFile{Path: f.Path, Content: f.Content}
		}
		if err := putThemeContent(tx, id, files, content); err != nil {
			return result, err
		}
		result.Restored = append(result.Restored, id)
	}
	return result, nil
}
