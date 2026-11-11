package application

import (
	"context"
	"errors"
	"testing"
)

type mineFake struct {
	RegistrationRepository
	do       func(context.Context, func(context.Context, RegistrationRepository) error) error
	mine     func(context.Context, string, MineFilter) ([]MyRegistration, int64, error)
	active   func(context.Context, string, string) (Registration, error)
	register func(context.Context, string, string) (Registration, error)
}

func (f *mineFake) Do(ctx context.Context, fn func(context.Context, RegistrationRepository) error) error {
	if f.do != nil {
		return f.do(ctx, fn)
	}
	return fn(ctx, f)
}
func (f *mineFake) Mine(ctx context.Context, s string, q MineFilter) ([]MyRegistration, int64, error) {
	return f.mine(ctx, s, q)
}
func (f *mineFake) Active(ctx context.Context, e, s string) (Registration, error) {
	return f.active(ctx, e, s)
}
func (f *mineFake) Register(ctx context.Context, e, s string) (Registration, error) {
	return f.register(ctx, e, s)
}

func TestMineValidatesBeforeRepository(t *testing.T) {
	for _, f := range []MineFilter{{Page: 0, PageSize: 20}, {Page: 1, PageSize: 0}, {Page: 1, PageSize: 101}, {Page: 2147483647, PageSize: 100}, {Page: 1, PageSize: 20, Status: "unknown"}} {
		svc := New(&mineFake{do: func(context.Context, func(context.Context, RegistrationRepository) error) error {
			t.Fatal("invalid filter reached DB")
			return nil
		}})
		if _, _, err := svc.Mine(context.Background(), "owner", f); !errors.Is(err, ErrValidation) {
			t.Fatal(err)
		}
	}
}
func TestMinePreservesOwnedFilterAndNonNilRows(t *testing.T) {
	f := MineFilter{Page: 3, PageSize: 2, Status: "EXPIRED", Upcoming: true}
	fake := &mineFake{mine: func(_ context.Context, owner string, got MineFilter) ([]MyRegistration, int64, error) {
		if owner != "owner" || got != f {
			t.Fatalf("owner=%s filter=%+v", owner, got)
		}
		return nil, 5, nil
	}}
	rows, n, err := New(fake).Mine(context.Background(), "owner", f)
	if err != nil || n != 5 || rows == nil {
		t.Fatalf("rows=%v n=%d err=%v", rows, n, err)
	}
}
func TestRegisterDuplicateCarriesID(t *testing.T) {
	fake := &mineFake{active: func(context.Context, string, string) (Registration, error) { return Registration{ID: "existing"}, nil }}
	_, err := New(fake).Register(context.Background(), "owner", "event")
	var dup *AlreadyRegistered
	if !errors.As(err, &dup) || dup.RegistrationID != "existing" || !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("%v", err)
	}
}
func TestRegisterLostDuplicateWinnerRemainsConflict(t *testing.T) {
	for _, rereadErr := range []error{ErrNotFound, errors.New("database unavailable")} {
		t.Run(rereadErr.Error(), func(t *testing.T) {
			attempts := 0
			fake := &mineFake{}
			fake.do = func(ctx context.Context, fn func(context.Context, RegistrationRepository) error) error {
				attempts++
				return fn(ctx, fake)
			}
			fake.active = func(context.Context, string, string) (Registration, error) {
				if attempts == 1 {
					return Registration{}, ErrNotFound
				}
				return Registration{}, rereadErr
			}
			fake.register = func(context.Context, string, string) (Registration, error) { return Registration{}, ErrAlreadyActive }
			_, err := New(fake).Register(context.Background(), "owner", "event")
			want := rereadErr
			if errors.Is(rereadErr, ErrNotFound) {
				want = ErrAlreadyActive
			}
			var duplicate *AlreadyRegistered
			if attempts != 2 || !errors.Is(err, want) || errors.As(err, &duplicate) {
				t.Fatalf("attempts=%d err=%v want=%v (without details)", attempts, err, want)
			}
		})
	}
}

func TestRegisterInsertRaceRereadsAfterRollback(t *testing.T) {
	finished, attempts := false, 0
	fake := &mineFake{}
	fake.do = func(ctx context.Context, fn func(context.Context, RegistrationRepository) error) error {
		attempts++
		err := fn(ctx, fake)
		if attempts == 1 {
			finished = true
		}
		return err
	}
	fake.active = func(context.Context, string, string) (Registration, error) {
		if attempts == 1 {
			return Registration{}, ErrNotFound
		}
		if !finished {
			t.Fatal("re-read before failed transaction finished")
		}
		return Registration{ID: "concurrent-winner"}, nil
	}
	fake.register = func(context.Context, string, string) (Registration, error) { return Registration{}, ErrAlreadyActive }
	_, err := New(fake).Register(context.Background(), "owner", "event")
	var dup *AlreadyRegistered
	if attempts != 2 || !errors.As(err, &dup) || dup.RegistrationID != "concurrent-winner" {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
