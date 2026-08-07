package importrun

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/rafaqwe1/sqlserver-import-export/internal/sqlfmt"
)

// TestParseLiteral_RoundTripsWriteValue feeds every value sqlfmt.WriteValue
// can emit for a bulk-copy-supported column type back through parseLiteral
// and checks the original Go value comes back. This is the load-bearing test
// for the "never guess" design: parseLiteral must invert WriteValue exactly
// for every type it claims to support.
func TestParseLiteral_RoundTripsWriteValue(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name    string
		val     any
		dbType  string
		colType string
		want    any
	}{
		{"tinyint", int64(255), "TINYINT", "tinyint", int64(255)},
		{"smallint negative", int64(-32768), "SMALLINT", "smallint", int64(-32768)},
		{"int", int64(2147483647), "INT", "int", int64(2147483647)},
		{"bigint", int64(9223372036854775807), "BIGINT", "bigint", int64(9223372036854775807)},
		{"bit true", true, "BIT", "bit", true},
		{"bit false", false, "BIT", "bit", false},
		{"float", float64(3.14159265), "FLOAT", "float", float64(3.14159265)},
		{"real negative", float64(-2.5), "REAL", "real", float64(-2.5)},
		{"decimal", []byte("12345.6789"), "DECIMAL", "decimal", "12345.6789"},
		{"decimal negative", []byte("-5.00"), "DECIMAL", "numeric", "-5.00"},
		{"money", []byte("19999.99"), "MONEY", "money", "19999.99"},
		{"varchar", "O'Brien's \"quoted\"", "VARCHAR", "varchar", "O'Brien's \"quoted\""},
		{"varchar empty", "", "VARCHAR", "varchar", ""},
		{"nvarchar unicode", "wörld's 世界", "NVARCHAR", "nvarchar", "wörld's 世界"},
		{"date", time.Date(2024, 3, 5, 0, 0, 0, 0, utc), "DATE", "date", time.Date(2024, 3, 5, 0, 0, 0, 0, utc)},
		{"time", time.Date(0, 1, 1, 13, 45, 30, 123456700, utc), "TIME", "time", time.Date(0, 1, 1, 13, 45, 30, 123456700, utc)},
		{"datetime2", time.Date(2024, 3, 5, 13, 45, 30, 123456700, utc), "DATETIME2", "datetime2", time.Date(2024, 3, 5, 13, 45, 30, 123456700, utc)},
		{"datetime whole second", time.Date(2024, 3, 5, 12, 0, 0, 0, utc), "DATETIME", "datetime", time.Date(2024, 3, 5, 12, 0, 0, 0, utc)},
		{"binary", []byte{0x01, 0x02, 0xFF, 0x00}, "VARBINARY", "varbinary", []byte{0x01, 0x02, 0xFF, 0x00}},
		{"binary empty", []byte{}, "VARBINARY", "varbinary", []byte{}},
		{"null int", nil, "INT", "int", nil},
		{"null varchar", nil, "VARCHAR", "varchar", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			sqlfmt.WriteValue(&buf, c.val, c.dbType)
			lit := buf.String()

			got, ok := parseLiteral(lit, c.colType)
			if !ok {
				t.Fatalf("parseLiteral(%q, %q) failed to parse", lit, c.colType)
			}

			if tv, isTime := c.want.(time.Time); isTime {
				gt, isGotTime := got.(time.Time)
				if !isGotTime || !gt.Equal(tv) {
					t.Errorf("parseLiteral(%q, %q) = %v, want %v", lit, c.colType, got, tv)
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseLiteral(%q, %q) = %#v, want %#v", lit, c.colType, got, c.want)
			}
		})
	}
}

