package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

const (
	// resetTokenBytes is the entropy of one password reset token. Only its
	// SHA-256 is stored, so a leaked database does not reveal a usable link.
	resetTokenBytes = 32
	// resetTokenTTL is how long a reset link stays valid.
	resetTokenTTL = time.Hour
	// maxTenantNameRunes bounds the display name a bootstrap stores.
	maxTenantNameRunes = 200
)

// Auth implements the authentication and provisioning use cases.
type Auth struct {
	tenants TenantStore
	users   UserStore
	tokens  ResetTokenStore
	sender  Sender
	clock   clock.Clock
}

// NewAuth wires the authentication use cases to their stores, outbound mail
// and clock.
func NewAuth(tenants TenantStore, users UserStore, tokens ResetTokenStore, sender Sender, clk clock.Clock) *Auth {
	return &Auth{tenants: tenants, users: users, tokens: tokens, sender: sender, clock: clk}
}

// TenantBySlug resolves a public slug, reporting domain.ErrTenantNotFound for
// an unknown one.
func (a *Auth) TenantBySlug(ctx context.Context, slug string) (domain.Tenant, error) {
	tenant, err := a.tenants.TenantBySlug(ctx, slug)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Tenant{}, domain.ErrTenantNotFound
	}
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("resolving tenant %q: %w", slug, err)
	}
	return tenant, nil
}

// Authenticate returns the login for email and password inside tenantID. An
// unknown email is verified against dummyPasswordHash and reported as
// domain.ErrInvalidCredentials, exactly like a wrong password, so neither the
// error nor the work done tells an attacker whether the email exists.
func (a *Auth) Authenticate(ctx context.Context, tenantID uuid.UUID, email, password string) (domain.User, error) {
	user, err := a.users.UserByEmail(ctx, tenantID, strings.TrimSpace(email))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// Burn one verification so timing does not reveal whether the login
		// exists.
		_ = VerifyPassword(dummyPasswordHash, password)
		return domain.User{}, domain.ErrInvalidCredentials
	case err != nil:
		return domain.User{}, fmt.Errorf("looking up login: %w", err)
	}
	if !VerifyPassword(user.PasswordHash, password) {
		return domain.User{}, domain.ErrInvalidCredentials
	}
	return user, nil
}

// CurrentUser re-reads the login behind a session. domain.ErrNotFound means
// the session no longer matches a login, which the web layer turns into a
// logout.
func (a *Auth) CurrentUser(ctx context.Context, tenantID, userID uuid.UUID) (domain.User, error) {
	user, err := a.users.UserByID(ctx, tenantID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("reading current login: %w", err)
	}
	return user, nil
}

// RequestPasswordReset mails a single-use reset link when email belongs to a
// login in tenant. An unknown email reports nil and sends nothing, so the
// endpoint cannot be used to enumerate accounts.
func (a *Auth) RequestPasswordReset(ctx context.Context, tenant domain.Tenant, email, baseURL string) error {
	email = strings.TrimSpace(email)
	user, err := a.users.UserByEmail(ctx, tenant.ID, email)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("looking up login for password reset: %w", err)
	}
	token, err := newResetToken()
	if err != nil {
		return err
	}
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := a.clock.Now().Add(resetTokenTTL)
	if err := a.tokens.IssueResetToken(ctx, tenant.ID, user.ID, tokenHash[:], expiresAt); err != nil {
		return fmt.Errorf("storing reset token: %w", err)
	}
	msg := domain.Message{
		To:      email,
		Subject: "Reset your Slotwise password",
		Body:    resetLink(baseURL, tenant.Slug, token),
	}
	if err := a.sender.Send(ctx, msg); err != nil {
		return fmt.Errorf("sending reset mail: %w", err)
	}
	return nil
}

