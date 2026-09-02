package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// sessionCookieName is the session credential's cookie.
//
// The __Host- prefix is a browser-enforced guarantee: a cookie so named is only
// accepted when it is Secure, has no Domain attribute, and has Path=/. That
// stops a sibling subdomain — including one an attacker controls after a
// takeover — from writing a cookie that our origin would then read. Because it
// requires Secure, it cannot be used over plaintext http, so development falls
// back to an unprefixed name.
const (
	sessionCookieName         = "__Host-forge_session"
	sessionCookieNameInsecure = "forge_session"
)

// cookieName picks the prefixed name only when it can be honoured.
func cookieName(secure bool) string {
	if secure {
		return sessionCookieName
	}
	return sessionCookieNameInsecure
}

// AuthHandlers serves the identity endpoints.
type AuthHandlers struct {
	svc *identity.Service
	cfg config.AuthConfig
	log *logx.Logger
}

// NewAuthHandlers wires the identity endpoints.
func NewAuthHandlers(svc *identity.Service, cfg config.AuthConfig, log *logx.Logger) *AuthHandlers {
	return &AuthHandlers{svc: svc, cfg: cfg, log: log}
}

func (h *AuthHandlers) requestContext(r *http.Request) identity.RequestContext {
	return identity.RequestContext{
		IP:        ClientIP(r),
		UserAgent: r.UserAgent(),
	}
}

