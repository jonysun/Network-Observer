package store

import "testing"

func TestTodoValidation(t *testing.T) {
	valid := TodoRecord{Title: "verify Linux", Phase: "验收", Status: "in_progress", Priority: "high", Progress: 70}
	if err := validateTodo(valid); err != nil { t.Fatal(err) }
	for _, item := range []TodoRecord{
		{Title: "", Status: "todo", Priority: "medium"},
		{Title: "bad status", Status: "started", Priority: "medium"},
		{Title: "bad priority", Status: "todo", Priority: "urgent"},
		{Title: "bad progress", Status: "todo", Priority: "medium", Progress: 101},
	} {
		if err := validateTodo(item); err == nil { t.Fatalf("expected validation failure for %+v", item) }
	}
}
