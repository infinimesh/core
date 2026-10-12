package graph

import "strconv"

const (
	defaultPageSize = 100
	maxPageSize     = 500
)

func cutPage[T any](items []T, size int32, token string) ([]T, string) {
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	off := 0
	if token != "" {
		n, err := strconv.Atoi(token)
		if err != nil || n < 0 {
			off = 0
		} else {
			off = n
		}
	}
	if off > len(items) {
		off = len(items)
	}
	end := off + int(size)
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	} else {
		end = len(items)
	}
	return items[off:end], next
}
