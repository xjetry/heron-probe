package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"time"
)

// Theme 是一个已安装主题的元数据；展开内容在 theme_file，原始 zip 在 theme_package。
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
var ErrThemeContentMissing = errors.New("theme has no files; upload the original package again")

// 托管内容存在性只看展开文件，不以原包或启用位代替；恢复允许保留没有内容的元数据行。
const themeHasContent = "EXISTS (SELECT 1 FROM theme_file WHERE theme_id = theme.id)"

// writeTheme 是主题元数据与内容在运行期的写入口：PutTheme、EnableTheme、DeleteTheme 都经它，事务提交成功之后
// 递增 themeGen。write 返回 nil 即已提交、返回错误即未应用（store 包注释的不变式），所以代数只随真正落库的改动前进；
// 不改变启用中主题的提交（装一个未启用的主题、重复停用）也递增，代价是托管多读一次库，换来的是判定只有"提交了"一条。
// 影响托管内容的写者（theme 与 theme_file）的其余入口都不在运行期：迁移在 Open 返回之前完成，恢复要求 hub 停机。
// theme_package 的 uploaded/revision 只描述备份原包，不参与托管读取，标记更新不推进代数。新增托管内容写者必须经这里，
// 否则它的提交不让代数前进，托管会一直服务改动之前的快照，直到下一次经这里的提交。
func (s *Store) writeTheme(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := s.write(ctx, fn); err != nil {
		return err
	}
	s.themeGen.Add(1)
	return nil
}

// ThemeGeneration 是启用中主题内容的代数：只增不减，启用中主题的内容每变一次它至少增一。只读一个原子量，不碰库。
func (s *Store) ThemeGeneration() uint64 { return s.themeGen.Load() }

// PutTheme 把一个校验过的主题包整体写入：元数据、原始 zip 与全部文件在同一个写事务里，提交前对任何读者不可见，失败则库里
// 什么都没变——托管读到的永远是某一个完整的包，不会是新旧文件的混合。t.ID 已存在即替换：删掉旧包的全部文件再写入
// 新的，启用状态沿用（更新启用中的主题不应让公开页回落）。不存在即新建（未启用），此时计数与插入在同一事务里，
// 并发上传不会都看到 limit−1 而一起越过上限；替换不计入上限。
// mustExist 为真时 t.ID 必须已安装，否则返回 ErrNotFound 且什么都不写：调用方声明这是一次更新（expect_id），
// 在它读到列表之后该主题被删掉时，静默装成一个新主题就是"更新"报告成功而实际做了另一件事。
// t.Enabled 被忽略，返回值里是写入后的实际状态。
func (s *Store) PutTheme(ctx context.Context, t Theme, files []ThemeFile, content []byte, mustExist bool, limit int) (Theme, error) {
	out := t
	err := s.writeTheme(ctx, func(tx *sql.Tx) error {
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
			if _, err := tx.Exec("UPDATE theme SET name = ?, version = ?, preview = ?, uploaded_at = ? WHERE id = ?",
				t.Name, t.Version, t.Preview, t.UploadedAt.Unix(), t.ID); err != nil {
				return err
			}
		}
		if err := putThemeContent(tx, t.ID, files, content); err != nil {
			return err
		}
		out.Enabled = enabled
		out.UploadedAt = time.Unix(t.UploadedAt.Unix(), 0).UTC()
		return nil
	})
	if err != nil {
		return Theme{}, err
	}
	s.wakeThemeBackup()
	return out, nil
}

