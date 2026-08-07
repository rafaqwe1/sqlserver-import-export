# sqlserver-import-export

A CLI that exports a SQL Server database's schema and data to two plain SQL files, and imports them back into another SQL Server instance. Handles self-referencing and circular foreign keys, real production data volumes (tested against tables in the tens of millions of rows), and doesn't choke the way SSMS's "Generate Scripts" tends to on either of those.

It's a lot less fiddling than bouncing between SSMS's export wizard, `bcp`, and hand-written scripts to get a real database with messy foreign keys from one server to another — export, copy two files, import, done:

```
schema.sql        CREATE TABLE / INDEX / FOREIGN KEY statements
data-import.sql   Batched INSERT statements, wrapped in one transaction
```

Nothing proprietary — both are ordinary T-SQL you can open, diff, hand-edit, or run yourself with `sqlcmd`. Import mode exists mainly because feeding gigabytes of generated SQL through `sqlcmd` by hand is tedious and doesn't retry sensibly when one table has a bad row.

## Installation

Requires Go 1.24+.

```bash
go install github.com/rafaqwe1/sqlserver-import-export@latest
```

Or from source:

```bash
git clone https://github.com/rafaqwe1/sqlserver-import-export.git
cd sqlserver-import-export
go build -o sqlserver-import-export .
```

## Quick start

```bash
./sqlserver-import-export -mode export \
  -conn "sqlserver://user:pass@source-host:1433?database=MyDb" \
  -dir ./backup

./sqlserver-import-export -mode import \
  -conn "sqlserver://user:pass@target-host:1433?database=MyDb" \
  -dir ./backup
```

`./backup/schema.sql` and `./backup/data-import.sql` are just files — look at them before you trust them.

## Usage

```
sqlserver-import-export -mode <export|import> -conn <connection-string> [flags]
```

| Flag | Modes | Default | Meaning |
|---|---|---|---|
| `-mode` | both | *(required)* | `export` or `import` |
| `-conn` | both | *(required)* | Connection string — source DB for export, target DB for import |
| `-dir` | both | `.` | Output directory (export) or input directory (import) |
| `-tables` | export | *(all tables)* | Comma-separated table names, or a path to a file listing one per line |
| `-batch` | export | `1000` | Rows per `INSERT` statement (SQL Server's hard limit is 1000; higher values get clamped down with a warning) |
| `-parallel` | export | `min(NumCPU, 8)` | Number of tables read from the source concurrently |
| `-skip-schema` | import | `false` | Skip `schema.sql`, only run `data-import.sql` (target tables already exist) |
| `-reset` | import | `false` | Clear the target first, so re-running import doesn't fail on leftovers from a previous run |

### `-reset`

Running import twice against the same target fails the second time — `schema.sql` tries to `CREATE TABLE` objects that already exist. I kept hitting this while iterating, so `-reset` clears the target first:

- **`-reset` alone**: drops every FK and table in the target, then proceeds normally — `schema.sql` recreates everything, `data-import.sql` reloads it.
- **`-reset -skip-schema`**: leaves table structure alone and just empties every table (constraints disabled, rows deleted, constraints re-enabled) before reloading. This is the one I actually use most, for repeatedly reloading data during testing without recreating the schema each time:

  ```bash
  ./sqlserver-import-export -mode import -conn "..." -dir ./backup -skip-schema -reset
  ```

No confirmation prompt — the flag itself is the confirmation. Don't script it against a database you don't mean to wipe.

### Selecting tables to export

Default is every user table. `-tables` takes either an inline comma-separated list:

```bash
./sqlserver-import-export -mode export -conn "..." -dir ./backup \
  -tables "dbo.Customers, Orders, sales.Invoices"
```

or a file, one table per line (`--` for comments, blank lines ignored):

```
# tables.txt
dbo.Customers
Orders
-- Invoices is exported separately, skip it here
```

A name without a schema prefix defaults to `dbo`. If a foreign key points at a table you didn't include, that one constraint gets skipped (noted with a comment in `schema.sql`) instead of failing the whole export.

### Connection strings

Both forms work — either is handled by the driver:

```
sqlserver://user:password@host:1433?database=MyDb
server=host;user id=user;password=password;port=1433;database=MyDb
```

If you're hitting a local/dev SQL Server and get `x509: negative serial number`, that's not this tool — some SQL Server images generate a self-signed cert that Go's stricter TLS parser rejects. Add `&encrypt=disable` to the connection string to skip TLS for that connection (fine for local/dev, don't do it against anything else).

