package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// testNow is the fake clock's reading in every test that needs one.
var testNow = time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)

// testBaseURL is the public base URL the reset tests mail links for.
const testBaseURL = "http://localhost:8080"

// mailedResetToken returns the token from the reset link in body, failing the
// test when body does not carry one.
func mailedResetToken(t *testing.T, body string) string {
	t.Helper()
	prefix := testBaseURL + "/app/acme/reset?token="
	token, found := strings.CutPrefix(body, prefix)
	if !found || token == "" {
		t.Fatalf("mail body %q is not a reset link starting with %q", body, prefix)
	}
	return token
}

// fakeTenantStore is an in-memory TenantStore.
type fakeTenantStore struct {
	bySlug    map[string]domain.Tenant
	insertErr error
	lookupErr error
	inserted  []domain.Tenant
}

func (f *fakeTenantStore) TenantBySlug(_ context.Context, slug string) (domain.Tenant, error) {
	if f.lookupErr != nil {
		return domain.Tenant{}, f.lookupErr
	}
	tenant, found := f.bySlug[slug]
	if !found {
		return domain.Tenant{}, domain.ErrNotFound
	}
	return tenant, nil
}

func (f *fakeTenantStore) InsertTenant(_ context.Context, tenant domain.Tenant) (bool, error) {
	if f.insertErr != nil {
		return false, f.insertErr
	}
	f.inserted = append(f.inserted, tenant)
	if f.bySlug == nil {
		f.bySlug = make(map[string]domain.Tenant)
	}
	if _, exists := f.bySlug[tenant.Slug]; exists {
		return false, nil
	}
	f.bySlug[tenant.Slug] = tenant
	return true, nil
}

// fakeUserStore is an in-memory UserStore.
type fakeUserStore struct {
	users      []domain.User
	insertErr  error
	lookupErr  error
	inserted   []domain.User
	lastLookup string
}

