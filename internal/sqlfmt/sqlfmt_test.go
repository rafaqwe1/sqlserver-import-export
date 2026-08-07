package sqlfmt

import (
	"bytes"
	"testing"
	"time"
)

func formatValue(val any, dbType string) string {
	var buf bytes.Buffer
	WriteValue(&buf, val, dbType)
	return buf.String()
}

func TestQuoteIdent(t *testing.T) {
	if got := QuoteIdent("Customers"); got != "[Customers]" {
		t.Errorf("got %q", got)
	}
	if got := QuoteIdent("weird]name"); got != "[weird]]name]" {
		t.Errorf("got %q", got)
	}
}

func TestQuoteString(t *testing.T) {
	if got := QuoteString("O'Brien"); got != "'O''Brien'" {
		t.Errorf("got %q", got)
	}
}

func TestFormatValue(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		name   string
		val    any
		dbType string
		want   string
	}{
		{"nil", nil, "INT", "NULL"},
		{"int", int64(42), "INT", "42"},
		{"bit true", true, "BIT", "1"},
		{"bit false", false, "BIT", "0"},
		{"decimal bytes", []byte("123.4500"), "DECIMAL", "123.4500"},
		{"varchar escapes quote", "it's", "VARCHAR", "'it''s'"},
		{"nvarchar unicode prefix", "héllo", "NVARCHAR", "N'héllo'"},
		{"date", time.Date(2024, 3, 5, 0, 0, 0, 0, loc), "DATE", "'2024-03-05'"},
		{"binary hex", []byte{0xDE, 0xAD, 0xBE, 0xEF}, "VARBINARY", "0xdeadbeef"},
		{"empty binary", []byte{}, "VARBINARY", "0x"},
		{"float", float64(1.5), "FLOAT", "1.5"},
		{
			"uniqueidentifier from raw bytes",
			[]byte{0xAA, 0xAA, 0xAA, 0xAA, 0xBB, 0xBB, 0xCC, 0xCC, 0xDD, 0xDD, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE},
			"UNIQUEIDENTIFIER",
			"'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatValue(c.val, c.dbType)
			if got != c.want {
				t.Errorf("formatValue(%v, %q) = %q, want %q", c.val, c.dbType, got, c.want)
			}
		})
	}
}

func TestFormatValue_DateTime(t *testing.T) {
	ts := time.Date(2024, 3, 5, 13, 45, 30, 0, time.UTC)
	got := formatValue(ts, "DATETIME2")
	want := "'2024-03-05T13:45:30'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
