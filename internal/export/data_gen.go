package export

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rafaqwe1/sqlserver-import-export/internal/meta"
	"github.com/rafaqwe1/sqlserver-import-export/internal/sqlfmt"
)

// maxBatchRows is SQL Server's hard limit on the number of row value
// expressions in a single INSERT ... VALUES (...),(...) statement.
const maxBatchRows = 1000

func tableKey(t *meta.Table) string {
	return strings.ToLower(t.Schema + "." + t.Name)
}

// orderTablesForInsert topologically sorts tables so a table referenced by a
// foreign key is inserted before the table that references it. Tables that
// can't be ordered this way (self-referencing FKs, or multi-table FK cycles)
// come back in deferredFK: their FK constraints must be disabled before their
// data loads and re-enabled afterward.
func orderTablesForInsert(tables []*meta.Table) (order []*meta.Table, deferredFK map[string]bool) {
	byKey := make(map[string]*meta.Table, len(tables))
	for _, t := range tables {
		byKey[tableKey(t)] = t
	}

	adjacency := map[string][]string{}
	indegree := map[string]int{}
	selfRef := map[string]bool{}
	for k := range byKey {
		indegree[k] = 0
	}

	for _, t := range tables {
		k := tableKey(t)
		for _, fk := range t.ForeignKeys {
			refKey := strings.ToLower(fk.RefSchema + "." + fk.RefTable)
			if refKey == k {
				selfRef[k] = true
				continue
			}
			if _, ok := byKey[refKey]; !ok {
				continue
			}
			adjacency[refKey] = append(adjacency[refKey], k)
			indegree[k]++
		}
	}

	allKeys := make([]string, 0, len(byKey))
	for k := range byKey {
		allKeys = append(allKeys, k)
	}
	sort.Strings(allKeys)

	var queue []string
	for _, k := range allKeys {
		if indegree[k] == 0 {
			queue = append(queue, k)
		}
	}

	visited := map[string]bool{}
	var orderKeys []string
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		if visited[k] {
			continue
		}
		visited[k] = true
		orderKeys = append(orderKeys, k)

		neighbors := append([]string(nil), adjacency[k]...)
		sort.Strings(neighbors)
		var newlyZero []string
		for _, nb := range neighbors {
			indegree[nb]--
			if indegree[nb] == 0 {
				newlyZero = append(newlyZero, nb)
			}
		}
		sort.Strings(newlyZero)
		queue = append(queue, newlyZero...)
	}

	var leftover []string
	for _, k := range allKeys {
		if !visited[k] {
			leftover = append(leftover, k)
		}
	}
	sort.Strings(leftover)
	orderKeys = append(orderKeys, leftover...)

	deferredFK = map[string]bool{}
	for k := range selfRef {
		deferredFK[k] = true
	}
	for _, k := range leftover {
		deferredFK[k] = true
	}

	order = make([]*meta.Table, len(orderKeys))
	for i, k := range orderKeys {
		order[i] = byKey[k]
	}
	return order, deferredFK
}

// WriteDataSQL writes the full data-import.sql contents. Tables are queried
// concurrently (bounded by parallel) into per-table temp files under tmpDir,
// then concatenated into w in dependency order: this overlaps each table's
// DB round trip with every other table's, while keeping w a single ordered
// stream and peak memory independent of table size.
func WriteDataSQL(ctx context.Context, db *sql.DB, w io.Writer, tables []*meta.Table, batchSize, parallel int, tmpDir string) error {
	order, deferredFK := orderTablesForInsert(tables)
	if batchSize <= 0 || batchSize > maxBatchRows {
		batchSize = maxBatchRows
	}
	if parallel < 1 {
		parallel = 1
	}

	tempFiles, cleanup, err := exportTablesConcurrently(ctx, db, order, batchSize, parallel, tmpDir)
	defer cleanup()
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "SET NOCOUNT ON;")
	fmt.Fprintln(w, "GO")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "SET XACT_ABORT OFF;")
	fmt.Fprintln(w, "GO")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "BEGIN TRANSACTION;")
	fmt.Fprintln(w, "GO")
	fmt.Fprintln(w)

	for i, t := range order {
		if deferredFK[tableKey(t)] {
			fmt.Fprintf(w, "ALTER TABLE %s NOCHECK CONSTRAINT ALL;\n", sqlfmt.QuoteQualified(t.Schema, t.Name))
			fmt.Fprintln(w, "GO")
			fmt.Fprintln(w)
		}
		if err := appendFile(w, tempFiles[i]); err != nil {
			return fmt.Errorf("assembling data for %s: %w", t.QualifiedName(), err)
		}
	}

	if len(deferredFK) > 0 {
		fmt.Fprintln(w, "-- Re-enabling and validating foreign keys deferred above")
		for _, t := range order {
			if deferredFK[tableKey(t)] {
				fmt.Fprintf(w, "ALTER TABLE %s WITH CHECK CHECK CONSTRAINT ALL;\n", sqlfmt.QuoteQualified(t.Schema, t.Name))
				fmt.Fprintln(w, "GO")
				fmt.Fprintln(w)
			}
		}
	}

	fmt.Fprintln(w, "COMMIT TRANSACTION;")
	fmt.Fprintln(w, "GO")
	return nil
}

