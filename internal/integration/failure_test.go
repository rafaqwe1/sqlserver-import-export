//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rafaqwe1/sqlserver-import-export/internal/importrun"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestImport_SchemaFailureStopsImmediately verifies that a broken batch in
// schema.sql aborts the whole import right away: later CREATE TABLE
// statements must never run.
func TestImport_SchemaFailureStopsImmediately(t *testing.T) {
	ctx := context.Background()
	tgtName := newTestDatabase(t, "tgt")
	tgt := openDB(t, tgtName)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schema.sql"), `
CREATE TABLE dbo.Broken (this is not valid T-SQL at all);
GO

CREATE TABLE dbo.ShouldNotExist (ID int NOT NULL PRIMARY KEY);
GO
`)

	err := importrun.Run(ctx, connStringForDB(tgtName), dir, false, false)
	if err == nil {
		t.Fatal("expected an error from a broken schema.sql batch, got nil")
	}
	if !strings.Contains(err.Error(), "schema.sql batch") {
		t.Errorf("expected error to mention the failing schema.sql batch, got: %v", err)
	}
	if tableExists(t, tgt, "dbo", "ShouldNotExist") {
		t.Error("schema execution should have stopped before the second CREATE TABLE, but it ran")
	}
}

// TestImport_DataBatchFailureContinues verifies that a failing INSERT batch
// in data-import.sql is logged and does not abort the transaction: later
// batches (including for the same table) still run and commit.
func TestImport_DataBatchFailureContinues(t *testing.T) {
	ctx := context.Background()
	tgtName := newTestDatabase(t, "tgt")
	tgt := openDB(t, tgtName)
	execSQL(t, tgt, `CREATE TABLE dbo.Widgets (
		WidgetID int NOT NULL PRIMARY KEY,
		Name varchar(50) NOT NULL UNIQUE
	)`)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "data-import.sql"), `
SET XACT_ABORT OFF;
GO

BEGIN TRANSACTION;
GO

-- TABLE: dbo.Widgets (2 rows) -- duplicate Name violates the UNIQUE constraint, whole batch rolls back
INSERT INTO dbo.Widgets (WidgetID, Name) VALUES (1, 'Widget1'), (2, 'Widget1');
GO

-- TABLE: dbo.Widgets (1 rows)
INSERT INTO dbo.Widgets (WidgetID, Name) VALUES (3, 'Widget3');
GO

COMMIT TRANSACTION;
GO
`)

	err := importrun.Run(ctx, connStringForDB(tgtName), dir, true, false)
	if err == nil {
		t.Fatal("expected an error reporting the failed data batch, got nil")
	}
	if !strings.Contains(err.Error(), "1 data batch(es) failed") {
		t.Errorf("expected error to report exactly 1 failed batch, got: %v", err)
	}

	n := rowCount(t, tgt, "dbo.Widgets")
	if n != 1 {
		t.Fatalf("expected 1 surviving row (the failed batch's insert must not have landed), got %d", n)
	}
	name := scalarString(t, tgt, "SELECT Name FROM dbo.Widgets")
	if name != "Widget3" {
		t.Errorf("expected the surviving row to be Widget3, got %q", name)
	}
}

// TestImport_SkipSchema verifies -skip-schema loads data-import.sql into
// already-existing tables without touching schema.sql.
func TestImport_SkipSchema(t *testing.T) {
	ctx := context.Background()
	tgtName := newTestDatabase(t, "tgt")
	tgt := openDB(t, tgtName)

	schemaDir := t.TempDir()
	writeFile(t, filepath.Join(schemaDir, "schema.sql"), `
CREATE TABLE dbo.Gadgets (GadgetID int NOT NULL PRIMARY KEY, Name varchar(50) NOT NULL);
GO
`)
	writeFile(t, filepath.Join(schemaDir, "data-import.sql"), `
BEGIN TRANSACTION;
GO
COMMIT TRANSACTION;
GO
`)
	if err := importrun.Run(ctx, connStringForDB(tgtName), schemaDir, false, false); err != nil {
		t.Fatalf("creating schema failed: %v", err)
	}
	if !tableExists(t, tgt, "dbo", "Gadgets") {
		t.Fatal("Gadgets table was not created")
	}
	if n := rowCount(t, tgt, "dbo.Gadgets"); n != 0 {
		t.Fatalf("expected no data yet, got %d rows", n)
	}

	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "data-import.sql"), `
BEGIN TRANSACTION;
GO
INSERT INTO dbo.Gadgets (GadgetID, Name) VALUES (1, 'Gizmo'), (2, 'Widget');
GO
COMMIT TRANSACTION;
GO
`)
	if err := importrun.Run(ctx, connStringForDB(tgtName), dataDir, true, false); err != nil {
		t.Fatalf("skip-schema data import failed: %v", err)
	}
	if n := rowCount(t, tgt, "dbo.Gadgets"); n != 2 {
		t.Fatalf("expected 2 rows after skip-schema data import, got %d", n)
	}
}