func (f *fakeUserStore) UserByEmail(_ context.Context, tenantID uuid.UUID, email string) (domain.User, error) {
	f.lastLookup = email
	if f.lookupErr != nil {
		return domain.User{}, f.lookupErr
	}
	for _, user := range f.users {
		if user.TenantID == tenantID && strings.EqualFold(user.Email, email) {
			return user, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeUserStore) UserByID(_ context.Context, tenantID, userID uuid.UUID) (domain.User, error) {
	if f.lookupErr != nil {
		return domain.User{}, f.lookupErr
	}
	for _, user := range f.users {
		if user.TenantID == tenantID && user.ID == userID {
			return user, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeUserStore) InsertUser(_ context.Context, user domain.User) (bool, error) {
	if f.insertErr != nil {
		return false, f.insertErr
	}
	f.inserted = append(f.inserted, user)
	for _, existing := range f.users {
		if existing.TenantID == user.TenantID && strings.EqualFold(existing.Email, user.Email) {
			return false, nil
		}
	}
	f.users = append(f.users, user)
	return true, nil
}

// issuedReset is one recorded IssueResetToken call.
type issuedReset struct {
	tenantID  uuid.UUID
	userID    uuid.UUID
	tokenHash []byte
	expiresAt time.Time
}

// fakeResetTokenStore is an in-memory ResetTokenStore that also records what it
// was asked to store and consume.
type fakeResetTokenStore struct {
	issueErr      error
	consumeErr    error
	consumeTenant uuid.UUID
	consumeFor    uuid.UUID
	issued        []issuedReset
	consumed      [][]byte
	lastHash      string
}

func (f *fakeResetTokenStore) IssueResetToken(_ context.Context, tenantID, userID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	if f.issueErr != nil {
		return f.issueErr
	}
	f.issued = append(f.issued, issuedReset{
		tenantID:  tenantID,
		userID:    userID,
		tokenHash: tokenHash,
		expiresAt: expiresAt,
	})
	return nil
}

func (f *fakeResetTokenStore) ConsumeResetToken(_ context.Context, tenantID uuid.UUID, tokenHash []byte, passwordHash string) (uuid.UUID, error) {
	f.consumeTenant = tenantID
	f.consumed = append(f.consumed, tokenHash)
	f.lastHash = passwordHash
	if f.consumeErr != nil {
		return uuid.Nil, f.consumeErr
	}
	return f.consumeFor, nil
}

// fakeSender records the mail the use case asked it to deliver.
type fakeSender struct {
	messages []domain.Message
	sendErr  error
}

func (f *fakeSender) Send(_ context.Context, msg domain.Message) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.messages = append(f.messages, msg)
	return nil
}

// authFixture bundles the use case under test with the fakes it writes to.
type authFixture struct {
	auth    *Auth
	tenants *fakeTenantStore
	users   *fakeUserStore
	tokens  *fakeResetTokenStore
	sender  *fakeSender
	clock   *clock.Fake
}

// newAuthFixture returns an Auth over empty fakes with the clock stopped at
// testNow.
func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	fixture := &authFixture{
		tenants: &fakeTenantStore{},
		users:   &fakeUserStore{},
		tokens:  &fakeResetTokenStore{},
		sender:  &fakeSender{},
		clock:   clock.NewFake(testNow),
	}
	fixture.auth = NewAuth(fixture.tenants, fixture.users, fixture.tokens, fixture.sender, fixture.clock)
	return fixture
}

// ownerLogin is a stored owner login.
func ownerLogin(tenantID uuid.UUID, email, passwordHash string) domain.User {
	return domain.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         domain.RoleOwner,
	}
}

// mustHashPassword hashes plain or fails the test.
func mustHashPassword(t *testing.T, plain string) string {
	t.Helper()
	hash, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return hash
}

func TestTenantBySlugMapsNotFound(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	tenant := domain.Tenant{ID: uuid.New(), Slug: "acme", Name: "Acme", Timezone: "Europe/Berlin"}
	f.tenants.bySlug = map[string]domain.Tenant{"acme": tenant}

	got, err := f.auth.TenantBySlug(t.Context(), "acme")
	if err != nil {
		t.Fatalf("TenantBySlug: %v", err)
	}
	if got != tenant {
		t.Errorf("TenantBySlug = %#v, want %#v", got, tenant)
	}
	if _, err := f.auth.TenantBySlug(t.Context(), "missing"); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("TenantBySlug(missing) error = %v, want domain.ErrTenantNotFound", err)
	}
}

func TestAuthenticateAcceptsTheRightPassword(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	tenantID := uuid.New()
	login := ownerLogin(tenantID, "owner@example.com", mustHashPassword(t, "correct-horse-battery"))
	f.users.users = []domain.User{login}

	got, err := f.auth.Authenticate(t.Context(), tenantID, "Owner@Example.com", "correct-horse-battery")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != login.ID {
		t.Errorf("Authenticate = %v, want the stored login %v", got.ID, login.ID)
	}
}

func TestAuthenticateTrimsTheEmail(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	tenantID := uuid.New()
	f.users.users = []domain.User{ownerLogin(tenantID, "owner@example.com", mustHashPassword(t, "correct-horse-battery"))}

	if _, err := f.auth.Authenticate(t.Context(), tenantID, "  owner@example.com  ", "correct-horse-battery"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if f.users.lastLookup != "owner@example.com" {
		t.Errorf("lookup email = %q, want the trimmed %q", f.users.lastLookup, "owner@example.com")
	}
}

func TestAuthenticateRejectsUnknownEmailAndWrongPassword(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	hash := mustHashPassword(t, "correct-horse-battery")
	tests := []struct {
		name     string
		email    string
		password string
	}{
		{name: "unknown email", email: "nobody@example.com", password: "correct-horse-battery"},
		{name: "wrong password", email: "owner@example.com", password: "wrong-horse-battery"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAuthFixture(t)
			f.users.users = []domain.User{ownerLogin(tenantID, "owner@example.com", hash)}
			_, err := f.auth.Authenticate(t.Context(), tenantID, tt.email, tt.password)
			if !errors.Is(err, domain.ErrInvalidCredentials) {
				t.Fatalf("error = %v, want domain.ErrInvalidCredentials", err)
			}
		})
	}
}

