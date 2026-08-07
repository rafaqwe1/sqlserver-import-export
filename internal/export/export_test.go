package export

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rafaqwe1/sqlserver-import-export/internal/meta"
)

func TestParseTableRefList(t *testing.T) {
	got := parseTableRefList("dbo.Customers, Orders ,,  sales.Invoices  ", ",")
	want := []meta.TableRef{
		{Schema: "dbo", Name: "Customers"},
		{Schema: "dbo", Name: "Orders"},
		{Schema: "sales", Name: "Invoices"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestParseTablesArg_InlineList(t *testing.T) {
	refs, err := parseTablesArg("Table1, Table_2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []meta.TableRef{
		{Schema: "dbo", Name: "Table1"},
		{Schema: "dbo", Name: "Table_2"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("got %#v, want %#v", refs, want)
	}
}

func TestParseTablesArg_FilePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tables.txt")
	content := "dbo.Customers\n-- a comment\n\nOrders\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	refs, err := parseTablesArg(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []meta.TableRef{
		{Schema: "dbo", Name: "Customers"},
		{Schema: "dbo", Name: "Orders"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("got %#v, want %#v", refs, want)
	}
}
