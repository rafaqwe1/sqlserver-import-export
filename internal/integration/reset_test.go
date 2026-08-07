//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rafaqwe1/sqlserver-import-export/internal/importrun"
)

// TestImport_Reset_FullReset verifies that -reset lets a full import
// (schema + data) run again against a target that already has the schema
// and data from a previous run, without the "object already exists" error
// that motivated adding this flag.
func TestImport_Reset_FullReset(t *testing.T) {
	ctx := context.Background()
	tgtName := newTestDatabase(t, "tgt")
	tgt := openDB(t, tgtName)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schema.sql"), `
CREATE TABLE dbo.Parent (ID int NOT NULL PRIMARY KEY, Name varchar(50) NOT NULL);
GO
CREATE TABLE dbo.Child (ID int NOT NULL PRIMARY KEY, ParentID int NOT NULL, Label varchar(50) NOT NULL);
GO
ALTER TABLE dbo.Child ADD CONSTRAINT FK_Child_Parent FOREIGN KEY (ParentID) REFERENCES dbo.Parent(ID);
GO
`)
	writeFile(t, filepath.Join(dir, "data-import.sql"), `
BEGIN TRANSACTION;
GO
INSERT INTO dbo.Parent (ID, Name) VALUES (1, 'P1');
GO
INSERT INTO dbo.Child (ID, ParentID, Label) VALUES (1, 1, 'C1');
GO
COMMIT TRANSACTION;
GO
`)

	if err := importrun.Run(ctx, connStringForDB(tgtName), dir, false, false); err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if n := rowCount(t, tgt, "dbo.Parent"); n != 1 {
		t.Fatalf("expected 1 row in Parent after first import, got %d", n)
	}

	// Without -reset, this must fail: schema.sql tries to CREATE TABLE objects that already exist.
	if err := importrun.Run(ctx, connStringForDB(tgtName), dir, false, false); err == nil {
		t.Fatal("expected the second import (no -reset) to fail on already-existing tables")
	}

	// With -reset, it must succeed: tables and FKs are dropped and recreated cleanly.
	if err := importrun.Run(ctx, connStringForDB(tgtName), dir, false, true); err != nil {
		t.Fatalf("import with -reset failed: %v", err)
	}
	if n := rowCount(t, tgt, "dbo.Parent"); n != 1 {
		t.Errorf("expected 1 row in Parent after reset import, got %d", n)
	}
	if n := rowCount(t, tgt, "dbo.Child"); n != 1 {
		t.Errorf("expected 1 row in Child after reset import, got %d", n)
	}
	// FK must still be enforced (dropped-then-recreated correctly, not just left disabled).
	_, err := tgt.ExecContext(ctx, "INSERT INTO dbo.Child (ID, ParentID, Label) VALUES (99, 12345, 'orphan')")
	if err == nil {
		t.Error("expected FK violation inserting a Child row with a nonexistent ParentID after reset")
	}
}

// TestImport_Reset_SkipSchemaDataOnly verifies that -reset with -skip-schema
// only clears data, leaving table structure (and FK enforcement) intact.
func TestImport_Reset_SkipSchemaDataOnly(t *testing.T) {
	ctx := context.Background()
	tgtName := newTestDatabase(t, "tgt")
	tgt := openDB(t, tgtName)
	execSQL(t, tgt,
		`CREATE TABLE dbo.Parent (ID int NOT NULL PRIMARY KEY, Name varchar(50) NOT NULL)`,
		`CREATE TABLE dbo.Child (ID int NOT NULL PRIMARY KEY, ParentID int NOT NULL, Label varchar(50) NOT NULL)`,
		`ALTER TABLE dbo.Child ADD CONSTRAINT FK_Child_Parent FOREIGN KEY (ParentID) REFERENCES dbo.Parent(ID)`,
		`INSERT INTO dbo.Parent (ID, Name) VALUES (1, 'Stale')`,
		`INSERT INTO dbo.Child (ID, ParentID, Label) VALUES (1, 1, 'StaleChild')`,
	)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "data-import.sql"), `
BEGIN TRANSACTION;
GO
INSERT INTO dbo.Parent (ID, Name) VALUES (2, 'Fresh');
GO
INSERT INTO dbo.Child (ID, ParentID, Label) VALUES (2, 2, 'FreshChild');
GO
COMMIT TRANSACTION;
GO
`)

	if err := importrun.Run(ctx, connStringForDB(tgtName), dir, true, true); err != nil {
		t.Fatalf("skip-schema reset import failed: %v", err)
	}

	if !tableExists(t, tgt, "dbo", "Parent") || !tableExists(t, tgt, "dbo", "Child") {
		t.Fatal("tables should still exist after a data-only reset")
	}
	if n := rowCount(t, tgt, "dbo.Parent"); n != 1 {
		t.Errorf("expected exactly 1 (fresh) row in Parent, got %d", n)
	}
	name := scalarString(t, tgt, "SELECT Name FROM dbo.Parent")
	if name != "Fresh" {
		t.Errorf("expected only the fresh row to survive, got Name=%q", name)
	}

	_, err := tgt.ExecContext(ctx, "INSERT INTO dbo.Child (ID, ParentID, Label) VALUES (99, 12345, 'orphan')")
	if err == nil {
		t.Error("expected FK to still be enforced after a data-only reset")
	}
}