func TestAuthenticateWrapsStoreFailures(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	boom := errors.New("connection reset")
	f.users.lookupErr = boom

	if _, err := f.auth.Authenticate(t.Context(), uuid.New(), "owner@example.com", "correct-horse-battery"); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to wrap %v", err, boom)
	}
}

func TestCurrentUserNotFoundStaysNotFound(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	_, err := f.auth.CurrentUser(t.Context(), uuid.New(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want domain.ErrNotFound", err)
	}
}

func TestRequestPasswordResetForUnknownEmailSendsNothing(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	tenant := domain.Tenant{ID: uuid.New(), Slug: "acme"}

	if err := f.auth.RequestPasswordReset(t.Context(), tenant, "nobody@example.com", testBaseURL); err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}
	if len(f.sender.messages) != 0 {
		t.Errorf("sent %d mails, want 0", len(f.sender.messages))
	}
	if len(f.tokens.issued) != 0 {
		t.Errorf("issued %d tokens, want 0", len(f.tokens.issued))
	}
}

func TestRequestPasswordResetMailsAStoredToken(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	tenant := domain.Tenant{ID: uuid.New(), Slug: "acme", Name: "Acme"}
	login := ownerLogin(tenant.ID, "owner@example.com", "not-used")
	f.users.users = []domain.User{login}

	if err := f.auth.RequestPasswordReset(t.Context(), tenant, " owner@example.com ", testBaseURL+"/"); err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}
	if len(f.sender.messages) != 1 {
		t.Fatalf("sent %d mails, want 1", len(f.sender.messages))
	}
	mail := f.sender.messages[0]
	if mail.To != "owner@example.com" {
		t.Errorf("To = %q, want the trimmed address", mail.To)
	}
	if f.users.lastLookup != "owner@example.com" {
		t.Errorf("lookup email = %q, want the trimmed address", f.users.lastLookup)
	}
	if mail.Subject != "Reset your Slotwise password" {
		t.Errorf("Subject = %q", mail.Subject)
	}
	raw, err := base64.RawURLEncoding.DecodeString(mailedResetToken(t, mail.Body))
	if err != nil || len(raw) != 32 {
		t.Errorf("mailed token decodes to %d bytes (error %v), want 32 random bytes", len(raw), err)
	}
}

func TestRequestPasswordResetStoresTheTokenHash(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	tenant := domain.Tenant{ID: uuid.New(), Slug: "acme", Name: "Acme"}
	login := ownerLogin(tenant.ID, "owner@example.com", "not-used")
	f.users.users = []domain.User{login}

	if err := f.auth.RequestPasswordReset(t.Context(), tenant, "owner@example.com", testBaseURL); err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}
	if len(f.sender.messages) != 1 || len(f.tokens.issued) != 1 {
		t.Fatalf("sent %d mails and issued %d tokens, want 1 and 1", len(f.sender.messages), len(f.tokens.issued))
	}
	token := mailedResetToken(t, f.sender.messages[0].Body)
	issued := f.tokens.issued[0]
	sum := sha256.Sum256([]byte(token))
	if !bytes.Equal(issued.tokenHash, sum[:]) {
		t.Error("the stored hash is not the SHA-256 of the mailed token")
	}
	if issued.tenantID != tenant.ID || issued.userID != login.ID {
		t.Errorf("token issued for %v/%v, want %v/%v", issued.tenantID, issued.userID, tenant.ID, login.ID)
	}
	if want := f.clock.Now().Add(time.Hour); !issued.expiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", issued.expiresAt, want)
	}
}

