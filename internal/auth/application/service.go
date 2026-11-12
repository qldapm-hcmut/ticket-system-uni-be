package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

const accessLifetime = 15 * time.Minute
const refreshLifetime = 7 * 24 * time.Hour

type Service struct {
	uow      UnitOfWork
	verifier IdentityVerifier
	tokens   TokenService
	clock    kernel.Clock
}

func NewService(uow UnitOfWork, verifier IdentityVerifier, tokens TokenService, clock kernel.Clock) *Service {
	return &Service{uow: uow, verifier: verifier, tokens: tokens, clock: clock}
}

func authError(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrIdentityConflict) {
		return ErrUnauthenticated
	}
	return err
}

func (s *Service) issue(ctx context.Context, r Repository, user User, now time.Time) (Tokens, error) {
	refresh, err := s.tokens.NewRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	access, err := s.tokens.AccessToken(user.ID, now.Add(accessLifetime))
	if err != nil {
		return Tokens{}, err
	}
	if _, err := r.CreateSession(ctx, user.ID, s.tokens.HashRefreshToken(refresh), now.Add(refreshLifetime)); err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(accessLifetime / time.Second)}, nil
}

func (s *Service) Login(ctx context.Context, idToken string) (AuthResult, error) {
	if strings.TrimSpace(idToken) == "" {
		return AuthResult{}, ErrUnauthenticated
	}
	// External verification must finish before beginning a database transaction.
	identity, err := s.verifier.Verify(ctx, idToken)
	if err != nil {
		if ctx.Err() != nil {
			return AuthResult{}, ctx.Err()
		}
		return AuthResult{}, ErrUnauthenticated
	}
	if identity.Sub == "" || identity.Email == "" {
		return AuthResult{}, ErrUnauthenticated
	}
	var result AuthResult
	// One bounded retry in a fresh transaction handles a competing first login.
	for attempt := 0; attempt < 2; attempt++ {
		err = s.uow.Do(ctx, func(ctx context.Context, r Repository) error {
			user, err := r.FindUserBySub(ctx, identity.Sub)
			if errors.Is(err, ErrNotFound) {
				user, err = r.FindUserByEmail(ctx, identity.Email)
				if errors.Is(err, ErrNotFound) {
					user, err = r.CreateUser(ctx, identity.Email, identity.FullName, identity.Sub)
				} else if err == nil {
					if !user.IsActive || (user.GoogleSub != "" && user.GoogleSub != identity.Sub) {
						return ErrUnauthenticated
					}
					if user.GoogleSub == "" {
						user, err = r.LinkIdentity(ctx, user.ID, identity.Sub)
					}
				}
			}
			if err != nil {
				return err
			}
			if !user.IsActive {
				return ErrUnauthenticated
			}
			tokens, err := s.issue(ctx, r, user, s.clock.Now())
			if err != nil {
				return err
			}
			result = AuthResult{Tokens: tokens, User: user}
			return nil
		})
		if !errors.Is(err, ErrIdentityConflict) && !errors.Is(err, ErrNotFound) {
			break
		}
	}
	if err != nil {
		return AuthResult{}, authError(err)
	}
	return result, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (AuthResult, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return AuthResult{}, ErrUnauthenticated
	}
	var result AuthResult
	err := s.uow.Do(ctx, func(ctx context.Context, r Repository) error {
		now := s.clock.Now()
		session, err := r.ConsumeSession(ctx, s.tokens.HashRefreshToken(refreshToken), now)
		if err != nil {
			return err
		}
		user, err := r.FindUserByID(ctx, session.UserID)
		if err != nil {
			return err
		}
		if !user.IsActive {
			return ErrUnauthenticated
		}
		tokens, err := s.issue(ctx, r, user, now)
		if err != nil {
			return err
		}
		result = AuthResult{Tokens: tokens, User: user}
		return nil
	})
	if err != nil {
		return AuthResult{}, authError(err)
	}
	return result, nil
}

// Logout's userID must come from authenticated server-side context, not the body.
func (s *Service) Logout(ctx context.Context, userID, refreshToken string) error {
	if userID == "" || strings.TrimSpace(refreshToken) == "" {
		return ErrUnauthenticated
	}
	return s.uow.Do(ctx, func(ctx context.Context, r Repository) error {
		return r.RevokeSession(ctx, userID, s.tokens.HashRefreshToken(refreshToken), s.clock.Now())
	})
}
