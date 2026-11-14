package store

import "testing"

func TestSplitStatementsIgnoresTrailingWhitespace(t *testing.T) {
	statements := migrationStatements("CREATE DATABASE observer;\n\nCREATE TABLE sample (id UInt8);\n")
	if len(statements) != 2 {
		t.Fatalf("expected two SQL statements, got %d", len(statements))
	}
	if statements[0] != "CREATE DATABASE observer" {
		t.Fatalf("unexpected first statement: %q", statements[0])
	}
	if statements[1] != "CREATE TABLE sample (id UInt8)" {
		t.Fatalf("unexpected second statement: %q", statements[1])
	}
}