func TestResetPasswordStoresTokenHashAndNewPassword(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	tenantID := uuid.New()

	if err := f.auth.ResetPassword(t.Context(), tenantID, "raw-token", "new-horse-battery"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if len(f.tokens.consumed) != 1 {
		t.Fatalf("consumed %d tokens, want 1", len(f.tokens.consumed))
	}
	sum := sha256.Sum256([]byte("raw-token"))
	if !bytes.Equal(f.tokens.consumed[0], sum[:]) {
		t.Error("the store was asked to consume something other than the token's SHA-256")
	}
	if f.tokens.consumeTenant != tenantID {
		t.Errorf("consumed tenant = %v, want %v", f.tokens.consumeTenant, tenantID)
	}
	if !VerifyPassword(f.tokens.lastHash, "new-horse-battery") {
		t.Error("the stored password hash does not verify the new password")
	}
}

func TestResetPasswordMapsAnInvalidToken(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	f.tokens.consumeErr = domain.ErrResetTokenInvalid

	err := f.auth.ResetPassword(t.Context(), uuid.New(), "raw-token", "new-horse-battery")
	if !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Fatalf("error = %v, want domain.ErrResetTokenInvalid", err)
	}
}

func TestResetPasswordRejectsShortPasswordBeforeConsuming(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)

	err := f.auth.ResetPassword(t.Context(), uuid.New(), "raw-token", "short")
	assertValidationError(t, err, "password")
	if len(f.tokens.consumed) != 0 {
		t.Errorf("consumed %d tokens, want 0", len(f.tokens.consumed))
	}
	if f.tokens.lastHash != "" {
		t.Error("a password hash reached the store before the policy passed")
	}
}

func TestResetPasswordRejectsEmptyToken(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)

	err := f.auth.ResetPassword(t.Context(), uuid.New(), "", "new-horse-battery")
	if !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Fatalf("error = %v, want domain.ErrResetTokenInvalid", err)
	}
	if len(f.tokens.consumed) != 0 {
		t.Errorf("consumed %d tokens, want 0", len(f.tokens.consumed))
	}
}

// bootstrapInput is the smallest input Bootstrap accepts.
func bootstrapInput() BootstrapInput {
	return BootstrapInput{
		Slug:          "acme",
		Name:          "Acme",
		Timezone:      "Europe/Berlin",
		OwnerEmail:    "owner@example.com",
		OwnerPassword: "correct-horse-battery",
	}
}

func TestBootstrapCreatesTenantAndOwner(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)

	created, err := f.auth.Bootstrap(t.Context(), bootstrapInput())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !created {
		t.Error("Bootstrap reported created=false on an empty database")
	}
	if len(f.tenants.inserted) != 1 {
		t.Fatalf("inserted %d tenants, want 1", len(f.tenants.inserted))
	}
	tenant := f.tenants.inserted[0]
	if tenant.Slug != "acme" || tenant.Name != "Acme" || tenant.Timezone != "Europe/Berlin" {
		t.Errorf("tenant = %#v, want slug acme, name Acme and timezone Europe/Berlin", tenant)
	}
	if tenant.ID == uuid.Nil {
		t.Error("tenant was inserted with a nil id")
	}
}

