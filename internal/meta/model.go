// Package meta reads SQL Server table structure from the sys.* catalog views.
package meta

import "strings"

// TableRef identifies a table by schema, name and catalog object_id.
type TableRef struct {
	ObjectID int64
	Schema   string
	Name     string
}

func (t TableRef) QualifiedName() string {
	return t.Schema + "." + t.Name
}

// Column describes one column of a table, as read from sys.columns/sys.types
// plus sys.identity_columns, sys.computed_columns and sys.default_constraints.
type Column struct {
	ColumnID              int
	Name                  string
	TypeName              string // base system type name, e.g. "varchar", "decimal"
	MaxLength             int16  // sys.columns.max_length; -1 means MAX
	Precision             uint8
	Scale                 uint8
	CollationName         string
	IsNullable            bool
	IsIdentity            bool
	IdentitySeed          int64
	IdentityIncrement     int64
	IsComputed            bool
	ComputedDefinition    string
	IsPersisted           bool
	DefaultConstraintName string
	DefaultDefinition     string
}

// IsRowVersion reports whether the column is a timestamp/rowversion column,
// which SQL Server populates automatically and cannot be targeted by INSERT.
func (c Column) IsRowVersion() bool {
	t := strings.ToLower(c.TypeName)
	return t == "timestamp" || t == "rowversion"
}

// IndexColumn is one column participating in an index/constraint.
type IndexColumn struct {
	ColumnName   string
	IsDescending bool
	IsIncluded   bool
}

// Index describes a row of sys.indexes (primary key, unique constraint, or a
// plain index), together with its columns from sys.index_columns.
type Index struct {
	Name               string
	IsPrimaryKey       bool
	IsUniqueConstraint bool
	IsUnique           bool
	IsClustered        bool
	FilterDefinition   string
	Columns            []IndexColumn
}

// KeyColumns returns the non-included (key) columns, in key order.
func (ix Index) KeyColumns() []IndexColumn {
	var out []IndexColumn
	for _, c := range ix.Columns {
		if !c.IsIncluded {
			out = append(out, c)
		}
	}
	return out
}

// IncludedColumns returns the INCLUDE-only columns.
func (ix Index) IncludedColumns() []IndexColumn {
	var out []IndexColumn
	for _, c := range ix.Columns {
		if c.IsIncluded {
			out = append(out, c)
		}
	}
	return out
}

// ForeignKeyColumn pairs a parent (this table's) column with the referenced column.
type ForeignKeyColumn struct {
	ParentColumn string
	RefColumn    string
}

// ForeignKey describes a row of sys.foreign_keys with its columns.
type ForeignKey struct {
	Name         string
	RefSchema    string
	RefTable     string
	Columns      []ForeignKeyColumn
	DeleteAction string // NO_ACTION, CASCADE, SET_NULL, SET_DEFAULT
	UpdateAction string
}

// CheckConstraint describes a row of sys.check_constraints.
type CheckConstraint struct {
	Name       string
	Definition string
}

// Table is the full structure of one user table.
type Table struct {
	ObjectID         int64
	Schema           string
	Name             string
	Columns          []Column
	Indexes          []Index // includes the primary key and unique constraints
	ForeignKeys      []ForeignKey
	CheckConstraints []CheckConstraint
}

func (t *Table) QualifiedName() string {
	return t.Schema + "." + t.Name
}

func (t *Table) HasIdentity() bool {
	for _, c := range t.Columns {
		if c.IsIdentity {
			return true
		}
	}
	return false
}

func (t *Table) PrimaryKey() *Index {
	for i := range t.Indexes {
		if t.Indexes[i].IsPrimaryKey {
			return &t.Indexes[i]
		}
	}
	return nil
}

// InsertableColumns returns columns that can appear in an INSERT column list:
// not computed, not rowversion/timestamp.
func (t *Table) InsertableColumns() []Column {
	var out []Column
	for _, c := range t.Columns {
		if c.IsComputed || c.IsRowVersion() {
			continue
		}
		out = append(out, c)
	}
	return out
}
