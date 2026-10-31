package panellicenseissuer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func errorResponse(w http.ResponseWriter, err error) {
	e := publicError(err)
	var body p.ErrorResponse
	body.Error.Code = e.Code
	body.Error.Message = e.Message
	jsonResponse(w, e.HTTPStatus, body)
}
func decodeBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return invalid("invalid JSON body")
	}
	if d.Decode(new(any)) != io.EOF {
		return invalid("trailing JSON data")
	}
	return nil
}
func (s *Service) authenticated(r *http.Request) bool {
	actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	expected := sha256.Sum256([]byte("Bearer " + s.config.AdminToken))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if r.URL.Path == p.StockPath || strings.HasPrefix(r.URL.Path, p.StockPath+"/") {
		s.serveStock(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, p.ManagementPath) {
		if !s.authenticated(r) {
			errorResponse(w, problem("unauthorized", "management authentication required", 401))
			return
		}
		if r.URL.Path == p.ManagementPath {
			switch r.Method {
			case http.MethodGet:
				items, err := s.List(ctx)
				if err != nil {
					errorResponse(w, err)
					return
				}
				jsonResponse(w, 200, map[string]any{"items": items})
				return
			case http.MethodPost:
				var body p.IssueRequest
				if err := decodeBody(w, r, &body); err != nil {
					errorResponse(w, err)
					return
				}
				result, err := s.Issue(ctx, r.Header.Get("Idempotency-Key"), body)
				if err != nil {
					errorResponse(w, err)
					return
				}
				jsonResponse(w, 200, result)
				return
			}
		} else {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, p.ManagementPath+"/"), "/")
			if len(parts) == 2 && strings.HasPrefix(parts[0], "lic_") {
				if parts[1] == "audit" && r.Method == http.MethodGet {
					items, err := s.Audit(ctx, parts[0])
					if err != nil {
						errorResponse(w, err)
						return
					}
					jsonResponse(w, 200, map[string]any{"items": items})
					return
				}
				if r.Method == http.MethodPost {
					var body p.MutationRequest
					if err := decodeBody(w, r, &body); err != nil {
						errorResponse(w, err)
						return
					}
					result, err := s.Mutate(ctx, parts[0], parts[1], r.Header.Get("Idempotency-Key"), body)
					if err != nil {
						errorResponse(w, err)
						return
					}
					jsonResponse(w, 200, result)
					return
				}
			}
		}
	} else if r.Method == http.MethodPost {
		operation := ""
		switch r.URL.Path {
		case p.PublicPath + "/activation/prepare":
			operation = "prepare"
		case p.PublicPath + "/activation/commit":
			operation = "commit"
		case p.PublicPath + "/sync":
			operation = "sync"
		case p.PublicPath + "/free/prepare":
			operation = "free-prepare"
		case p.PublicPath + "/free/commit":
			operation = "free-commit"
		case p.PublicPath + "/free/install/prepare":
			operation = "free-install-prepare"
		case p.PublicPath + "/free/install/commit":
			operation = "free-install-commit"
		case p.PublicPath + "/shop/catalog":
			operation = "shop-catalog"
		case p.PublicPath + "/redemption/prepare":
			operation = "redeem-prepare"
		case p.PublicPath + "/redemption/commit":
			operation = "redeem-commit"
		}
		if operation != "" {
			var signed p.InstanceRequest
			if err := decodeBody(w, r, &signed); err != nil {
				errorResponse(w, err)
				return
			}
			envelope, err := s.Public(ctx, operation, signed)
			if err != nil {
				errorResponse(w, err)
				return
			}
			jsonResponse(w, 200, envelope)
			return
		}
	}
	errorResponse(w, problem("not_found", "route not found", 404))
}