func TestBootstrapNormalisesTheOwnerLogin(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	in := bootstrapInput()
	in.Name = "  Acme  "
	in.OwnerEmail = " Owner@Example.COM "

	created, err := f.auth.Bootstrap(t.Context(), in)
	if err != nil || !created {
		t.Fatalf("Bootstrap = %v, %v; want true, nil", created, err)
	}
	if len(f.tenants.inserted) != 1 || len(f.users.inserted) != 1 {
		t.Fatalf("inserted %d tenants and %d logins, want 1 and 1", len(f.tenants.inserted), len(f.users.inserted))
	}
	tenant := f.tenants.inserted[0]
	if tenant.Name != "Acme" {
		t.Errorf("tenant name = %q, want it trimmed", tenant.Name)
	}
	owner := f.users.inserted[0]
	if owner.TenantID != tenant.ID {
		t.Errorf("owner tenant = %v, want %v", owner.TenantID, tenant.ID)
	}
	if owner.Email != "owner@example.com" {
		t.Errorf("owner email = %q, want the trimmed, lower-cased address", owner.Email)
	}
	if owner.Role != domain.RoleOwner {
		t.Errorf("owner role = %q, want %q", owner.Role, domain.RoleOwner)
	}
	if !VerifyPassword(owner.PasswordHash, in.OwnerPassword) {
		t.Error("the stored owner hash does not verify the bootstrap password")
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	in := bootstrapInput()

	first, err := f.auth.Bootstrap(t.Context(), in)
	if err != nil || !first {
		t.Fatalf("first Bootstrap = %v, %v; want true, nil", first, err)
	}
	second, err := f.auth.Bootstrap(t.Context(), in)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	if second {
		t.Error("second Bootstrap reported created=true")
	}
	if len(f.users.inserted) != 2 {
		t.Fatalf("inserted %d logins, want 2 attempts", len(f.users.inserted))
	}
	if f.users.inserted[1].TenantID != f.users.inserted[0].TenantID {
		t.Errorf("the repeat used tenant %v, want the existing %v",
			f.users.inserted[1].TenantID, f.users.inserted[0].TenantID)
	}
	if len(f.tenants.bySlug) != 1 {
		t.Errorf("the tenant map holds %d tenants, want 1", len(f.tenants.bySlug))
	}
}

func TestBootstrapReusesAnExistingSlug(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	existing := domain.Tenant{ID: uuid.New(), Slug: "acme", Name: "Old Acme", Timezone: "UTC"}
	f.tenants.bySlug = map[string]domain.Tenant{"acme": existing}

	created, err := f.auth.Bootstrap(t.Context(), bootstrapInput())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !created {
		t.Error("Bootstrap reported created=false although the owner was new")
	}
	if len(f.users.inserted) != 1 {
		t.Fatalf("inserted %d logins, want 1", len(f.users.inserted))
	}
	if f.users.inserted[0].TenantID != existing.ID {
		t.Errorf("owner tenant = %v, want the existing %v", f.users.inserted[0].TenantID, existing.ID)
	}
}

func TestBootstrapRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*BootstrapInput)
		field  string
	}{
		{name: "slug is not a slug", mutate: func(in *BootstrapInput) { in.Slug = "Acme" }, field: "slug"},
		{name: "slug is empty", mutate: func(in *BootstrapInput) { in.Slug = "" }, field: "slug"},
		{name: "slug is reserved", mutate: func(in *BootstrapInput) { in.Slug = "services" }, field: "slug"},
		{name: "slug is reserved staff", mutate: func(in *BootstrapInput) { in.Slug = "staff" }, field: "slug"},
		{name: "name is blank", mutate: func(in *BootstrapInput) { in.Name = "   " }, field: "name"},
		{name: "name is too long", mutate: func(in *BootstrapInput) { in.Name = strings.Repeat("a", 201) }, field: "name"},
		{name: "timezone is unknown", mutate: func(in *BootstrapInput) { in.Timezone = "Mars/Olympus" }, field: "timezone"},
		{name: "timezone is empty", mutate: func(in *BootstrapInput) { in.Timezone = "" }, field: "timezone"},
		{name: "email is malformed", mutate: func(in *BootstrapInput) { in.OwnerEmail = "not-an-email" }, field: "owner_email"},
		{name: "email is empty", mutate: func(in *BootstrapInput) { in.OwnerEmail = "" }, field: "owner_email"},
		{name: "password is too short", mutate: func(in *BootstrapInput) { in.OwnerPassword = "short" }, field: "owner_password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAuthFixture(t)
			in := bootstrapInput()
			tt.mutate(&in)

			created, err := f.auth.Bootstrap(t.Context(), in)
			if created {
				t.Error("Bootstrap reported created=true for invalid input")
			}
			assertValidationError(t, err, tt.field)
			if len(f.tenants.inserted) != 0 || len(f.users.inserted) != 0 {
				t.Errorf("invalid input reached the stores: %d tenants, %d logins",
					len(f.tenants.inserted), len(f.users.inserted))
			}
		})
	}
}
