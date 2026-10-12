package graph

import "testing"

func TestCutPage(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	page, next := cutPage(items, 2, "")
	if len(page) != 2 || page[0] != 1 || next != "2" {
		t.Fatalf("first page %v %s", page, next)
	}
	page, next = cutPage(items, 2, next)
	if len(page) != 2 || page[0] != 3 || next != "4" {
		t.Fatalf("second page %v %s", page, next)
	}
	page, next = cutPage(items, 2, "4")
	if len(page) != 1 || page[0] != 5 || next != "" {
		t.Fatalf("last page %v %s", page, next)
	}
	page, next = cutPage(items, 0, "")
	if len(page) != 5 || next != "" {
		t.Fatalf("default page %v %s", page, next)
	}
}