### Failure handling

- A broken statement in `schema.sql` stops the import immediately — nothing after it runs.
- A broken batch in `data-import.sql` gets logged and the import keeps going, so one bad table doesn't block the rest of the restore. Exit code is still non-zero if anything failed. All the failures get collected into a summary block at the end, grouped by table, so you don't have to scroll back through thousands of `OK` lines to find them.
- `data-import.sql` runs inside one transaction with `XACT_ABORT OFF`, so a failed statement normally doesn't kill the transaction. A few SQL Server error classes roll it back anyway regardless of that setting (I hit this against real data — some literal-conversion errors do it even with `XACT_ABORT OFF`); the importer detects that via `@@TRANCOUNT` and reopens the transaction so the rest of the file still runs, with a warning that the earlier batches in that transaction got undone.

## Why it's not just `INSERT` statements under the hood

Benchmarked against a 3-million-row table (~172 MB of generated SQL):

| | Time | Peak memory |
|---|---|---|
| Export | 7.3s | 13.5 MB |
| Import | 38s | 13.5 MB |

Memory stays flat regardless of table size in both directions — everything streams row by row instead of buffering the file, so a 100 GB table costs about the same RAM as a 1 MB one.

Import is the more interesting half. A literal `INSERT INTO t VALUES (...),(...),...` statement is capped by SQL Server's own parse/compile cost for that statement, not by network or disk — I checked by running the exact same generated batch both through this tool and raw through `sqlcmd`, and got matching timings either way (~6,000 rows/sec). At that rate a few-hundred-million-row table takes hours. The only way past it is SQL Server's native bulk-copy protocol (the same one `bcp`/`BULK INSERT` use).

`data-import.sql` itself doesn't change — it's still plain INSERT statements. What changes is how import mode *runs* it: each batch gets parsed back into typed values and sent via bulk-copy instead of executed as literal SQL, but only when that can be done with real confidence, since I'd rather fall back to the slower proven path than risk getting a value wrong in a backup tool:

- Values are parsed using the actual destination column's SQL type (read from `sys.columns`), never guessed from what the literal looks like.
- Parsing happens entirely before any database call. Anything that doesn't parse cleanly — an unsupported type (`uniqueidentifier`, `sql_variant`, spatial types, fixed-length `char`/`binary`), or any shape mismatch — falls the whole batch back to plain SQL. Nothing gets sent twice.
- A failure *after* a bulk-copy attempt has already started is reported as a batch failure like any other, never retried as plain SQL — rows might already be on the server, and retrying could double-insert them.

## What's captured, and what isn't

Captured: tables, columns (type/length/precision/scale/collation), `IDENTITY`, computed columns, `DEFAULT`/`CHECK`/`UNIQUE`/`PRIMARY KEY` constraints, non-key indexes (including `INCLUDE` columns and filtered-index `WHERE` clauses), foreign keys with `ON DELETE`/`ON UPDATE` actions, and data for every SQL Server column type (`hierarchyid`/`geography`/`geometry`/`sql_variant` included, best-effort).

Not captured, on purpose — this backs up tables and data, not the whole database: views, stored procedures, functions, triggers, permissions, partitioning, full-text indexes, sequences, temporal tables, user-defined types/CLR types.

## Development

```bash
go build ./...
go vet ./...
go test ./...    # unit tests, fast, no database needed
```

Integration tests run against a dedicated, disposable SQL Server container — never real data, a separate container on its own port:

```bash
./scripts/integration-test.sh test          # starts the container if needed, runs the suite
./scripts/integration-test.sh test -v -run TestExportImportRoundTrip
./scripts/integration-test.sh down          # tears the container down
```

## License

MIT — see [LICENSE](LICENSE).
