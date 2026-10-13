package main

import (
	"testing"
	"time"
)

func TestCategoryTree(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(_ Category, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutCategory(Category{Name: "Scacchi"}))
	must(s.PutCategory(Category{Name: "Finali", Parent: "scacchi"}))
	must(s.PutCategory(Category{Name: "Torri", Parent: "finali"}))
	must(s.PutCategory(Category{Name: "Informatica"}))

	if _, err := s.PatchCategory("scacchi", CategoryPatch{Parent: ptr("torri")}); err == nil {
		t.Error("expected cycle to be rejected")
	}
	if _, err := s.PutCategory(Category{Name: "X", Parent: "nope"}); err == nil {
		t.Error("expected unknown parent to be rejected")
	}

	now := time.Now()
	for i, c := range []string{"scacchi", "finali", "torri", "torri", "informatica"} {
		if err := s.Add(&Book{ID: string(rune('a' + i)), Title: "b", Category: c, AddedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	all := s.Categories("")
	var paths []string
	for _, c := range all {
		paths = append(paths, c.Path)
	}
	want := []string{"Informatica", "Scacchi", "Scacchi / Finali", "Scacchi / Finali / Torri"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
	if all[1].Total != 4 || all[1].Count != 1 {
		t.Errorf("scacchi count/total = %d/%d", all[1].Count, all[1].Total)
	}
	if n := len(s.List(Query{Category: "finali", Recursive: true})); n != 3 {
		t.Errorf("recursive finali = %d books", n)
	}
	if n := len(s.List(Query{Category: "finali"})); n != 1 {
		t.Errorf("direct finali = %d books", n)
	}
	if kids := s.Children("", ""); len(kids) != 2 {
		t.Errorf("roots = %v", kids)
	}

	if err := s.DeleteCategory("finali"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Category("torri"); c.Parent != "scacchi" {
		t.Errorf("torri parent = %q", c.Parent)
	}
	if b, _ := s.Get("b"); b.Category != "scacchi" {
		t.Errorf("book moved to %q", b.Category)
	}
}

func ptr[T any](v T) *T { return &v }
