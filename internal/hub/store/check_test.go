package store

import (
	"database/sql"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// CHECK 约束承载的不变式只能由写入来钉：describe 比的是列、默认值、主键与索引，不读 CHECK，两份 DDL（schema.go 与
// 冻结的迁移）同删或只删一份，它都照样判两库相等。这里在新建库与从 v1 迁到当前的库上，对每条 CHECK 各做一次违反它的
// 写入并断言被拒；带 CHECK 的表从新建库的 sqlite_master 数出，与 checkViolations 对照，新加的 CHECK 没有对应的
// 违反写入即失败。
func TestEveryCheckConstraintRefusesItsViolation(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV1, 1, seedMinuteRow)
	checkViolations := map[string]string{
		// 单行表：第二行插不进去。
		"admin":           "INSERT INTO admin (id, password_hash, updated_at) VALUES (2, 'x', 0)",
		"register_window": "INSERT INTO register_window (id, key_hash, expires_at, remaining) VALUES (2, x'00', 0, 0)",
		"probe_meta":      "INSERT INTO probe_meta (id, version) VALUES (2, 0)",
		// 0 与 1 以外的启用值会绕过只看 enabled = 1 的 theme_enabled 索引。
		"theme": "INSERT INTO theme (id, name, version, preview, uploaded_at, enabled) VALUES ('x', 'X', '1', '', 0, 2)",
	}
	if got, want := tablesWithCheck(t, fresh.r), slices.Sorted(maps.Keys(checkViolations)); !slices.Equal(got, want) {
		t.Fatalf("tables with a CHECK constraint = %v, violations cover %v; every CHECK needs a write that violates it here", got, want)
	}
	for _, s := range []*Store{fresh, migrated} {
		kind := map[bool]string{true: "migrated", false: "fresh"}[s == migrated]
		for _, table := range slices.Sorted(maps.Keys(checkViolations)) {
			err := s.write(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.Exec(checkViolations[table])
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Errorf("%s on a %s database: %v, want the CHECK on %s to refuse it", checkViolations[table], kind, err, table)
			}
		}
	}
}

var (
	sqlLineComment = regexp.MustCompile(`--[^\n]*`)
	sqlCheck       = regexp.MustCompile(`(?i)\bcheck\s*\(`)
)

// tablesWithCheck 是 DDL 里带 CHECK 约束的表，按名字排序。先去掉行注释：DDL 的注释里也会写到 CHECK 这个词。
func tablesWithCheck(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		if sqlCheck.MatchString(sqlLineComment.ReplaceAllString(ddl, "")) {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}
