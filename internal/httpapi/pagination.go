package httpapi

import (
	"net/http"
	"strconv"
)

type paginationParams struct {
	Page     int
	PageSize int
	Limit    int
	Offset   int
}

func parsePagination(r *http.Request) paginationParams {
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parsePositiveInt(r.URL.Query().Get("page_size"), 10)
	switch pageSize {
	case 10, 50, 100, 500:
	default:
		pageSize = 10
	}
	return paginationParams{Page: page, PageSize: pageSize, Limit: pageSize, Offset: (page - 1) * pageSize}
}

func (s *Server) parsePagination(r *http.Request) paginationParams {
	defaultSize := s.store.Settings().DefaultPageSize
	switch defaultSize {
	case 10, 50, 100, 500:
	default:
		defaultSize = 10
	}
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parsePositiveInt(r.URL.Query().Get("page_size"), defaultSize)
	switch pageSize {
	case 10, 50, 100, 500:
	default:
		pageSize = defaultSize
	}
	return paginationParams{Page: page, PageSize: pageSize, Limit: pageSize, Offset: (page - 1) * pageSize}
}

func paginationRequested(r *http.Request) bool {
	query := r.URL.Query()
	return query.Has("page") || query.Has("page_size")
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func paginatedData(items any, total int, page paginationParams, extra map[string]any) map[string]any {
	result := map[string]any{
		"items": items, "total": total, "page": page.Page, "page_size": page.PageSize,
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}
