package meta

import (
	"fmt"
	"strings"
)

// FormatColumnType renders the SQL Server type syntax for a column
// declaration, e.g. "varchar(50)", "decimal(10,2)", "datetime2(7)".
// It does not include COLLATE, NULL/NOT NULL, IDENTITY or defaults.
func FormatColumnType(c Column) string {
	t := strings.ToLower(c.TypeName)
	switch t {
	case "varchar", "char", "varbinary", "binary":
		if c.MaxLength == -1 {
			return fmt.Sprintf("%s(MAX)", t)
		}
		return fmt.Sprintf("%s(%d)", t, c.MaxLength)
	case "nvarchar", "nchar":
		if c.MaxLength == -1 {
			return fmt.Sprintf("%s(MAX)", t)
		}
		return fmt.Sprintf("%s(%d)", t, c.MaxLength/2)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", t, c.Precision, c.Scale)
	case "datetime2", "datetimeoffset", "time":
		return fmt.Sprintf("%s(%d)", t, c.Scale)
	default:
		return t
	}
}

// IsCharType reports whether COLLATE is meaningful for this column's type.
func IsCharType(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "char", "varchar", "text", "nchar", "nvarchar", "ntext":
		return true
	}
	return false
}
