// Package importrun implements "-mode import": executing schema.sql and
// data-import.sql (as produced by the export mode) against a target server.
package importrun

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	goLineRe  = regexp.MustCompile(`(?i)^\s*GO\s*(\d+)?\s*$`)
	tableMkRe = regexp.MustCompile(`(?i)^--\s*TABLE:\s*(\S+)`)
)

// readerBufSize is generous because a single batch line (e.g. a huge BLOB
// literal) can be far larger than bufio's default.
const readerBufSize = 1 << 20

// Run connects to connStr with a single dedicated connection (so that the
// BEGIN TRANSACTION in data-import.sql stays open across every batch), runs
// schema.sql first unless skipSchema is set, then data-import.sql. With
// reset set, the target is cleared first (dropped and recreated by
// schema.sql, or just emptied if skipSchema is also set) so re-running
// import doesn't fail on objects left over from a previous run.
func Run(ctx context.Context, connStr, dir string, skipSchema, reset bool) error {
	db, err := sql.Open("sqlserver", connStr)
	if err != nil {
		return fmt.Errorf("opening connection: %w", err)
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer conn.Close()

	if reset {
		if err := resetDatabase(ctx, conn, !skipSchema); err != nil {
			return fmt.Errorf("resetting target database: %w", err)
		}
	}

	if !skipSchema {
		if err := runSchema(ctx, conn, dir); err != nil {
			return err
		}
	} else {
		fmt.Println("Skipping schema.sql (-skip-schema)")
	}

	return runData(ctx, conn, dir)
}

func runSchema(ctx context.Context, conn *sql.Conn, dir string) error {
	f, err := os.Open(filepath.Join(dir, "schema.sql"))
	if err != nil {
		return fmt.Errorf("opening schema.sql: %w", err)
	}
	defer f.Close()

	fmt.Println("Running schema.sql...")
	br := newBatchReader(f)
	i := 0
	for {
		batch, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading schema.sql: %w", err)
		}
		i++
		if _, err := conn.ExecContext(ctx, batch); err != nil {
			return fmt.Errorf("schema.sql batch %d failed, stopping: %w\n--- batch ---\n%s", i, err, batch)
		}
		fmt.Printf("[schema] %d OK\n", i)
	}
	return nil
}

func runData(ctx context.Context, conn *sql.Conn, dir string) error {
	f, err := os.Open(filepath.Join(dir, "data-import.sql"))
	if err != nil {
		return fmt.Errorf("opening data-import.sql: %w", err)
	}
	defer f.Close()

	fmt.Println("Running data-import.sql...")
	br := newBatchReader(f)
	bulkTypes := newColumnTypeCache()
	currentTable := ""
	var failures []batchFailure
	i, okCount := 0, 0
	for {
		batch, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading data-import.sql: %w", err)
		}
		i++

		if table, ok := tableMarker(batch); ok {
			currentTable = table
		}

		handled, execErr := tryBulkInsert(ctx, conn, bulkTypes, batch)
		if !handled {
			_, execErr = conn.ExecContext(ctx, batch)
		}
		if execErr != nil {
			failures = append(failures, batchFailure{table: currentTable, batch: i, err: execErr})
			fmt.Printf("[data] %s %d FAILED: %v\n", currentTable, i, execErr)
			if reopened, rerr := reopenTransactionIfRolledBack(ctx, conn); rerr != nil {
				return fmt.Errorf("recovering session after batch %d failure: %w", i, rerr)
			} else if reopened {
				fmt.Printf("[data] %s %d: that error implicitly rolled back the whole transaction (SQL Server does this for some errors even with XACT_ABORT OFF); "+
					"a new transaction was reopened so the import can continue, but any earlier batches in the old transaction were undone and must be re-run\n",
					currentTable, i)
			}
			continue
		}
		okCount++
		fmt.Printf("[data] %s %d OK\n", currentTable, i)
	}

	fmt.Printf("data-import.sql complete: %d succeeded, %d failed (of %d batches)\n", okCount, len(failures), i)
	if len(failures) > 0 {
		printFailureSummary(failures)
		return fmt.Errorf("%d data batch(es) failed during import; see summary above", len(failures))
	}
	return nil
}

type batchFailure struct {
	table string
	batch int
	err   error
}

// printFailureSummary prints every failure grouped by table, in the order
// tables were first seen, so a long run's scattered per-batch FAILED lines
// don't have to be hunted through by hand — the whole block at the end can
// just be copied out as-is.
func printFailureSummary(failures []batchFailure) {
	byTable := map[string][]batchFailure{}
	var order []string
	for _, f := range failures {
		if _, ok := byTable[f.table]; !ok {
			order = append(order, f.table)
		}
		byTable[f.table] = append(byTable[f.table], f)
	}

	fmt.Printf("\n=== %d failed batch(es) across %d table(s) ===\n", len(failures), len(order))
	for _, table := range order {
		fs := byTable[table]
		fmt.Printf("\n%s (%d batch(es)):\n", table, len(fs))
		for _, f := range fs {
			fmt.Printf("  batch %d: %v\n", f.batch, f.err)
		}
	}
}

// reopenTransactionIfRolledBack checks whether the session's transaction was
// implicitly rolled back by the error that was just handled. Most runtime
// errors leave a transaction open (with XACT_ABORT OFF, which data-import.sql
// sets), but SQL Server unconditionally rolls back the current transaction
// for some errors detected at statement compile/bind time (e.g. converting
// an invalid literal to its target type), regardless of XACT_ABORT. Left
// alone, every later statement would silently run outside any transaction,
// and the script's trailing COMMIT would fail with "no corresponding BEGIN
// TRANSACTION". Reopening a transaction here keeps the rest of the script
// coherent; it does not recover whatever the aborted transaction had already
// done.
func reopenTransactionIfRolledBack(ctx context.Context, conn *sql.Conn) (bool, error) {
	var tranCount int
	if err := conn.QueryRowContext(ctx, "SELECT @@TRANCOUNT").Scan(&tranCount); err != nil {
		return false, fmt.Errorf("checking @@TRANCOUNT: %w", err)
	}
	if tranCount > 0 {
		return false, nil
	}
	if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		return false, fmt.Errorf("reopening transaction: %w", err)
	}
	return true, nil
}

func tableMarker(batch string) (string, bool) {
	line, _, _ := strings.Cut(batch, "\n")
	m := tableMkRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// batchReader splits a T-SQL script into batches on lines that are just "GO"
// (case-insensitive, optionally with a repeat count, which is ignored since
// we only ever generate a plain "GO"). It reads line-by-line with no fixed
// token-size limit (unlike bufio.Scanner), so one arbitrarily long line (a
// large BLOB literal) can't abort the read, and it holds only the current
// batch in memory rather than the whole file.
type batchReader struct {
	r   *bufio.Reader
	buf bytes.Buffer
}

func newBatchReader(r io.Reader) *batchReader {
	return &batchReader{r: bufio.NewReaderSize(r, readerBufSize)}
}

func (b *batchReader) Next() (string, error) {
	for {
		line, err := b.r.ReadString('\n')
		if len(line) > 0 {
			if goLineRe.MatchString(strings.TrimRight(line, "\r\n")) {
				if batch := strings.TrimSpace(b.buf.String()); batch != "" {
					b.buf.Reset()
					return batch, nil
				}
				b.buf.Reset()
			} else {
				b.buf.WriteString(line)
			}
		}
		if err != nil {
			if batch := strings.TrimSpace(b.buf.String()); batch != "" {
				b.buf.Reset()
				return batch, nil
			}
			return "", io.EOF
		}
	}
}
