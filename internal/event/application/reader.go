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
	Catalog(context.Context, CatalogFilter) ([]map[string]any, int64, error)
	Units(context.Context, string) ([]map[string]any, error)
	Detail(context.Context, string) (map[string]any, error)
	MyActiveRegistration(context.Context, string, string) (*RegistrationRef, error)
	Managed(context.Context, Actor, Filter) ([]map[string]any, int64, error)
}

type RegistrationRef struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	WaitlistPosition *int64 `json:"waitlistPosition"`
}
