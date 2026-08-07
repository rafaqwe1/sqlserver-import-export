//go:build integration

// Package integration exercises the export and import packages end-to-end
// against a real SQL Server instance. Run with:
//
//	go test -tags integration ./internal/integration/...
//
// The target server defaults to localhost:14330 / sa / TestPass123! (the
// dedicated test container started for this project); override with
// SQLSERVER_TEST_HOST, SQLSERVER_TEST_USER, SQLSERVER_TEST_PASSWORD.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

func testHost() string {
	if v := os.Getenv("SQLSERVER_TEST_HOST"); v != "" {
		return v
	}
	return "localhost:14330"
}

func testUser() string {
	if v := os.Getenv("SQLSERVER_TEST_USER"); v != "" {
		return v
	}
	return "sa"
}

func testPassword() string {
	if v := os.Getenv("SQLSERVER_TEST_PASSWORD"); v != "" {
		return v
	}
	return "TestPass123!"
}

// connStringForDB builds a connection string for dbName, escaping credentials
// properly (avoids the shell-escaping pitfalls of hand-built connection
// strings) and disabling TLS, matching what's needed for the local/dev test
// container.
func connStringForDB(dbName string) string {
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(testUser(), testPassword()),
		Host:   testHost(),
	}
	q := u.Query()
	q.Set("database", dbName)
	q.Set("encrypt", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

func openMaster(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlserver", connStringForDB("master"))
	if err != nil {
		t.Fatalf("opening connection to test server: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("cannot reach test SQL Server at %s: %v\n"+
			"(start the test container or set SQLSERVER_TEST_HOST/SQLSERVER_TEST_USER/SQLSERVER_TEST_PASSWORD)", testHost(), err)
	}
	return db
}

func openDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlserver", connStringForDB(name))
	if err != nil {
		t.Fatalf("opening connection to database %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var dbNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// newTestDatabase creates a uniquely-named database on the test server and
// registers a cleanup that drops it when the test finishes.
func newTestDatabase(t *testing.T, suffix string) string {
	t.Helper()
	master := openMaster(t)

	name := "sie_test_" + dbNameSanitizer.ReplaceAllString(t.Name(), "_") + "_" + suffix +
		fmt.Sprintf("_%d", time.Now().UnixNano()%1_000_000)
	if len(name) > 110 {
		name = name[:110]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := master.ExecContext(ctx, "CREATE DATABASE ["+name+"]"); err != nil {
		t.Fatalf("creating test database %s: %v", name, err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_, _ = master.ExecContext(cctx, "ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE")
		_, _ = master.ExecContext(cctx, "DROP DATABASE ["+name+"]")
	})
	return name
}

func execSQL(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("executing SQL: %v\n--- statement ---\n%s", err, s)
		}
	}
}

func rowCount(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT_BIG(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("counting rows in %s: %v", table, err)
	}
	return n
}

func scalarString(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRowContext(context.Background(), query).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return s.String
}

func tableExists(t *testing.T, db *sql.DB, schema, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM sys.tables tb JOIN sys.schemas s ON tb.schema_id = s.schema_id
		WHERE s.name = @p1 AND tb.name = @p2`, schema, name).Scan(&n)
	if err != nil {
		t.Fatalf("checking table existence %s.%s: %v", schema, name, err)
	}
	return n > 0
}

func indexExists(t *testing.T, db *sql.DB, schema, table, indexName string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM sys.indexes i
		JOIN sys.tables tb ON tb.object_id = i.object_id
		JOIN sys.schemas s ON tb.schema_id = s.schema_id
		WHERE s.name = @p1 AND tb.name = @p2 AND i.name = @p3`, schema, table, indexName).Scan(&n)
	if err != nil {
		t.Fatalf("checking index existence %s on %s.%s: %v", indexName, schema, table, err)
	}
	return n > 0
}
