package importrun

import (
	"io"
	"strings"
	"testing"
)

func collectBatches(t *testing.T, content string) []string {
	t.Helper()
	br := newBatchReader(strings.NewReader(content))
	var got []string
	for {
		batch, err := br.Next()
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got = append(got, batch)
	}
}

func assertBatches(t *testing.T, content string, want []string) {
	t.Helper()
	got := collectBatches(t, content)
	if len(got) != len(want) {
		t.Fatalf("got %d batches, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("batch %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBatchReader(t *testing.T) {
	content := "CREATE TABLE [dbo].[T1] (x int);\nGO\n\nCREATE TABLE [dbo].[T2] (y int);\nGO 5\n\n\n-- trailing comment only\n"
	assertBatches(t, content, []string{
		"CREATE TABLE [dbo].[T1] (x int);",
		"CREATE TABLE [dbo].[T2] (y int);",
		"-- trailing comment only",
	})
}

func TestBatchReader_NoTrailingGO(t *testing.T) {
	assertBatches(t, "BEGIN TRANSACTION;\nGO\nCOMMIT TRANSACTION;", []string{
		"BEGIN TRANSACTION;", "COMMIT TRANSACTION;",
	})
}

func TestBatchReader_CaseInsensitiveAndWhitespace(t *testing.T) {
	assertBatches(t, "SELECT 1;\n  go  \nSELECT 2;\nGo\n", []string{
		"SELECT 1;", "SELECT 2;",
	})
}

func TestBatchReader_HugeSingleLine(t *testing.T) {
	huge := strings.Repeat("x", 5*1024*1024)
	content := "INSERT INTO T VALUES ('" + huge + "');\nGO\n"
	got := collectBatches(t, content)
	if len(got) != 1 {
		t.Fatalf("got %d batches, want 1", len(got))
	}
	if !strings.Contains(got[0], huge) {
		t.Error("huge line was not preserved intact")
	}
}

func TestTableMarker(t *testing.T) {
	batch := "-- TABLE: dbo.Orders (~42 rows)\nSET IDENTITY_INSERT [dbo].[Orders] ON;"
	table, ok := tableMarker(batch)
	if !ok || table != "dbo.Orders" {
		t.Fatalf("expected to capture dbo.Orders, got %q, %v", table, ok)
	}

	if _, ok := tableMarker("INSERT INTO [dbo].[Orders] (...) VALUES (...);"); ok {
		t.Error("expected no marker in a plain INSERT batch")
	}
}
