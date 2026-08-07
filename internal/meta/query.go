package meta

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// idChunkSize bounds how many object_ids go into a single IN(...) list, so
// schemas with thousands of tables don't produce one unmanageably large query.
const idChunkSize = 500

func ListTables(ctx context.Context, db *sql.DB) ([]TableRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.object_id, s.name, t.name
		FROM sys.tables t
		JOIN sys.schemas s ON t.schema_id = s.schema_id
		ORDER BY s.name, t.name`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	var out []TableRef
	for rows.Next() {
		var ref TableRef
		if err := rows.Scan(&ref.ObjectID, &ref.Schema, &ref.Name); err != nil {
			return nil, fmt.Errorf("scanning table list: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// LoadTables reads full structure for every ref in O(len(refs)/idChunkSize)
// round trips instead of one query per table, which matters once a database
// has hundreds of tables.
func LoadTables(ctx context.Context, db *sql.DB, refs []TableRef) ([]*Table, error) {
	if len(refs) == 0 {
		return nil, nil
	}

	tables := make(map[int64]*Table, len(refs))
	order := make([]int64, len(refs))
	ids := make([]int64, len(refs))
	for i, r := range refs {
		tables[r.ObjectID] = &Table{ObjectID: r.ObjectID, Schema: r.Schema, Name: r.Name}
		order[i] = r.ObjectID
		ids[i] = r.ObjectID
	}

	for _, chunk := range chunkIDs(ids, idChunkSize) {
		if err := loadColumns(ctx, db, chunk, tables); err != nil {
			return nil, err
		}
		if err := loadIndexes(ctx, db, chunk, tables); err != nil {
			return nil, err
		}
		if err := loadForeignKeys(ctx, db, chunk, tables); err != nil {
			return nil, err
		}
		if err := loadCheckConstraints(ctx, db, chunk, tables); err != nil {
			return nil, err
		}
	}

	out := make([]*Table, len(order))
	for i, id := range order {
		out[i] = tables[id]
	}
	return out, nil
}

func chunkIDs(ids []int64, size int) [][]int64 {
	var chunks [][]int64
	for size < len(ids) {
		ids, chunks = ids[size:], append(chunks, ids[:size:size])
	}
	return append(chunks, ids)
}

// idListSQL renders ids as a literal SQL IN-list. Safe without
// parameterization: ids always originate from our own prior sys.tables
// query, never from external input.
func idListSQL(ids []int64) string {
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

func loadColumns(ctx context.Context, db *sql.DB, ids []int64, tables map[int64]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT
			c.object_id, c.column_id, c.name, ty.name,
			c.max_length, c.precision, c.scale,
			ISNULL(c.collation_name, ''),
			c.is_nullable, c.is_identity,
			ISNULL(ic.seed_value, 0), ISNULL(ic.increment_value, 0),
			c.is_computed, ISNULL(cc.definition, ''), ISNULL(cc.is_persisted, 0),
			ISNULL(dc.name, ''), ISNULL(dc.definition, '')
		FROM sys.columns c
		JOIN sys.types ty
			ON c.system_type_id = ty.system_type_id AND ty.user_type_id = ty.system_type_id
		LEFT JOIN sys.identity_columns ic
			ON ic.object_id = c.object_id AND ic.column_id = c.column_id
		LEFT JOIN sys.computed_columns cc
			ON cc.object_id = c.object_id AND cc.column_id = c.column_id
		LEFT JOIN sys.default_constraints dc
			ON dc.parent_object_id = c.object_id AND dc.parent_column_id = c.column_id
		WHERE c.object_id IN (`+idListSQL(ids)+`)
		ORDER BY c.object_id, c.column_id`)
	if err != nil {
		return fmt.Errorf("loading columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var objectID int64
		var c Column
		var seed, incr int64
		if err := rows.Scan(
			&objectID, &c.ColumnID, &c.Name, &c.TypeName,
			&c.MaxLength, &c.Precision, &c.Scale,
			&c.CollationName,
			&c.IsNullable, &c.IsIdentity,
			&seed, &incr,
			&c.IsComputed, &c.ComputedDefinition, &c.IsPersisted,
			&c.DefaultConstraintName, &c.DefaultDefinition,
		); err != nil {
			return fmt.Errorf("scanning column: %w", err)
		}
		c.IdentitySeed, c.IdentityIncrement = seed, incr
		t := tables[objectID]
		t.Columns = append(t.Columns, c)
	}
	return rows.Err()
}

