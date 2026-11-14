package application

import (
	"context"
	"time"
)

type Filter struct {
	UnitID, Status, Search string
	Page, PageSize         int32
}
type CatalogFilter struct {
	Filter
	From, To      *time.Time
	AvailableOnly bool
}

type Reader interface {
	Catalog(context.Context, Actor, CatalogFilter) ([]map[string]any, int64, error)
	Units(context.Context, string) ([]map[string]any, error)
	Detail(context.Context, Actor, string) (map[string]any, error)
	Managed(context.Context, Actor, Filter) ([]map[string]any, int64, error)
	Pending(context.Context, Actor, Filter) ([]map[string]any, int64, error)
}
