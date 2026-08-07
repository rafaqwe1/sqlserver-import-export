//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rafaqwe1/sqlserver-import-export/internal/export"
	"github.com/rafaqwe1/sqlserver-import-export/internal/importrun"
)

// TestExportTablesFilter_InlineList exports only a subset of tables via the
// comma-separated -tables form, and checks that the excluded table's FK is
// skipped (commented out) rather than emitted (which would fail since its
// target table doesn't exist in the export), while data for the included
// tables still imports cleanly.
func TestExportTablesFilter_InlineList(t *testing.T) {
	ctx := context.Background()
	srcName := newTestDatabase(t, "src")
	tgtName := newTestDatabase(t, "tgt")
	src := openDB(t, srcName)
	tgt := openDB(t, tgtName)

	execSQL(t, src, fixtureDDL()...)
	execSQL(t, src, fixtureData()...)

	dir := t.TempDir()
	// Orders references Customers via FK; excluding Customers must not break the export.
	if err := export.Run(ctx, connStringForDB(srcName), dir, "Orders", 100, 2); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	schemaSQL := readFile(t, filepath.Join(dir, "schema.sql"))
	if strings.Contains(schemaSQL, "CREATE TABLE [dbo].[Customers]") {
		t.Errorf("schema.sql should not contain Customers, it wasn't requested")
	}
	if !strings.Contains(schemaSQL, "CREATE TABLE [dbo].[Orders]") {
		t.Errorf("schema.sql missing requested table Orders")
	}
	if strings.Contains(schemaSQL, "ADD CONSTRAINT [FK_Orders_Customers]") {
		t.Errorf("FK to excluded table Customers should have been skipped, not emitted")
	}
	if !strings.Contains(schemaSQL, "Skipped FK") {
		t.Errorf("expected a comment noting the skipped FK")
	}

	if err := importrun.Run(ctx, connStringForDB(tgtName), dir, false, false); err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if tableExists(t, tgt, "dbo", "Customers") {
		t.Errorf("Customers should not have been created on target")
	}
	srcN := rowCount(t, src, "[dbo].[Orders]")
	tgtN := rowCount(t, tgt, "[dbo].[Orders]")
	if srcN != tgtN || srcN == 0 {
		t.Errorf("Orders row count mismatch: source=%d target=%d", srcN, tgtN)
	}
}

// TestExportTablesFilter_FileList checks that a file-based -tables list
// (one name per line, "--" comments, blank lines) resolves the same set of
// tables as the equivalent inline comma list.
func TestExportTablesFilter_FileList(t *testing.T) {
	ctx := context.Background()
	srcName := newTestDatabase(t, "src")
	src := openDB(t, srcName)
	execSQL(t, src, fixtureDDL()...)
	execSQL(t, src, fixtureData()...)

	listFile := filepath.Join(t.TempDir(), "tables.txt")
	content := "-- tables to export\ndbo.Customers\n\nOrders\n"
	if err := os.WriteFile(listFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := export.Run(ctx, connStringForDB(srcName), dir, listFile, 100, 2); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	schemaSQL := readFile(t, filepath.Join(dir, "schema.sql"))
	for _, want := range []string{"CREATE TABLE [dbo].[Customers]", "CREATE TABLE [dbo].[Orders]", "ADD CONSTRAINT [FK_Orders_Customers]"} {
		if !strings.Contains(schemaSQL, want) {
			t.Errorf("schema.sql missing %q", want)
		}
	}
	for _, unwanted := range []string{"[dbo].[Employees]", "[dbo].[AllTypes]"} {
		if strings.Contains(schemaSQL, "CREATE TABLE "+unwanted) {
			t.Errorf("schema.sql should not contain %s, it wasn't requested", unwanted)
		}
	}
}

// TestExportTablesFilter_UnknownTable checks that requesting a table that
// doesn't exist in the database produces a clear error rather than silently
// exporting nothing or a partial set.
func TestExportTablesFilter_UnknownTable(t *testing.T) {
	ctx := context.Background()
	srcName := newTestDatabase(t, "src")
	src := openDB(t, srcName)
	execSQL(t, src, fixtureDDL()...)

	dir := t.TempDir()
	err := export.Run(ctx, connStringForDB(srcName), dir, "Customers,DoesNotExist", 100, 2)
	if err == nil {
		t.Fatal("expected an error for a nonexistent table, got nil")
	}
	if !strings.Contains(err.Error(), "DoesNotExist") {
		t.Errorf("expected error to mention the missing table name, got: %v", err)
	}
}
