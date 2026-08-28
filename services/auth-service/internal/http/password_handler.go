package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const errCodeInvalidToken = "invalid_token"

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

type emailHandoffAvailability interface {
	EmailHandoffAvailable() bool
}

func AuthForgotPassword(recovery service.PasswordRecoveryManager, limiters ...*targetAwareRateLimiter) http.Handler {
	targetLimiter := firstTargetLimiter(limiters)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recovery == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "password recovery endpoint disabled")
			return
		}
		if !emailHandoffAvailable(recovery) {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "password recovery endpoint disabled")
			return
		}

		var req forgotPasswordRequest
		if !decodePasswordRequest(w, r, &req) {
			return
		}
		if !targetLimiter.allowEmail(req.Email) {
			writeRateLimited(w)
			return
		}

		if err := recovery.ForgotPassword(r.Context(), domain.ForgotPasswordInput{Email: req.Email}); err != nil {
			if errors.Is(err, domain.ErrEmailOutboxUnavailable) {
				httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "password recovery endpoint disabled")
				return
			}
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

func AuthResetPassword(recovery service.PasswordRecoveryManager, limiters ...*targetAwareRateLimiter) http.Handler {
	targetLimiter := firstTargetLimiter(limiters)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recovery == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "password recovery endpoint disabled")
			return
		}

		var req resetPasswordRequest
		if !decodePasswordRequest(w, r, &req) {
			return
		}
		if !targetLimiter.allowToken(req.Token) {
			writeRateLimited(w)
			return
		}

		err := recovery.ResetPassword(r.Context(), domain.ResetPasswordInput{Token: req.Token, NewPassword: req.NewPassword})
		if err != nil {
			writePasswordResetError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func decodePasswordRequest(w http.ResponseWriter, r *http.Request, req any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(req); err != nil {
		writeDecodeError(w, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeDecodeError(w, err)
		return false
	}
	return true
}

func writePasswordResetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidToken):
		httputil.WriteError(w, http.StatusUnauthorized, errCodeInvalidToken, "invalid or expired token")
	case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrPasswordPolicy):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func firstTargetLimiter(limiters []*targetAwareRateLimiter) *targetAwareRateLimiter {
	if len(limiters) == 0 {
		return nil
	}
	return limiters[0]
}

func emailHandoffAvailable(manager any) bool {
	if manager == nil {
		return false
	}
	availability, ok := manager.(emailHandoffAvailability)
	return !ok || availability.EmailHandoffAvailable()
}
