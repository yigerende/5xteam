package httpapi

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"chatgpt-space-merge/internal/store"
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

func TestServerPaginationUsesPersistedDefaultAndAllowsRequestOverride(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	settings := dataStore.Settings()
	settings.DefaultPageSize = 50
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore}

	page := server.parsePagination(httptest.NewRequest("GET", "/api/items?page=2", nil))
	if page.PageSize != 50 || page.Limit != 50 || page.Offset != 50 {
		t.Fatalf("persisted default parsed as %+v", page)
	}
	overridden := server.parsePagination(httptest.NewRequest("GET", "/api/items?page=2&page_size=100", nil))
	if overridden.PageSize != 100 || overridden.Offset != 100 {
		t.Fatalf("request override parsed as %+v", overridden)
	}
}

func TestParsePaginationFallsBackForInvalidValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/items?page=-1&page_size=25", nil)
	page := parsePagination(r)
	if page.Page != 1 || page.PageSize != 10 || page.Offset != 0 {
		t.Fatalf("invalid values parsed as %+v", page)
	}
}
