// Package sqlfmt formats T-SQL identifiers and literal values.
package sqlfmt

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func QuoteIdent(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

func QuoteQualified(schema, name string) string {
	return QuoteIdent(schema) + "." + QuoteIdent(name)
}

func QuoteString(s string) string {
	var buf bytes.Buffer
	buf.Grow(len(s) + 2)
	writeQuoted(&buf, s)
	return buf.String()
}

const isoDateTime = "2006-01-02T15:04:05.9999999"

// Layouts for the literal forms WriteValue produces for date/time columns,
// exported so importrun's bulk-copy literal parser can stay in exact sync
// with the writer instead of duplicating format strings that could drift.
const (
	DateLayout           = "2006-01-02"
	TimeLayout           = "15:04:05.9999999"
	DateTimeLayout       = isoDateTime
	DateTimeOffsetLayout = isoDateTime + "Z07:00"
)

// WriteValue appends the T-SQL literal for one scanned column value to buf,
// dispatching on the driver-reported DatabaseTypeName. val is nil for SQL
// NULL. Writing directly to a reused buffer (instead of returning a string
// per call) is what keeps GB-scale exports off the allocator.
func WriteValue(buf *bytes.Buffer, val any, dbType string) {
	if val == nil {
		buf.WriteString("NULL")
		return
	}

	switch strings.ToUpper(dbType) {
	case "BIT":
		if b, ok := val.(bool); ok {
			if b {
				buf.WriteByte('1')
			} else {
				buf.WriteByte('0')
			}
			return
		}

	case "TINYINT", "SMALLINT", "INT", "BIGINT":
		if n, ok := val.(int64); ok {
			var tmp [20]byte
			buf.Write(strconv.AppendInt(tmp[:0], n, 10))
			return
		}

	case "DECIMAL", "MONEY", "SMALLMONEY":
		// go-mssqldb scans these as []byte holding the ASCII decimal string,
		// so it can be copied straight into the output with no conversion.
		if b, ok := val.([]byte); ok {
			if len(b) == 0 {
				buf.WriteByte('0')
			} else {
				buf.Write(b)
			}
			return
		}

	case "FLOAT", "REAL":
		if f, ok := val.(float64); ok {
			var tmp [32]byte
			buf.Write(strconv.AppendFloat(tmp[:0], f, 'G', -1, 64))
			return
		}

	case "CHAR", "VARCHAR", "TEXT":
		writeQuoted(buf, toStr(val))
		return

	case "NCHAR", "NVARCHAR", "NTEXT", "XML":
		buf.WriteByte('N')
		writeQuoted(buf, toStr(val))
		return

	case "UNIQUEIDENTIFIER":
		// Scanned as the raw 16-byte GUID, already reordered by the driver to
		// standard RFC4122 byte order, not a formatted string.
		if b, ok := val.([]byte); ok && len(b) == 16 {
			buf.WriteByte('\'')
			writeGUID(buf, b)
			buf.WriteByte('\'')
			return
		}
		writeQuoted(buf, toStr(val))
		return

	case "DATE":
		if t, ok := val.(time.Time); ok {
			writeQuoted(buf, t.Format(DateLayout))
			return
		}

	case "TIME":
		if t, ok := val.(time.Time); ok {
			writeQuoted(buf, t.Format(TimeLayout))
			return
		}

	case "DATETIME", "DATETIME2", "SMALLDATETIME":
		if t, ok := val.(time.Time); ok {
			writeQuoted(buf, t.Format(DateTimeLayout))
			return
		}

	case "DATETIMEOFFSET":
		if t, ok := val.(time.Time); ok {
			writeQuoted(buf, t.Format(DateTimeOffsetLayout))
			return
		}

	case "BINARY", "VARBINARY", "IMAGE":
		if b, ok := val.([]byte); ok {
			writeHexLiteral(buf, b)
			return
		}

	case "HIERARCHYID", "GEOGRAPHY", "GEOMETRY":
		// Best-effort: these CLR UDTs implement IBinarySerialize, so CONVERT
		// from their native varbinary serialization round-trips correctly.
		if b, ok := val.([]byte); ok {
			buf.WriteString("CONVERT(")
			buf.WriteString(strings.ToLower(dbType))
			buf.WriteString(", ")
			writeHexLiteral(buf, b)
			buf.WriteByte(')')
			return
		}

	case "SQL_VARIANT":
		writeVariant(buf, val)
		return
	}

	// Unknown/unsupported type or an unexpected Go value for the declared
	// type: best-effort string literal rather than failing the export.
	buf.WriteByte('N')
	writeQuoted(buf, toStr(val))
}

// writeQuoted appends 'escaped' around s, doubling any embedded quote, in a
// handful of WriteString calls rather than building an escaped copy of s.
func writeQuoted(buf *bytes.Buffer, s string) {
	buf.WriteByte('\'')
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			buf.WriteString(s[start : i+1])
			buf.WriteByte('\'')
			start = i + 1
		}
	}
	buf.WriteString(s[start:])
	buf.WriteByte('\'')
}

// writeGUID renders a 16-byte GUID (standard RFC4122 byte order) as
// "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE".
func writeGUID(buf *bytes.Buffer, b []byte) {
	var h [32]byte
	hex.Encode(h[:], b)
	buf.Write(h[0:8])
	buf.WriteByte('-')
	buf.Write(h[8:12])
	buf.WriteByte('-')
	buf.Write(h[12:16])
	buf.WriteByte('-')
	buf.Write(h[16:20])
	buf.WriteByte('-')
	buf.Write(h[20:32])
}

func writeHexLiteral(buf *bytes.Buffer, b []byte) {
	buf.WriteString("0x")
	if len(b) > 0 {
		enc := hex.NewEncoder(buf)
		enc.Write(b)
	}
}

func toStr(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// writeVariant makes a best-effort literal for a sql_variant value based on
// its underlying Go runtime type, since the driver does not expose the
// variant's original SQL Server type name.
func writeVariant(buf *bytes.Buffer, val any) {
	switch v := val.(type) {
	case int64:
		var tmp [20]byte
		buf.Write(strconv.AppendInt(tmp[:0], v, 10))
	case float64:
		var tmp [32]byte
		buf.Write(strconv.AppendFloat(tmp[:0], v, 'G', -1, 64))
	case bool:
		if v {
			buf.WriteByte('1')
		} else {
			buf.WriteByte('0')
		}
	case []byte:
		writeHexLiteral(buf, v)
	case time.Time:
		writeQuoted(buf, v.Format(isoDateTime))
	case string:
		buf.WriteByte('N')
		writeQuoted(buf, v)
	default:
		buf.WriteByte('N')
		writeQuoted(buf, fmt.Sprintf("%v", val))
	}
}
