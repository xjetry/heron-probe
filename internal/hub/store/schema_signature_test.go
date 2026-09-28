package store

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// describe 读取 SQLite 实际建成的结构，不比较建表 SQL：重建表与 ALTER TABLE 的文本可能不同。
// 列元数据防止漏列、默认值和主键次序漂移；table_list 的 wr/strict 防止漏掉存储模式和类型约束。
// 索引从 index_list 枚举，不能过滤 sqlite_：UNIQUE 自动索引和 WITHOUT ROWID 主键都由 SQLite 管理。
// index_xinfo 保留键列、辅助列、排序与校对规则，避免列名相同却访问路径或唯一性语义不同。
// 部分/表达式索引及触发器、视图的语义不能从列元数据还原，因此保留 SQL 原文（含字符串中的空白）。
// sqlite_sequence 等内部表也保留结构与存在性，但不比较数据：旧库中的序列值本就可能不同于空库。
func describe(t *testing.T, db *sql.DB) []string {
	t.Helper()
	queries := []struct {
		kind string
		sql  string
	}{
		{"object", `SELECT type, name, tbl_name AS owner FROM main.sqlite_schema`},
		{"table", `SELECT s.name AS table_name, p.type, p.ncol, p.wr, p.strict
			FROM main.sqlite_schema s JOIN pragma_table_list p ON p.name = s.name AND p.schema = 'main'
			WHERE s.type = 'table'`},
		{"column", `SELECT s.name AS table_name, p.cid, p.name, p.type, p."notnull", p.dflt_value, p.pk, p.hidden
			FROM main.sqlite_schema s JOIN pragma_table_xinfo(s.name, 'main') p WHERE s.type = 'table'`},
		{"index", `SELECT s.name AS table_name, i.name AS index_name, i."unique", i.origin,
			CASE i.origin WHEN 'u' THEN 'UNIQUE' WHEN 'pk' THEN 'PRIMARY KEY' ELSE 'CREATE INDEX' END AS constraint_kind,
			i.partial FROM main.sqlite_schema s JOIN pragma_index_list(s.name, 'main') i WHERE s.type = 'table'`},
		{"index_column", `SELECT s.name AS table_name, i.name AS index_name, x.seqno, x.cid, x.name, x.desc, x.coll, x.key
			FROM main.sqlite_schema s JOIN pragma_index_list(s.name, 'main') i
			JOIN pragma_index_xinfo(i.name, 'main') x WHERE s.type = 'table'`},
		{"index_sql", `SELECT s.name AS table_name, i.name AS index_name, d.sql
			FROM main.sqlite_schema s JOIN pragma_index_list(s.name, 'main') i
			JOIN main.sqlite_schema d ON d.type = 'index' AND d.name = i.name
			WHERE s.type = 'table' AND (i.partial = 1 OR EXISTS
				(SELECT 1 FROM pragma_index_xinfo(i.name, 'main') x WHERE x.cid = -2))`},
		{"definition", `SELECT type, name, tbl_name AS owner, sql FROM main.sqlite_schema WHERE type IN ('trigger', 'view')`},
	}
	var signature []string
	for _, query := range queries {
		func() {
			rows, err := db.Query(query.sql)
			if err != nil {
				t.Fatalf("schema %s: %v", query.kind, err)
			}
			defer rows.Close()
			columns, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				values := make([]sql.NullString, len(columns))
				args := make([]any, len(columns))
				for i := range values {
					args[i] = &values[i]
				}
				if err := rows.Scan(args...); err != nil {
					t.Fatalf("schema %s: %v", query.kind, err)
				}
				fields := []string{query.kind}
				for i, value := range values {
					text := "NULL"
					if value.Valid {
						text = strconv.Quote(value.String)
					}
					fields = append(fields, columns[i]+"="+text)
				}
				signature = append(signature, strings.Join(fields, " "))
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("schema %s: %v", query.kind, err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}()
	}
	// 对象创建顺序与 index_list.seq 不属于结构；列序与 index_xinfo.seqno 则保留在每一项中。
	slices.Sort(signature)
	return signature
}

func schemaDifference(left, right []string, leftLabel, rightLabel string) string {
	var differences []string
	for _, side := range []struct {
		own, other []string
		label      string
	}{{left, right, leftLabel}, {right, left, rightLabel}} {
		for _, item := range side.own {
			if _, exists := slices.BinarySearch(side.other, item); !exists {
				differences = append(differences, fmt.Sprintf("%s有: %s", side.label, item))
			}
		}
	}
	return strings.Join(differences, "\n")
}

func TestSchemaSignatureDistinguishesStructure(t *testing.T) {
	const table = `CREATE TABLE sample (id INTEGER NOT NULL PRIMARY KEY, value TEXT)`
	for _, tc := range []struct {
		name        string
		left, right string
		want        string
	}{
		{"table_presence", table, table + `; CREATE TABLE extra (id INTEGER)`, `table_name="extra"`},
		{"column_name", table, `CREATE TABLE sample (id INTEGER NOT NULL PRIMARY KEY, other TEXT)`, `name="other"`},
		{"column_type", table, `CREATE TABLE sample (id INTEGER NOT NULL PRIMARY KEY, value BLOB)`, `type="BLOB"`},
		{"not_null", table, `CREATE TABLE sample (id INTEGER NOT NULL PRIMARY KEY, value TEXT NOT NULL)`, `notnull="1"`},
		{"default", table, `CREATE TABLE sample (id INTEGER NOT NULL PRIMARY KEY, value TEXT DEFAULT '')`, `dflt_value="''"`},
		{"null_default", table, `CREATE TABLE sample (id INTEGER NOT NULL PRIMARY KEY, value TEXT DEFAULT NULL)`, `dflt_value="NULL"`},
		{"primary_key_order", `CREATE TABLE sample (a TEXT, b TEXT, PRIMARY KEY (a, b))`, `CREATE TABLE sample (a TEXT, b TEXT, PRIMARY KEY (b, a))`, `pk="2"`},
		{"without_rowid", table, table + ` WITHOUT ROWID`, `wr="1"`},
		{"strict", table, table + ` STRICT`, `strict="1"`},
		{"unique_constraint", table, `CREATE TABLE sample (id INTEGER NOT NULL PRIMARY KEY, value TEXT UNIQUE)`, `index_name="sqlite_autoindex_sample_1" unique="1" origin="u" constraint_kind="UNIQUE"`},
		{"extra_index", table, table + `; CREATE INDEX by_value ON sample(value)`, `index_name="by_value"`},
		{"index_unique", table + `; CREATE INDEX by_value ON sample(value)`, table + `; CREATE UNIQUE INDEX by_value ON sample(value)`, `unique="1"`},
		{"index_column_order", table + `; CREATE INDEX by_value ON sample(id, value)`, table + `; CREATE INDEX by_value ON sample(value, id)`, `seqno="0" cid="1"`},
		{"index_descending", table + `; CREATE INDEX by_value ON sample(value)`, table + `; CREATE INDEX by_value ON sample(value DESC)`, `desc="1"`},
		{"index_collation", table + `; CREATE INDEX by_value ON sample(value)`, table + `; CREATE INDEX by_value ON sample(value COLLATE NOCASE)`, `coll="NOCASE"`},
		{"partial_index", table + `; CREATE INDEX by_value ON sample(value)`, table + `; CREATE INDEX by_value ON sample(value) WHERE id > 0`, `partial="1"`},
		{"partial_predicate", table + `; CREATE INDEX by_value ON sample(value) WHERE id > 0`, table + `; CREATE INDEX by_value ON sample(value) WHERE id > 1`, `WHERE id > 1`},
		{"index_expression", table + `; CREATE INDEX by_value ON sample(lower(value))`, table + `; CREATE INDEX by_value ON sample(upper(value))`, `upper(value)`},
		{"predicate_literal_spaces", table + `; CREATE INDEX by_value ON sample(value) WHERE value = 'a b'`, table + `; CREATE INDEX by_value ON sample(value) WHERE value = 'a  b'`, `'a  b'`},
		{"internal_table", `CREATE TABLE sample (id INTEGER PRIMARY KEY)`, `CREATE TABLE sample (id INTEGER PRIMARY KEY AUTOINCREMENT)`, `table_name="sqlite_sequence"`},
		{"trigger_presence", table, table + `; CREATE TRIGGER changed AFTER INSERT ON sample BEGIN UPDATE sample SET value = 'a'; END`, `type="trigger" name="changed"`},
		{"trigger_body", table + `; CREATE TRIGGER changed AFTER INSERT ON sample BEGIN UPDATE sample SET value = 'a'; END`, table + `; CREATE TRIGGER changed AFTER INSERT ON sample BEGIN UPDATE sample SET value = 'b'; END`, `SET value = 'b'`},
		{"view_presence", table, table + `; CREATE VIEW visible AS SELECT id FROM sample`, `type="view" name="visible"`},
		{"view_body", table + `; CREATE VIEW visible AS SELECT id FROM sample`, table + `; CREATE VIEW visible AS SELECT value FROM sample`, `SELECT value FROM sample`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, left := schemaPolicyFixture(t, []string{tc.left}, 0)
			_, right := schemaPolicyFixture(t, []string{tc.right}, 0)
			// want 取自右侧夹具独有的项。两种参数顺序各比一次，标签跟着夹具走而不跟参数位置走：
			// 右侧独有的项先作第二个参数、再作第一个参数的独有项，两次都必须以"右有"报出。
			for _, order := range []struct {
				first, second           *sql.DB
				firstLabel, secondLabel string
			}{{left, right, "左", "右"}, {right, left, "右", "左"}} {
				diff := schemaDifference(describe(t, order.first), describe(t, order.second), order.firstLabel, order.secondLabel)
				found := false
				for _, line := range strings.Split(diff, "\n") {
					if strings.HasPrefix(line, "右有: ") && strings.Contains(line, tc.want) {
						found = true
					}
				}
				if !found {
					t.Errorf("schema difference with %s first must report 右有 %q:\n%s", order.firstLabel, tc.want, diff)
				}
			}
		})
	}
}

func TestSchemaSignatureIgnoresCreationOrderAndData(t *testing.T) {
	_, left := schemaPolicyFixture(t, []string{
		`CREATE TABLE sample (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT UNIQUE)`,
		`CREATE INDEX by_value ON sample(value)`,
		`CREATE INDEX by_id ON sample(id)`,
		`INSERT INTO sample(value) VALUES ('old')`,
	}, 0)
	_, right := schemaPolicyFixture(t, []string{
		`CREATE TABLE sample (
			-- 空白和注释不是表结构，字符串常量中的空白则不能被折叠。
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			value TEXT UNIQUE
		)`,
		`CREATE INDEX by_id ON sample (id)`,
		`CREATE INDEX by_value ON sample (value)`,
	}, 0)
	if diff := schemaDifference(describe(t, left), describe(t, right), "迁移后", "新建"); diff != "" {
		t.Fatalf("equivalent schemas differ:\n%s", diff)
	}
}
