package export

import (
	"testing"

	"github.com/rafaqwe1/sqlserver-import-export/internal/meta"
)

func mustIndexOf(t *testing.T, order []*meta.Table, schema, name string) int {
	t.Helper()
	for i, tbl := range order {
		if tbl.Schema == schema && tbl.Name == name {
			return i
		}
	}
	t.Fatalf("table %s.%s not found in order", schema, name)
	return -1
}

func TestOrderTablesForInsert_SimpleParentChild(t *testing.T) {
	customers := &meta.Table{Schema: "dbo", Name: "Customers"}
	orders := &meta.Table{
		Schema: "dbo", Name: "Orders",
		ForeignKeys: []meta.ForeignKey{
			{Name: "FK_Orders_Customers", RefSchema: "dbo", RefTable: "Customers"},
		},
	}

	order, deferred := orderTablesForInsert([]*meta.Table{orders, customers})

	if len(deferred) != 0 {
		t.Fatalf("expected no deferred tables, got %v", deferred)
	}
	ci, oi := mustIndexOf(t, order, "dbo", "Customers"), mustIndexOf(t, order, "dbo", "Orders")
	if ci >= oi {
		t.Errorf("Customers must come before Orders: customers at %d, orders at %d", ci, oi)
	}
}

func TestOrderTablesForInsert_SelfReference(t *testing.T) {
	employees := &meta.Table{
		Schema: "dbo", Name: "Employees",
		ForeignKeys: []meta.ForeignKey{
			{Name: "FK_Employees_Manager", RefSchema: "dbo", RefTable: "Employees"},
		},
	}

	order, deferred := orderTablesForInsert([]*meta.Table{employees})

	if len(order) != 1 {
		t.Fatalf("expected 1 table in order, got %d", len(order))
	}
	if !deferred["dbo.employees"] {
		t.Errorf("expected dbo.employees to be deferred, got %v", deferred)
	}
}

func TestOrderTablesForInsert_MultiTableCycle(t *testing.T) {
	a := &meta.Table{
		Schema: "dbo", Name: "A",
		ForeignKeys: []meta.ForeignKey{{Name: "FK_A_B", RefSchema: "dbo", RefTable: "B"}},
	}
	b := &meta.Table{
		Schema: "dbo", Name: "B",
		ForeignKeys: []meta.ForeignKey{{Name: "FK_B_A", RefSchema: "dbo", RefTable: "A"}},
	}

	order, deferred := orderTablesForInsert([]*meta.Table{a, b})

	if len(order) != 2 {
		t.Fatalf("expected 2 tables in order, got %d", len(order))
	}
	if !deferred["dbo.a"] || !deferred["dbo.b"] {
		t.Errorf("expected both dbo.a and dbo.b to be deferred, got %v", deferred)
	}
}

func TestOrderTablesForInsert_FKOutsideExportedSet(t *testing.T) {
	orders := &meta.Table{
		Schema: "dbo", Name: "Orders",
		ForeignKeys: []meta.ForeignKey{
			{Name: "FK_Orders_Customers", RefSchema: "dbo", RefTable: "Customers"}, // not in the exported set
		},
	}

	order, deferred := orderTablesForInsert([]*meta.Table{orders})

	if len(order) != 1 || order[0] != orders {
		t.Fatalf("expected only Orders in order, got %v", order)
	}
	if len(deferred) != 0 {
		t.Errorf("expected no deferred tables (FK target not exported, no ordering constraint), got %v", deferred)
	}
}

func TestOrderTablesForInsert_Chain(t *testing.T) {
	// C -> B -> A (C depends on B, B depends on A)
	a := &meta.Table{Schema: "dbo", Name: "A"}
	b := &meta.Table{
		Schema: "dbo", Name: "B",
		ForeignKeys: []meta.ForeignKey{{Name: "FK_B_A", RefSchema: "dbo", RefTable: "A"}},
	}
	c := &meta.Table{
		Schema: "dbo", Name: "C",
		ForeignKeys: []meta.ForeignKey{{Name: "FK_C_B", RefSchema: "dbo", RefTable: "B"}},
	}

	order, deferred := orderTablesForInsert([]*meta.Table{c, a, b})
	if len(deferred) != 0 {
		t.Fatalf("expected no deferred tables, got %v", deferred)
	}
	ai, bi, ci := mustIndexOf(t, order, "dbo", "A"), mustIndexOf(t, order, "dbo", "B"), mustIndexOf(t, order, "dbo", "C")
	if !(ai < bi && bi < ci) {
		t.Errorf("expected order A, B, C; got indices A=%d B=%d C=%d", ai, bi, ci)
	}
}
