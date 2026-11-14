package application

import (
	"context"
	"errors"
	"time"
)

var ErrValidation = errors.New("invalid notification options")

type Notification struct {
	ID        string     `json:"id"`
	EventID   *string    `json:"eventId"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	ReadAt    *time.Time `json:"readAt"`
	CreatedAt time.Time  `json:"createdAt"`
}
type Filter struct {
	Page, PageSize int32
	Unread         bool
}
type List struct {
	Items                   []Notification
	TotalItems, UnreadCount int64
}
type ReadResult struct {
	UpdatedCount int64 `json:"updatedCount"`
	UnreadCount  int64 `json:"unreadCount"`
}
type ReadOptions struct {
	IDs []string
	All bool
}
type Store interface {
	List(context.Context, string, Filter) (List, error)
	MarkRead(context.Context, string, ReadOptions) (ReadResult, error)
}
type Service struct{ store Store }

func New(store Store) *Service { return &Service{store} }
func (s *Service) List(ctx context.Context, actor string, filter Filter) (List, error) {
	if filter.Page < 1 || filter.PageSize < 1 || filter.PageSize > 100 {
		return List{}, ErrValidation
	}
	// The SQL offset is int32; calculate in int64 before checking its limit.
	offset := (int64(filter.Page) - 1) * int64(filter.PageSize)
	if offset > 2147483647 {
		return List{}, ErrValidation
	}
	return s.store.List(ctx, actor, filter)
}

func (s *Service) MarkRead(ctx context.Context, actor string, options ReadOptions) (ReadResult, error) {
	hasIDs := len(options.IDs) > 0
	bothChoices := options.All && hasIDs
	neitherChoice := !options.All && !hasIDs
	if bothChoices || neitherChoice || len(options.IDs) > 100 {
		return ReadResult{}, ErrValidation
	}
	for _, id := range options.IDs {
		if !validUUID(id) {
			return ReadResult{}, ErrValidation
		}
	}
	return s.store.MarkRead(ctx, actor, options)
}
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