// TestParseLiteral_UnsupportedTypesNeverGuess locks in that types the bulk
// path intentionally doesn't handle always fail to parse, regardless of how
// plausible the literal looks — this is the safety gate that forces a
// fallback to plain ExecContext instead of risking a wrong value.
func TestParseLiteral_UnsupportedTypesNeverGuess(t *testing.T) {
	cases := []struct {
		lit     string
		colType string
	}{
		{"'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'", "uniqueidentifier"},
		{"42", "sql_variant"},
		{"CONVERT(geography, 0xDEADBEEF)", "geography"},
		{"CONVERT(hierarchyid, 0x58)", "hierarchyid"},
		{"'text'", "unknown_type"},
		// Fixed-length types: bulk-copy requires the value to be exactly the
		// declared length, which a round-tripped literal isn't guaranteed to
		// be (see the "invalid column length from the bcp client" bug this
		// locks in the fix for). Always fall back to plain SQL text instead.
		{"'abc'", "char"},
		{"N'abc'", "nchar"},
		{"0x0102", "binary"},
	}
	for _, c := range cases {
		if _, ok := parseLiteral(c.lit, c.colType); ok {
			t.Errorf("parseLiteral(%q, %q) should not have succeeded", c.lit, c.colType)
		}
	}
}

func TestParseInsertBatch(t *testing.T) {
	batch := "INSERT INTO [dbo].[Customers] ([ID], [Name]) VALUES\n(1, N'Alice'),\n(2, N'O''Brien');"
	table, columns, rowsBlob, ok := parseInsertBatch(batch)
	if !ok {
		t.Fatal("expected parseInsertBatch to succeed")
	}
	if table != "[dbo].[Customers]" {
		t.Errorf("table = %q", table)
	}
	if !reflect.DeepEqual(columns, []string{"ID", "Name"}) {
		t.Errorf("columns = %#v", columns)
	}
	wantRows := "(1, N'Alice'),\n(2, N'O''Brien')"
	if rowsBlob != wantRows {
		t.Errorf("rowsBlob = %q, want %q", rowsBlob, wantRows)
	}
}

func TestParseInsertBatch_RejectsNonInsertBatches(t *testing.T) {
	nonInserts := []string{
		"SET IDENTITY_INSERT [dbo].[Customers] ON",
		"BEGIN TRANSACTION;",
		"COMMIT TRANSACTION;",
		"ALTER TABLE [dbo].[Orders] NOCHECK CONSTRAINT ALL;",
		"-- TABLE: dbo.Customers (~3 rows)",
		"CREATE TABLE [dbo].[Customers] ([ID] int NOT NULL);",
	}
	for _, batch := range nonInserts {
		if _, _, _, ok := parseInsertBatch(batch); ok {
			t.Errorf("parseInsertBatch(%q) should not have matched", batch)
		}
	}
}

func TestSplitTopLevel(t *testing.T) {
	got := splitTopLevel(`1, N'a, b', CONVERT(geography, 0x01)`, ',')
	want := []string{"1", " N'a, b'", " CONVERT(geography, 0x01)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestSplitTopLevel_EscapedQuoteInsideValue(t *testing.T) {
	got := splitTopLevel(`N'it''s, complicated', 2`, ',')
	want := []string{"N'it''s, complicated'", " 2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestParseRows(t *testing.T) {
	colTypes := map[string]string{"ID": "int", "Name": "varchar"}
	rows, ok := parseRows("(1, 'Alice'),\n(2, 'O''Brien')", []string{"ID", "Name"}, colTypes)
	if !ok {
		t.Fatal("expected parseRows to succeed")
	}
	want := [][]any{
		{int64(1), "Alice"},
		{int64(2), "O'Brien"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("got %#v, want %#v", rows, want)
	}
}

func TestParseRows_UnknownColumnTypeFallsBack(t *testing.T) {
	colTypes := map[string]string{"ID": "int"} // "Guid" missing on purpose
	_, ok := parseRows("(1, 'x')", []string{"ID", "Guid"}, colTypes)
	if ok {
		t.Error("expected parseRows to fail when a column's type is unknown")
	}
}

func TestSplitQualifiedIdent_RoundTripsQuoteQualified(t *testing.T) {
	cases := []struct{ schema, table string }{
		{"dbo", "Customers"},
		{"my schema", "weird]table"},
	}
	for _, c := range cases {
		q := sqlfmt.QuoteQualified(c.schema, c.table)
		gotSchema, gotTable, ok := splitQualifiedIdent(q)
		if !ok {
			t.Fatalf("splitQualifiedIdent(%q) failed", q)
		}
		if gotSchema != c.schema || gotTable != c.table {
			t.Errorf("splitQualifiedIdent(%q) = (%q, %q), want (%q, %q)", q, gotSchema, gotTable, c.schema, c.table)
		}
	}
}