// ResetPassword spends token and stores newPassword. Unknown, used and expired
// tokens are all domain.ErrResetTokenInvalid; the password policy is checked
// before the store is touched.
func (a *Auth) ResetPassword(ctx context.Context, tenantID uuid.UUID, token, newPassword string) error {
	if err := checkPasswordPolicy("password", newPassword); err != nil {
		return err
	}
	if token == "" {
		return domain.ErrResetTokenInvalid
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := a.tokens.ConsumeResetToken(ctx, tenantID, tokenHash[:], hash); err != nil {
		if errors.Is(err, domain.ErrResetTokenInvalid) {
			return domain.ErrResetTokenInvalid
		}
		return fmt.Errorf("consuming reset token: %w", err)
	}
	return nil
}

// Bootstrap provisions the tenant in in and its first owner login. It is
// idempotent: a repeat with the same slug reports created false and changes
// nothing, so two processes starting at once converge on one tenant and one
// owner. Whether any tenant exists cannot be queried without a tenant (RLS
// matches zero rows), so the environment-driven input is what starts
// provisioning.
func (a *Auth) Bootstrap(ctx context.Context, in BootstrapInput) (bool, error) {
	name, email, err := normalizeBootstrap(in)
	if err != nil {
		return false, err
	}
	hash, err := HashPassword(in.OwnerPassword)
	if err != nil {
		return false, err
	}
	tenantID := uuid.New()
	inserted, err := a.tenants.InsertTenant(ctx, domain.Tenant{
		ID:       tenantID,
		Slug:     in.Slug,
		Name:     name,
		Timezone: in.Timezone,
	})
	if err != nil {
		return false, fmt.Errorf("inserting tenant %q: %w", in.Slug, err)
	}
	if !inserted {
		existing, err := a.tenants.TenantBySlug(ctx, in.Slug)
		if err != nil {
			return false, fmt.Errorf("resolving existing tenant %q: %w", in.Slug, err)
		}
		tenantID = existing.ID
	}
	owner := domain.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Email:        email,
		PasswordHash: hash,
		Role:         domain.RoleOwner,
	}
	created, err := a.users.InsertUser(ctx, owner)
	if err != nil {
		return false, fmt.Errorf("inserting owner login: %w", err)
	}
	return created, nil
}

// normalizeBootstrap validates the provisioning input and returns the trimmed
// tenant name and the trimmed, lower-cased owner email. It never queries the
// stores, so it is also the whole input contract of Bootstrap.
func normalizeBootstrap(in BootstrapInput) (string, string, error) {
	name := strings.TrimSpace(in.Name)
	email := strings.ToLower(strings.TrimSpace(in.OwnerEmail))
	nameRunes := utf8.RuneCountInString(name)
	switch {
	case !ValidSlug(in.Slug):
		return "", "", domain.ValidationError{
			Field:   "slug",
			Message: "must be lowercase letters, digits and single hyphens, at most 63 characters",
		}
	case nameRunes == 0 || nameRunes > maxTenantNameRunes:
		return "", "", domain.ValidationError{Field: "name", Message: "must be between 1 and 200 characters"}
	case !validTimezone(in.Timezone):
		return "", "", domain.ValidationError{Field: "timezone", Message: "must be a valid IANA time zone"}
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return "", "", domain.ValidationError{Field: "owner_email", Message: "must be a valid email address"}
	}
	if err := checkPasswordPolicy("owner_password", in.OwnerPassword); err != nil {
		return "", "", err
	}
	return name, email, nil
}

// validTimezone reports whether tz names a zone the server can load. A blank
// value is rejected even though time.LoadLocation maps it to UTC: a tenant
// with no configured zone would silently compute its schedule in the wrong
// one.
func validTimezone(tz string) bool {
	if strings.TrimSpace(tz) == "" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// newResetToken returns resetTokenBytes of randomness as an unpadded
// URL-safe string.
func newResetToken() (string, error) {
	token := make([]byte, resetTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("generating reset token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}

// resetLink builds the public reset URL a login can click.
func resetLink(baseURL, slug, token string) string {
	return strings.TrimSuffix(baseURL, "/") + "/app/" + slug + "/reset?token=" + token
}
