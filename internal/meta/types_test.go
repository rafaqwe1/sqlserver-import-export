package meta

import "testing"

func TestFormatColumnType(t *testing.T) {
	cases := []struct {
		name string
		col  Column
		want string
	}{
		{"varchar(n)", Column{TypeName: "varchar", MaxLength: 50}, "varchar(50)"},
		{"varchar(max)", Column{TypeName: "varchar", MaxLength: -1}, "varchar(MAX)"},
		{"nvarchar(n) halves byte length", Column{TypeName: "nvarchar", MaxLength: 100}, "nvarchar(50)"},
		{"nvarchar(max)", Column{TypeName: "nvarchar", MaxLength: -1}, "nvarchar(MAX)"},
		{"decimal(p,s)", Column{TypeName: "decimal", Precision: 10, Scale: 2}, "decimal(10,2)"},
		{"datetime2(scale)", Column{TypeName: "datetime2", Scale: 7}, "datetime2(7)"},
		{"int passthrough", Column{TypeName: "int"}, "int"},
		{"bit passthrough", Column{TypeName: "bit"}, "bit"},
		{"binary(n)", Column{TypeName: "binary", MaxLength: 16}, "binary(16)"},
		{"varbinary(max)", Column{TypeName: "varbinary", MaxLength: -1}, "varbinary(MAX)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FormatColumnType(c.col)
			if got != c.want {
				t.Errorf("FormatColumnType(%+v) = %q, want %q", c.col, got, c.want)
			}
		})
	}
}

func TestIsCharType(t *testing.T) {
	for _, tn := range []string{"char", "varchar", "text", "nchar", "nvarchar", "ntext"} {
		if !IsCharType(tn) {
			t.Errorf("IsCharType(%q) = false, want true", tn)
		}
	}
	for _, tn := range []string{"int", "decimal", "datetime2", "varbinary"} {
		if IsCharType(tn) {
			t.Errorf("IsCharType(%q) = true, want false", tn)
		}
	}
}
