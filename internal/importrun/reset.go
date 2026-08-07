package importrun

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rafaqwe1/sqlserver-import-export/internal/sqlfmt"
)

type tableRef struct{ schema, name string }

func listUserTables(ctx context.Context, conn *sql.Conn) ([]tableRef, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT s.name, t.name
		FROM sys.tables t
		JOIN sys.schemas s ON t.schema_id = s.schema_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []tableRef
	for rows.Next() {
		var r tableRef
		if err := rows.Scan(&r.schema, &r.name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type fkRef struct{ schema, table, name string }

func listForeignKeys(ctx context.Context, conn *sql.Conn) ([]fkRef, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT s.name, t.name, fk.name
		FROM sys.foreign_keys fk
		JOIN sys.tables t ON t.object_id = fk.parent_object_id
		JOIN sys.schemas s ON s.schema_id = t.schema_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []fkRef
	for rows.Next() {
		var r fkRef
		if err := rows.Scan(&r.schema, &r.table, &r.name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// resetDatabase clears out whatever a previous import run left behind, so
// running import again doesn't fail with "there is already an object named
// ... in the database".
//
// dropTables (true when schema.sql is about to run) drops every FK
// constraint and then every table outright — safe because schema.sql
// recreates all of it from scratch immediately afterward, so nothing needs
// to be remembered before dropping.
//
// With -skip-schema, tables are assumed to already be the ones you want to
// keep — dropTables is false, and reset instead disables constraints, wipes
// every row, and re-enables constraints, leaving structure untouched.
func resetDatabase(ctx context.Context, conn *sql.Conn, dropTables bool) error {
	tables, err := listUserTables(ctx, conn)
	if err != nil {
		return fmt.Errorf("listing tables: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}

	if dropTables {
		fmt.Printf("Resetting target database: dropping %d table(s)...\n", len(tables))
		fks, err := listForeignKeys(ctx, conn)
		if err != nil {
			return fmt.Errorf("listing foreign keys: %w", err)
		}
		for _, fk := range fks {
			stmt := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s",
				sqlfmt.QuoteQualified(fk.schema, fk.table), sqlfmt.QuoteIdent(fk.name))
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("dropping foreign key %s: %w", fk.name, err)
			}
		}
		for _, t := range tables {
			if _, err := conn.ExecContext(ctx, "DROP TABLE "+sqlfmt.QuoteQualified(t.schema, t.name)); err != nil {
				return fmt.Errorf("dropping table %s.%s: %w", t.schema, t.name, err)
			}
		}
		return nil
	}

	fmt.Printf("Resetting target database: clearing data from %d table(s)...\n", len(tables))
	for _, t := range tables {
		q := sqlfmt.QuoteQualified(t.schema, t.name)
		if _, err := conn.ExecContext(ctx, "ALTER TABLE "+q+" NOCHECK CONSTRAINT ALL"); err != nil {
			return fmt.Errorf("disabling constraints on %s.%s: %w", t.schema, t.name, err)
		}
	}
	for _, t := range tables {
		q := sqlfmt.QuoteQualified(t.schema, t.name)
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+q); err != nil {
			return fmt.Errorf("deleting data from %s.%s: %w", t.schema, t.name, err)
		}
	}
	for _, t := range tables {
		q := sqlfmt.QuoteQualified(t.schema, t.name)
		if _, err := conn.ExecContext(ctx, "ALTER TABLE "+q+" WITH CHECK CHECK CONSTRAINT ALL"); err != nil {
			return fmt.Errorf("re-enabling constraints on %s.%s: %w", t.schema, t.name, err)
		}
	}
	return nil
}
