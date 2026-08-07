// Command sqlserver-import-export exports a SQL Server database's schema and
// data to two SQL files, and imports them back into another SQL Server.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"

	_ "github.com/microsoft/go-mssqldb"
	"github.com/rafaqwe1/sqlserver-import-export/internal/export"
	"github.com/rafaqwe1/sqlserver-import-export/internal/importrun"
)

// version is set via -ldflags at release build time (see .goreleaser.yaml).
var version = "dev"

func defaultParallelism() int {
	if n := runtime.NumCPU(); n < 8 {
		return n
	}
	return 8
}

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	mode := flag.String("mode", "", "required: \"export\" or \"import\"")
	conn := flag.String("conn", "", "required: SQL Server connection string (source for export, target for import)")
	dir := flag.String("dir", ".", "export: output directory; import: input directory")
	tablesFile := flag.String("tables", "", "export only: comma-separated table names (e.g. \"dbo.Customers,Orders\") or a path to a file listing one per line (default: all user tables)")
	batch := flag.Int("batch", 1000, "export only: rows per INSERT statement (SQL Server's hard cap is 1000)")
	parallel := flag.Int("parallel", defaultParallelism(), "export only: number of tables to read concurrently from the source")
	skipSchema := flag.Bool("skip-schema", false, "import only: skip schema.sql and only run data-import.sql")
	reset := flag.Bool("reset", false, "import only: clear the target first (drop+recreate tables normally, or just delete all rows with -skip-schema) so re-running import doesn't fail on leftovers from a previous run")
	flag.Parse()

	if *showVersion {
		fmt.Println("sqlserver-import-export " + version)
		return
	}

	if *mode != "export" && *mode != "import" {
		fmt.Fprintln(os.Stderr, "error: -mode must be \"export\" or \"import\"")
		flag.Usage()
		os.Exit(2)
	}
	if *conn == "" {
		fmt.Fprintln(os.Stderr, "error: -conn is required")
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()

	var err error
	switch *mode {
	case "export":
		err = export.Run(ctx, *conn, *dir, *tablesFile, *batch, *parallel)
	case "import":
		err = importrun.Run(ctx, *conn, *dir, *skipSchema, *reset)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
