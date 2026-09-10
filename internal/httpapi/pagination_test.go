package httpapi

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestParsePaginationAllowsConfiguredSizes(t *testing.T) {
	for _, size := range []int{10, 50, 100, 500} {
		r := httptest.NewRequest("GET", "/api/items?page=3&page_size="+strconv.Itoa(size), nil)
		page := parsePagination(r)
		if page.Page != 3 || page.PageSize != size || page.Limit != size || page.Offset != size*2 {
			t.Fatalf("size %d parsed as %+v", size, page)
		}
	}
}

func TestParsePaginationFallsBackForInvalidValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/items?page=-1&page_size=25", nil)
	page := parsePagination(r)
	if page.Page != 1 || page.PageSize != 10 || page.Offset != 0 {
		t.Fatalf("invalid values parsed as %+v", page)
	}
}
