package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Theme 是一个已安装主题的元数据；内容在 theme_file。
type Theme struct {
	ID      string
	Name    string
	Version string
	// Preview 是包内预览图路径，空串表示没有。
	Preview    string
	UploadedAt time.Time
	Enabled    bool
}

// ThemeFile 是主题包里的一个普通文件。
type ThemeFile struct {
	Path    string
	Content []byte
}

var ErrThemeLimit = errors.New("theme limit reached")

// PutTheme 把一个校验过的主题包整体写入：元数据与全部文件在同一个写事务里，提交前对任何读者不可见，失败则库里
// 什么都没变——托管读到的永远是某一个完整的包，不会是新旧文件的混合。t.ID 已存在即替换：删掉旧包的全部文件再写入
// 新的，启用状态沿用（更新启用中的主题不应让公开页回落）。不存在即新建（未启用），此时计数与插入在同一事务里，
// 并发上传不会都看到 limit−1 而一起越过上限；替换不计入上限。
// mustExist 为真时 t.ID 必须已安装，否则返回 ErrNotFound 且什么都不写：调用方声明这是一次更新（expect_id），
// 在它读到列表之后该主题被删掉时，静默装成一个新主题就是"更新"报告成功而实际做了另一件事。
// t.Enabled 被忽略，返回值里是写入后的实际状态。
func (s *Store) PutTheme(ctx context.Context, t Theme, files []ThemeFile, mustExist bool, limit int) (Theme, error) {
	out := t
	err := s.write(ctx, func(tx *sql.Tx) error {
		var enabled bool
		err := tx.QueryRow("SELECT enabled FROM theme WHERE id = ?", t.ID).Scan(&enabled)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if mustExist {
				return ErrNotFound
			}
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM theme").Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return ErrThemeLimit
			}
			if _, err := tx.Exec("INSERT INTO theme (id, name, version, preview, uploaded_at, enabled) VALUES (?, ?, ?, ?, ?, 0)",
				t.ID, t.Name, t.Version, t.Preview, t.UploadedAt.Unix()); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.Exec("DELETE FROM theme_file WHERE theme_id = ?", t.ID); err != nil {
				return err
			}
			if _, err := tx.Exec("UPDATE theme SET name = ?, version = ?, preview = ?, uploaded_at = ? WHERE id = ?",
				t.Name, t.Version, t.Preview, t.UploadedAt.Unix(), t.ID); err != nil {
				return err
			}
		}
		for _, f := range files {
			if _, err := tx.Exec("INSERT INTO theme_file (theme_id, path, content) VALUES (?, ?, ?)", t.ID, f.Path, f.Content); err != nil {
				return err
			}
		}
		out.Enabled = enabled
		out.UploadedAt = time.Unix(t.UploadedAt.Unix(), 0).UTC()
		return nil
	})
	if err != nil {
		return Theme{}, err
	}
	return out, nil
}

// ListThemes 按 id 升序列出已安装的主题。
func (s *Store) ListThemes(ctx context.Context) ([]Theme, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, name, version, preview, uploaded_at, enabled FROM theme ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Theme
	for rows.Next() {
		var t Theme
		var at int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Version, &t.Preview, &at, &t.Enabled); err != nil {
			return nil, err
		}
		t.UploadedAt = time.Unix(at, 0).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// EnableTheme 让 id 成为唯一启用的主题；id 不存在时返回 ErrNotFound，什么都不改。先清掉现有的启用行再置位，
// 顺序由 theme_enabled 索引要求：反过来会在同一事务里短暂出现两行 1 而被索引拒绝。
func (s *Store) EnableTheme(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		// 空 id 表示"不启用任何主题"（回到内置公开页），不是"匹配一切"：显式分支，不让它落进下面按 id 查找的路径
		// 变成 NotFound。
		if id == "" {
			_, err := tx.Exec("UPDATE theme SET enabled = 0 WHERE enabled = 1")
			return err
		}
		var exists bool
		if err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM theme WHERE id = ?)", id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if _, err := tx.Exec("UPDATE theme SET enabled = 0 WHERE enabled = 1 AND id <> ?", id); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE theme SET enabled = 1 WHERE id = ?", id)
		return err
	})
}

// DeleteTheme 删除主题及其全部文件；不存在时返回 ErrNotFound。删掉的若是启用中的主题，删除之后就没有启用行，
// 按 §10.1 即回落内置公开页——公开页是匿名入口，不因一次管理操作变成 404。
func (s *Store) DeleteTheme(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM theme WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec("DELETE FROM theme_file WHERE theme_id = ?", id)
		return err
	})
}

// ThemePreview 读出主题的元数据与预览图内容；主题不存在时返回 ErrNotFound，没有预览图时 content 为 nil。两次读在
// 同一个只读事务里：中途被替换的主题不会拿到新包的路径配旧包的内容。
func (s *Store) ThemePreview(ctx context.Context, id string) (Theme, []byte, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Theme{}, nil, err
	}
	defer tx.Rollback()
	var t Theme
	var at int64
	err = tx.QueryRowContext(ctx, "SELECT id, name, version, preview, uploaded_at, enabled FROM theme WHERE id = ?", id).
		Scan(&t.ID, &t.Name, &t.Version, &t.Preview, &at, &t.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Theme{}, nil, ErrNotFound
	}
	if err != nil {
		return Theme{}, nil, err
	}
	t.UploadedAt = time.Unix(at, 0).UTC()
	if t.Preview == "" {
		return t, nil, nil
	}
	var content []byte
	if err := tx.QueryRowContext(ctx, "SELECT content FROM theme_file WHERE theme_id = ? AND path = ?", id, t.Preview).Scan(&content); err != nil {
		return Theme{}, nil, err
	}
	return t, content, nil
}

// EnabledThemeFiles 在启用中的主题里按路径取文件内容，返回 path→content（没有的路径不在其中）与"是否有启用中的主题"。
// 全部路径在一条语句里读出：SQLite 的单条语句读的是同一个快照，所以返回的文件同属一个完整的包——托管在请求路径未命中时
// 回落 index.html，两者分两次读的话，中间换了启用主题或整包替换会拼出两个包的混合，删掉主题则让回落读空。
// 没有启用中的主题时 enabled 为假：调用方据此回落内置公开页，而不是把它当作"文件都不存在"。
func (s *Store) EnabledThemeFiles(ctx context.Context, paths []string) (files map[string][]byte, enabled bool, err error) {
	if len(paths) == 0 {
		return nil, false, errors.New("EnabledThemeFiles: no paths")
	}
	args := make([]any, len(paths))
	for i, p := range paths {
		args[i] = p
	}
	// LEFT JOIN 让启用中的主题至少产出一行（路径都没命中时 path 为 NULL），零行才表示没有启用中的主题。
	rows, err := s.r.QueryContext(ctx, "SELECT f.path, f.content FROM theme t LEFT JOIN theme_file f ON f.theme_id = t.id AND f.path IN (?"+
		strings.Repeat(", ?", len(paths)-1)+") WHERE t.enabled = 1", args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	files = map[string][]byte{}
	for rows.Next() {
		enabled = true
		var path sql.NullString
		var content []byte
		if err := rows.Scan(&path, &content); err != nil {
			return nil, false, err
		}
		if path.Valid {
			files[path.String] = content
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return files, enabled, nil
}