type indexKey struct {
	objectID int64
	indexID  int
}

func loadIndexes(ctx context.Context, db *sql.DB, ids []int64, tables map[int64]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT
			i.object_id, i.index_id, i.name,
			i.is_primary_key, i.is_unique_constraint, i.is_unique,
			CASE WHEN i.type = 1 THEN 1 ELSE 0 END,
			ISNULL(i.filter_definition, ''),
			col.name, ic.is_descending_key, ic.is_included_column
		FROM sys.indexes i
		JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
		WHERE i.object_id IN (`+idListSQL(ids)+`) AND i.type IN (1, 2) AND i.name IS NOT NULL
		ORDER BY i.object_id, i.index_id, ic.is_included_column, ic.key_ordinal, ic.index_column_id`)
	if err != nil {
		return fmt.Errorf("loading indexes: %w", err)
	}
	defer rows.Close()

	byKey := map[indexKey]*Index{}
	var keyOrder []indexKey
	for rows.Next() {
		var k indexKey
		var ix Index
		var col IndexColumn
		if err := rows.Scan(
			&k.objectID, &k.indexID, &ix.Name,
			&ix.IsPrimaryKey, &ix.IsUniqueConstraint, &ix.IsUnique,
			&ix.IsClustered,
			&ix.FilterDefinition,
			&col.ColumnName, &col.IsDescending, &col.IsIncluded,
		); err != nil {
			return fmt.Errorf("scanning index column: %w", err)
		}
		existing, ok := byKey[k]
		if !ok {
			cp := ix
			existing = &cp
			byKey[k] = existing
			keyOrder = append(keyOrder, k)
		}
		existing.Columns = append(existing.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, k := range keyOrder {
		t := tables[k.objectID]
		t.Indexes = append(t.Indexes, *byKey[k])
	}
	return nil
}

func loadForeignKeys(ctx context.Context, db *sql.DB, ids []int64, tables map[int64]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT
			fk.parent_object_id, fk.object_id, fk.name,
			rs.name, rt.name,
			fk.delete_referential_action_desc, fk.update_referential_action_desc,
			pc.name, rc.name
		FROM sys.foreign_keys fk
		JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id
		JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
		WHERE fk.parent_object_id IN (`+idListSQL(ids)+`)
		ORDER BY fk.parent_object_id, fk.name, fkc.constraint_column_id`)
	if err != nil {
		return fmt.Errorf("loading foreign keys: %w", err)
	}
	defer rows.Close()

	byID := map[int64]*ForeignKey{}
	var idOrder []int64
	parentOf := map[int64]int64{}
	for rows.Next() {
		var parentObjectID, fkObjectID int64
		var fk ForeignKey
		var col ForeignKeyColumn
		if err := rows.Scan(
			&parentObjectID, &fkObjectID, &fk.Name,
			&fk.RefSchema, &fk.RefTable,
			&fk.DeleteAction, &fk.UpdateAction,
			&col.ParentColumn, &col.RefColumn,
		); err != nil {
			return fmt.Errorf("scanning foreign key column: %w", err)
		}
		existing, ok := byID[fkObjectID]
		if !ok {
			cp := fk
			existing = &cp
			byID[fkObjectID] = existing
			idOrder = append(idOrder, fkObjectID)
			parentOf[fkObjectID] = parentObjectID
		}
		existing.Columns = append(existing.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range idOrder {
		t := tables[parentOf[id]]
		t.ForeignKeys = append(t.ForeignKeys, *byID[id])
	}
	return nil
}

func loadCheckConstraints(ctx context.Context, db *sql.DB, ids []int64, tables map[int64]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT parent_object_id, name, definition
		FROM sys.check_constraints
		WHERE parent_object_id IN (`+idListSQL(ids)+`)
		ORDER BY parent_object_id, name`)
	if err != nil {
		return fmt.Errorf("loading check constraints: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var objectID int64
		var cc CheckConstraint
		if err := rows.Scan(&objectID, &cc.Name, &cc.Definition); err != nil {
			return fmt.Errorf("scanning check constraint: %w", err)
		}
		tables[objectID].CheckConstraints = append(tables[objectID].CheckConstraints, cc)
	}
	return rows.Err()
}