// setSessionCookie writes the session credential.
func (h *AuthHandlers) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:  cookieName(h.cfg.CookieSecure),
		Value: token,
		Path:  "/",
		// Domain is deliberately unset: __Host- forbids it, and scoping a
		// session cookie to a parent domain shares it with every subdomain.
		Expires: expires,
		// HttpOnly keeps the credential out of reach of any script on the page,
		// so an XSS becomes a session-riding bug rather than a session-theft one.
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		// Lax rather than Strict: Strict would drop the cookie on the
		// cross-site navigation that an email verification link *is*, so a user
		// clicking through from their inbox would arrive signed out.
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandlers) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(h.cfg.CookieSecure),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionToken extracts the credential from a request.
//
// The cookie is authoritative; a bearer token is accepted so that CLI and
// service callers do not need a cookie jar.
func (h *AuthHandlers) SessionToken(r *http.Request) string {
	if c, err := r.Cookie(cookieName(h.cfg.CookieSecure)); err == nil && c.Value != "" {
		return c.Value
	}
	const prefix = "Bearer "
	if v := r.Header.Get("Authorization"); len(v) > len(prefix) && hasPrefixFold(v, prefix) {
		return v[len(prefix):]
	}
	return ""
}

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// UserDTO is the public shape of an account.
//
// A separate type from identity.User, deliberately: the domain struct carries
// PasswordHash, and a serialiser that reflects over the domain type is one
// forgotten json:"-" away from publishing it. Building the DTO explicitly means
// a new field is invisible until somebody adds it here on purpose.
type UserDTO struct {
	ID            string  `json:"id"`
	Email         string  `json:"email"`
	DisplayName   string  `json:"display_name"`
	EmailVerified bool    `json:"email_verified"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
	VerifiedAt    *string `json:"email_verified_at,omitempty"`
}

func toUserDTO(u *identity.User) UserDTO {
	dto := UserDTO{
		ID:            u.ID,
		Email:         u.Email,
		DisplayName:   u.DisplayName,
		EmailVerified: u.EmailVerified(),
		Status:        string(u.Status),
		CreatedAt:     u.CreatedAt.UTC().Format(time.RFC3339),
	}
	if u.EmailVerifiedAt != nil {
		s := u.EmailVerifiedAt.UTC().Format(time.RFC3339)
		dto.VerifiedAt = &s
	}
	return dto
}

// SessionDTO is the public shape of a session, for the security surface where a
// user reviews their own devices.
type SessionDTO struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	UserAgent  string `json:"user_agent"`
	IP         string `json:"ip,omitempty"`
	Current    bool   `json:"current"`
}

func toSessionDTO(s *identity.Session, currentID string) SessionDTO {
	dto := SessionDTO{
		ID:         s.ID,
		CreatedAt:  s.CreatedAt.UTC().Format(time.RFC3339),
		LastSeenAt: s.LastSeenAt.UTC().Format(time.RFC3339),
		ExpiresAt:  s.ExpiresAt.UTC().Format(time.RFC3339),
		UserAgent:  s.UserAgent,
		Current:    s.ID == currentID,
	}
	if s.IP != nil {
		dto.IP = s.IP.String()
	}
	return dto
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

type signUpRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type signUpResponse struct {
	User UserDTO `json:"user"`
	// VerificationSent is reported truthfully. When mail delivery failed the
	// account still exists, and telling the user to check an inbox that will
	// never receive anything is exactly the kind of small lie that costs an
	// afternoon.
	VerificationSent bool   `json:"verification_sent"`
	Message          string `json:"message"`
}

// SignUp handles POST /v1/auth/sign-up.
func (h *AuthHandlers) SignUp(w http.ResponseWriter, r *http.Request) {
	var req signUpRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.log, err)
		return
	}

	res, err := h.svc.SignUp(r.Context(), identity.SignUpInput{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
	}, h.requestContext(r))
	if err != nil {
		WriteError(w, r, h.log, err)
		return
	}

	msg := "Account created. Check your email for a link to confirm your address."
	if !res.VerificationSent {
		msg = "Account created, but the confirmation email could not be sent. " +
			"You can sign in now and request a new confirmation link."
	}
	WriteJSON(w, http.StatusCreated, signUpResponse{
		User:             toUserDTO(res.User),
		VerificationSent: res.VerificationSent,
		Message:          msg,
	})
}

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type signInResponse struct {
	User UserDTO `json:"user"`
	// Token is returned in the body as well as the cookie so that non-browser
	// clients (the CLI, integration tests) do not need a cookie jar. Browsers
	// should keep using the HttpOnly cookie and ignore this field.
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// SignIn handles POST /v1/auth/sign-in.
func (h *AuthHandlers) SignIn(w http.ResponseWriter, r *http.Request) {
	var req signInRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.log, err)
		return
	}

	res, err := h.svc.SignIn(r.Context(), req.Email, req.Password, h.requestContext(r))
	if err != nil {
		WriteError(w, r, h.log, err)
		return
	}

	h.setSessionCookie(w, res.Token, res.Session.ExpiresAt)
	WriteJSON(w, http.StatusOK, signInResponse{
		User:      toUserDTO(res.User),
		Token:     res.Token,
		ExpiresAt: res.Session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// SignOut handles POST /v1/auth/sign-out.
func (h *AuthHandlers) SignOut(w http.ResponseWriter, r *http.Request) {
	session, ok := SessionFrom(r.Context())
	if !ok {
		// Already signed out. Clearing the cookie and returning success is the
		// honest outcome: the caller's goal — "end my session" — is satisfied.
		h.clearSessionCookie(w)
		WriteJSON(w, http.StatusOK, map[string]string{"message": "Signed out."})
		return
	}
	if err := h.svc.SignOut(r.Context(), session.ID); err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	h.clearSessionCookie(w)
	WriteJSON(w, http.StatusOK, map[string]string{"message": "Signed out."})
}

// SignOutAll handles POST /v1/auth/sign-out-all.
func (h *AuthHandlers) SignOutAll(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	session, _ := SessionFrom(r.Context())

	n, err := h.svc.SignOutAllDevices(r.Context(), user.ID, session.ID)
	if err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"revoked": n,
		"message": "Other devices have been signed out. This one is still signed in.",
	})
}

type verifyEmailRequest struct {
	Token string `json:"token"`
}

// VerifyEmail handles POST /v1/auth/verify-email.
func (h *AuthHandlers) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req verifyEmailRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	user, err := h.svc.VerifyEmail(r.Context(), req.Token)
	if err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"user":    toUserDTO(user),
		"message": "Email address confirmed.",
	})
}

type emailOnlyRequest struct {
	Email string `json:"email"`
}

// ResendVerification handles POST /v1/auth/resend-verification.
//
// Always 202, whatever happened. Distinguishing "sent" from "no such account"
// or "already verified" would make this an account-enumeration oracle.
func (h *AuthHandlers) ResendVerification(w http.ResponseWriter, r *http.Request) {
	var req emailOnlyRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	if err := h.svc.ResendVerification(r.Context(), req.Email, h.requestContext(r)); err != nil {
		// Only genuine infrastructure failures reach here; the service already
		// swallows the outcomes that would be disclosive.
		WriteError(w, r, h.log, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]string{
		"message": "If that address has an unconfirmed account, a new confirmation link is on its way.",
	})
}

// RequestPasswordReset handles POST /v1/auth/forgot-password.
//
// Always 202. See identity.Service.RequestPasswordReset for why this endpoint
// must not distinguish a known address from an unknown one: it is
// unauthenticated, and "which of these leaked addresses has an account?" is
// exactly the question asked before a credential-stuffing run.
func (h *AuthHandlers) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req emailOnlyRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	if err := h.svc.RequestPasswordReset(r.Context(), req.Email, h.requestContext(r)); err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]string{
		"message": "If that address has an account, a password reset link is on its way.",
	})
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ResetPassword handles POST /v1/auth/reset-password.
func (h *AuthHandlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	user, err := h.svc.ResetPassword(r.Context(), req.Token, req.NewPassword)
	if err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	// Every session was revoked, including any this browser held.
	h.clearSessionCookie(w)
	WriteJSON(w, http.StatusOK, map[string]any{
		"user":    toUserDTO(user),
		"message": "Password updated. All devices have been signed out — sign in again with your new password.",
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword handles POST /v1/auth/change-password.
func (h *AuthHandlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	user, _ := UserFrom(r.Context())

	rotated, err := h.svc.ChangePassword(r.Context(), user.ID, req.CurrentPassword, req.NewPassword, h.requestContext(r))
	if err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	// The change rotated the session; install the replacement credential or the
	// caller is signed out by their own successful request.
	h.setSessionCookie(w, rotated.Token, rotated.Session.ExpiresAt)
	WriteJSON(w, http.StatusOK, map[string]any{
		"token":      rotated.Token,
		"expires_at": rotated.Session.ExpiresAt.UTC().Format(time.RFC3339),
		"message":    "Password updated. Other devices have been signed out.",
	})
}

// Me handles GET /v1/auth/me.
func (h *AuthHandlers) Me(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	session, _ := SessionFrom(r.Context())
	WriteJSON(w, http.StatusOK, map[string]any{
		"user":    toUserDTO(user),
		"session": toSessionDTO(session, session.ID),
	})
}

// ListSessions handles GET /v1/auth/sessions.
func (h *AuthHandlers) ListSessions(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	current, _ := SessionFrom(r.Context())

	sessions, err := h.svc.ListSessions(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, h.log, err)
		return
	}
	out := make([]SessionDTO, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, toSessionDTO(s, current.ID))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// ---------------------------------------------------------------------------
// authentication middleware
// ---------------------------------------------------------------------------

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeySession
)

// UserFrom returns the authenticated account from a request context.
func UserFrom(ctx context.Context) (*identity.User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(*identity.User)
	return u, ok
}

// SessionFrom returns the authenticated session from a request context.
func SessionFrom(ctx context.Context) (*identity.Session, bool) {
	s, ok := ctx.Value(ctxKeySession).(*identity.Session)
	return s, ok
}

// RequireAuth rejects unauthenticated requests.
func (h *AuthHandlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := h.SessionToken(r)
		if token == "" {
			WriteError(w, r, h.log, errs.New("httpapi.RequireAuth", errs.CodeNotAuthenticated).
				WithDetail("no session cookie or bearer token was presented"))
			return
		}
		user, session, err := h.svc.Authenticate(r.Context(), token)
		if err != nil {
			// A dead credential must not linger in the browser: leaving it means
			// every later request fails identically with no path out.
			h.clearSessionCookie(w)
			WriteError(w, r, h.log, err)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUser, user)
		ctx = context.WithValue(ctx, ctxKeySession, session)
		ctx = logx.WithActor(ctx, user.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuth attaches the principal when one is present, without requiring it.
func (h *AuthHandlers) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := h.SessionToken(r); token != "" {
			if user, session, err := h.svc.Authenticate(r.Context(), token); err == nil {
				ctx := context.WithValue(r.Context(), ctxKeyUser, user)
				ctx = context.WithValue(ctx, ctxKeySession, session)
				ctx = logx.WithActor(ctx, user.ID)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireVerifiedEmail gates the consequential surface.
//
// Sign-in deliberately does not require a verified address (see
// identity.User.CanSignIn); this is where the requirement actually bites, so
// that an unverified user can still sign in, see their state, and ask for a new
// link rather than being locked out with no route forward.
func (h *AuthHandlers) RequireVerifiedEmail(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFrom(r.Context())
		if !ok {
			WriteError(w, r, h.log, errs.New("httpapi.RequireVerifiedEmail", errs.CodeNotAuthenticated))
			return
		}
		if !user.EmailVerified() {
			WriteError(w, r, h.log, errs.New("httpapi.RequireVerifiedEmail", errs.CodeEmailNotVerified).
				WithDetail("confirm %s before starting work that FORGE will act on", user.Email))
			return
		}
		next.ServeHTTP(w, r)
	})
}
