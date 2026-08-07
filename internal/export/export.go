// Package export implements "-mode export": reading table structure and data
// from a SQL Server database and writing schema.sql / data-import.sql.
package export

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rafaqwe1/sqlserver-import-export/internal/meta"
)

// writerBufSize is the bufio buffer used for schema.sql/data-import.sql
// output; large enough that GB-scale exports aren't dominated by syscalls.
const writerBufSize = 1 << 20

// Run connects to connStr and exports either the tables named by tablesArg
// or every user table if tablesArg is empty, writing schema.sql and
// data-import.sql into dir. tablesArg is either a path to a file listing one
// "schema.table"/"table" (defaulting to dbo) per line, with "--" comments,
// or a comma-separated list of the same in a single string. parallel bounds
// how many tables are read from the source concurrently.
func Run(ctx context.Context, connStr, dir, tablesArg string, batchSize, parallel int) error {
	if batchSize <= 0 || batchSize > maxBatchRows {
		if batchSize > maxBatchRows {
			fmt.Printf("Note: -batch %d exceeds SQL Server's 1000-row VALUES limit; using 1000.\n", batchSize)
		}
		batchSize = maxBatchRows
	}
	if parallel < 1 {
		parallel = 1
	}

	db, err := sql.Open("sqlserver", connStr)
	if err != nil {
		return fmt.Errorf("opening connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(parallel + 2)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}

	refs, err := resolveTables(ctx, db, tablesArg)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return fmt.Errorf("no tables to export")
	}
	fmt.Printf("Exporting %d table(s)...\n", len(refs))

	tables, err := meta.LoadTables(ctx, db, refs)
	if err != nil {
		return fmt.Errorf("loading table structure: %w", err)
	}
	exportedSet := make(map[string]bool, len(refs))
	for _, r := range refs {
		exportedSet[strings.ToLower(r.Schema+"."+r.Name)] = true
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	schemaPath := filepath.Join(dir, "schema.sql")
	schemaSQL := GenerateSchemaSQL(tables, exportedSet)
	if err := os.WriteFile(schemaPath, []byte(schemaSQL), 0o644); err != nil {
		return fmt.Errorf("writing schema.sql: %w", err)
	}
	fmt.Printf("Wrote %s\n", schemaPath)

	dataPath := filepath.Join(dir, "data-import.sql")
	f, err := os.Create(dataPath)
	if err != nil {
		return fmt.Errorf("creating data-import.sql: %w", err)
	}
	bw := bufio.NewWriterSize(f, writerBufSize)

	fmt.Printf("Exporting data (%d table(s) in parallel)...\n", parallel)
	werr := WriteDataSQL(ctx, db, bw, tables, batchSize, parallel, dir)
	if werr == nil {
		werr = bw.Flush()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	fmt.Printf("Wrote %s\n", dataPath)

	return nil
}

// resolveTables interprets tablesArg (if non-empty) and matches each
// requested table case-insensitively against the database's actual tables,
// or lists every user table when tablesArg is empty.
func resolveTables(ctx context.Context, db *sql.DB, tablesArg string) ([]meta.TableRef, error) {
	all, err := meta.ListTables(ctx, db)
	if err != nil {
		return nil, err
	}
	if tablesArg == "" {
		return all, nil
	}

	requested, err := parseTablesArg(tablesArg)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]meta.TableRef, len(all))
	for _, t := range all {
		byKey[strings.ToLower(t.Schema+"."+t.Name)] = t
	}

	var resolved []meta.TableRef
	var missing []string
	for _, r := range requested {
		key := strings.ToLower(r.Schema + "." + r.Name)
		if actual, ok := byKey[key]; ok {
			resolved = append(resolved, actual)
		} else {
			missing = append(missing, r.QualifiedName())
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("tables requested via -tables not found in database: %s", strings.Join(missing, ", "))
	}
	return resolved, nil
}

// parseTablesArg accepts either a path to an existing file listing table
// names one per line, or the table names themselves as a single
// comma-separated string (e.g. "dbo.Customers, Orders"). A file is detected
// by checking whether tablesArg names an existing regular file; otherwise it
// is parsed as a comma-separated list.
func parseTablesArg(tablesArg string) ([]meta.TableRef, error) {
	if info, err := os.Stat(tablesArg); err == nil && !info.IsDir() {
		return parseTablesFile(tablesArg)
	}
	return parseTableRefList(tablesArg, ","), nil
}

// parseTablesFile reads a list of table names, one per line. Lines starting
// with "--" are comments and blank lines are skipped.
func parseTablesFile(path string) ([]meta.TableRef, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading tables file %s: %w", path, err)
	}
	var refs []meta.TableRef
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		refs = append(refs, parseTableRef(line))
	}
	return refs, nil
}

// parseTableRefList splits s on sep, trims each entry, drops empty entries,
// and parses each into a TableRef.
func parseTableRefList(s, sep string) []meta.TableRef {
	var refs []meta.TableRef
	for part := range strings.SplitSeq(s, sep) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		refs = append(refs, parseTableRef(part))
	}
	return refs
}

// parseTableRef parses "schema.table" or "table" (defaulting to schema "dbo").
func parseTableRef(s string) meta.TableRef {
	schema, name := "dbo", s
	if sch, n, ok := strings.Cut(s, "."); ok {
		schema, name = sch, n
	}
	return meta.TableRef{Schema: schema, Name: name}
}