// 在线上传与离线恢复共用原包和文件的写入；调用方的事务同时包含元数据，失败不能留下半个包。
func putThemeContent(tx *sql.Tx, id string, files []ThemeFile, content []byte) error {
	if len(content) == 0 {
		return errors.New("theme package is empty")
	}
	var revision int64
	for revision == 0 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		revision = int64(binary.LittleEndian.Uint64(b[:]) & (1<<63 - 1))
	}
	if _, err := tx.Exec("DELETE FROM theme_file WHERE theme_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO theme_package (theme_id, content, revision, uploaded) VALUES (?, ?, ?, 0)
		ON CONFLICT (theme_id) DO UPDATE SET content = excluded.content, revision = excluded.revision, uploaded = 0`, id, content, revision); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := tx.Exec("INSERT INTO theme_file (theme_id, path, content) VALUES (?, ?, ?)", id, f.Path, f.Content); err != nil {
			return err
		}
	}
	return nil
}

// ThemeChanges 由一个备份管理器消费；通知合并而不排队，持久化 uploaded 与周期同步兜住丢失的通知。
func (s *Store) ThemeChanges() <-chan struct{} { return s.themeChanges }

func (s *Store) wakeThemeBackup() {
	select {
	case s.themeChanges <- struct{}{}:
	default:
	}
}

type ThemeBackup struct {
	Revision int64
	Content  []byte
	Uploaded bool
}

type ThemeBackupEntry struct {
	ID                               string
	Revision                         int64
	Uploaded, HasPackage, HasContent bool
}

// 同步清单的一条语句固定元数据与原包标识的同一快照；后续内容读取只能取这一份写入。
func (s *Store) ThemeBackupEntries(ctx context.Context) ([]ThemeBackupEntry, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT theme.id, COALESCE(p.revision,0), COALESCE(p.uploaded,0), p.theme_id IS NOT NULL, "+themeHasContent+" FROM theme LEFT JOIN theme_package p ON p.theme_id=theme.id ORDER BY theme.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThemeBackupEntry
	for rows.Next() {
		var e ThemeBackupEntry
		if err := rows.Scan(&e.ID, &e.Revision, &e.Uploaded, &e.HasPackage, &e.HasContent); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ThemeBackupContent(ctx context.Context, id string, revision int64) ([]byte, error) {
	var content []byte
	err := s.r.QueryRowContext(ctx, "SELECT content FROM theme_package WHERE theme_id=? AND revision=?", id, revision).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return content, err
}

func (s *Store) ThemesWithoutPackage(ctx context.Context) ([]string, error) {
	entries, err := s.ThemeBackupEntries(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.HasPackage {
			ids = append(ids, e.ID)
		}
	}
	return ids, nil
}

// ThemeBackupPackage 用一条语句读取同一次写入的标识与原包。旧库可能只有展开文件，缺包显式报错而不重打包。
func (s *Store) ThemeBackupPackage(ctx context.Context, id string) (ThemeBackup, error) {
	var p ThemeBackup
	err := s.r.QueryRowContext(ctx, "SELECT revision, content, uploaded FROM theme_package WHERE theme_id = ?", id).Scan(&p.Revision, &p.Content, &p.Uploaded)
	if errors.Is(err, sql.ErrNoRows) {
		return ThemeBackup{}, ErrNotFound
	}
	return p, err
}

// MarkThemeUploaded 只确认上传时读到的写入标识；期间的覆盖或重装保留自己的待上传状态。
func (s *Store) MarkThemeUploaded(ctx context.Context, id string, revision int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE theme_package SET uploaded = 1 WHERE theme_id = ? AND revision = ?", id, revision)
		return err
	})
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
	return s.writeTheme(ctx, func(tx *sql.Tx) error {
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
		var hasContent bool
		if err := tx.QueryRow("SELECT "+themeHasContent+" FROM theme WHERE id=?", id).Scan(&hasContent); err != nil {
			return err
		}
		if !hasContent {
			return ErrThemeContentMissing
		}
		if _, err := tx.Exec("UPDATE theme SET enabled = 0 WHERE enabled = 1 AND id <> ?", id); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE theme SET enabled = 1 WHERE id = ?", id)
		return err
	})
}

// DeleteTheme 删除主题、原始 zip 及其全部文件；不存在时返回 ErrNotFound。删掉的若是启用中的主题，删除之后就没有启用行，
// 按 §10.1 即回落内置公开页——公开页是匿名入口，不因一次管理操作变成 404。
func (s *Store) DeleteTheme(ctx context.Context, id string) error {
	err := s.writeTheme(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM theme WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec("DELETE FROM theme_package WHERE theme_id = ?", id); err != nil {
			return err
		}
		_, err = tx.Exec("DELETE FROM theme_file WHERE theme_id = ?", id)
		return err
	})
	if err == nil {
		s.wakeThemeBackup()
	}
	return err
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

// EnabledThemePackage 读出启用中主题的整包（path → content）与读之前取到的代数；没有启用中的主题时 enabled 为假，
// 调用方据此回落内置公开页，而不是把它当作"包里没有文件"。
//
// 整包在一条语句里读出：SQLite 的一条语句在一个隐式读事务里执行，从第一行到最后一行看到的是同一个快照，所以返回的
// 文件同属一个完整的包。拆成两次读就没有这一条：把 index.html 与其余文件分开读时，两次之间提交的替换或切换让两部分
// 出自两个包；先查启用的是哪个主题再读它的文件时，两次之间提交的删除让一个启用中的主题读出来是空包。
//
// 代数必须在语句之前读：读到 g 时，把代数递增到 g 的那些提交都已完成（writeTheme 先提交后递增），随后开始的语句看得到
// 它们，所以返回的内容至少与 g 一样新——比 g 新也无妨，调用方只会在下一次比较时多读一次。先读库后取代数则不然：
// 语句开始之后、取代数之前提交并递增的写，会让语句读到的旧内容标上新代数，调用方此后比较代数都相等，一直服务旧包，
// 直到下一次写。
func (s *Store) EnabledThemePackage(ctx context.Context) (gen uint64, files map[string][]byte, enabled bool, err error) {
	gen = s.themeGen.Load()
	// LEFT JOIN 让启用中的主题至少产出一行（包里没有文件时 path 为 NULL），零行才表示没有启用中的主题。
	rows, err := s.r.QueryContext(ctx, "SELECT f.path, f.content FROM theme t LEFT JOIN theme_file f ON f.theme_id = t.id WHERE t.enabled = 1")
	if err != nil {
		return 0, nil, false, err
	}
	defer rows.Close()
	files = map[string][]byte{}
	for rows.Next() {
		enabled = true
		var path sql.NullString
		var content []byte
		if err := rows.Scan(&path, &content); err != nil {
			return 0, nil, false, err
		}
		if path.Valid {
			files[path.String] = content
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, false, err
	}
	return gen, files, enabled, nil
}