// exportTablesConcurrently runs a bounded worker pool over order, each worker
// streaming one table's INSERT statements to its own temp file. Returns the
// temp file paths (index-aligned with order) and a cleanup func that removes
// them; cleanup is always safe to call, including after an error.
func exportTablesConcurrently(ctx context.Context, db *sql.DB, order []*meta.Table, batchSize, parallel int, tmpDir string) ([]string, func(), error) {
	workDir, err := os.MkdirTemp(tmpDir, ".sie-export-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("creating temp directory: %w", err)
	}
	cleanup := func() { os.RemoveAll(workDir) }

	paths := make([]string, len(order))
	jobs := make(chan int)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}

	for range parallel {
		wg.Go(func() {
			for idx := range jobs {
				t := order[idx]
				path := filepath.Join(workDir, fmt.Sprintf("%d.sql", idx))
				if err := exportTableToFile(ctx, db, t, path, batchSize); err != nil {
					fail(fmt.Errorf("exporting %s: %w", t.QualifiedName(), err))
					return
				}
				paths[idx] = path
			}
		})
	}

feed:
	for i := range order {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	return paths, cleanup, firstErr
}

func exportTableToFile(ctx context.Context, db *sql.DB, t *meta.Table, path string, batchSize int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if err := writeTableData(ctx, db, w, t, batchSize); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func appendFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// estimateRowCount reads sys.partitions instead of running COUNT(*), so the
// informational row count in the "-- TABLE:" comment doesn't cost a second
// full scan of a multi-gigabyte table.
func estimateRowCount(ctx context.Context, db *sql.DB, objectID int64) (int64, error) {
	var n sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT SUM(p.rows) FROM sys.partitions p
		WHERE p.object_id = @p1 AND p.index_id IN (0, 1)`, objectID).Scan(&n)
	return n.Int64, err
}

func writeTableData(ctx context.Context, db *sql.DB, w io.Writer, t *meta.Table, batchSize int) error {
	qualified := sqlfmt.QuoteQualified(t.Schema, t.Name)
	cols := t.InsertableColumns()

	rowCount, err := estimateRowCount(ctx, db, t.ObjectID)
	if err != nil {
		return fmt.Errorf("estimating row count: %w", err)
	}
	fmt.Fprintf(w, "-- TABLE: %s (~%d rows)\n", t.QualifiedName(), rowCount)

	if len(cols) == 0 {
		fmt.Fprintln(w, "-- (no insertable columns: table is entirely computed/rowversion columns)")
		fmt.Fprintln(w)
		return nil
	}

	colNames := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = sqlfmt.QuoteIdent(c.Name)
	}
	colList := strings.Join(colNames, ", ")

	hasIdentity := t.HasIdentity()
	if hasIdentity {
		fmt.Fprintf(w, "SET IDENTITY_INSERT %s ON;\n", qualified)
		fmt.Fprintln(w, "GO")
		fmt.Fprintln(w)
	}

	rows, err := db.QueryContext(ctx, "SELECT "+colList+" FROM "+qualified)
	if err != nil {
		return fmt.Errorf("querying data: %w", err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return fmt.Errorf("reading column types: %w", err)
	}
	dbTypeNames := make([]string, len(colTypes))
	for i, ct := range colTypes {
		dbTypeNames[i] = ct.DatabaseTypeName()
	}

	vals := make([]any, len(cols))
	dest := make([]any, len(cols))
	for i := range dest {
		dest[i] = &vals[i]
	}

	var rowsBuf bytes.Buffer
	pending := 0
	flush := func() error {
		if pending == 0 {
			return nil
		}
		if _, err := fmt.Fprintf(w, "INSERT INTO %s (%s) VALUES\n", qualified, colList); err != nil {
			return err
		}
		if _, err := w.Write(rowsBuf.Bytes()); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ";\nGO\n\n"); err != nil {
			return err
		}
		rowsBuf.Reset()
		pending = 0
		return nil
	}

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if pending > 0 {
			rowsBuf.WriteString(",\n")
		}
		rowsBuf.WriteByte('(')
		for i, v := range vals {
			if i > 0 {
				rowsBuf.WriteString(", ")
			}
			sqlfmt.WriteValue(&rowsBuf, v, dbTypeNames[i])
		}
		rowsBuf.WriteByte(')')
		pending++
		if pending >= batchSize {
			if err := flush(); err != nil {
				return fmt.Errorf("writing batch: %w", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading rows: %w", err)
	}
	if err := flush(); err != nil {
		return fmt.Errorf("writing batch: %w", err)
	}

	if hasIdentity {
		fmt.Fprintf(w, "SET IDENTITY_INSERT %s OFF;\n", qualified)
		fmt.Fprintln(w, "GO")
		fmt.Fprintln(w)
	}

	return nil
}
