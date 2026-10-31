package panellicenseissuer

import (
	p "github.com/hongle/hl-panel/internal/panellicense"
	"net/http"
	"strconv"
	"strings"
)

func (s *Service) serveStock(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		errorResponse(w, problem("unauthorized", "management authentication required", 401))
		return
	}
	ctx := r.Context()
	if r.URL.Path == p.StockPath {
		switch r.Method {
		case http.MethodGet:
			offset := 0
			if value := r.URL.Query().Get("offset"); value != "" {
				var err error
				offset, err = strconv.Atoi(value)
				if err != nil || offset < 0 || offset > 1000000 {
					errorResponse(w, invalid("invalid offset"))
					return
				}
			}
			items, err := s.store.StockBatches(ctx, s.now(), offset)
			if err != nil {
				errorResponse(w, err)
				return
			}
			result := map[string]any{"items": items}
			if len(items) == 100 {
				result["next_offset"] = offset + 100
			}
			jsonResponse(w, 200, result)
			return
		case http.MethodPost:
			var input p.StockBatchRequest
			if err := decodeBody(w, r, &input); err != nil {
				errorResponse(w, err)
				return
			}
			result, err := s.CreateStockBatch(ctx, r.Header.Get("Idempotency-Key"), input)
			if err != nil {
				errorResponse(w, err)
				return
			}
			jsonResponse(w, 200, result)
			return
		}
	} else {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, p.StockPath+"/"), "/")
		if len(parts) == 2 && strings.HasPrefix(parts[0], "batch_") && len(parts[0]) == 38 {
			if parts[1] == "redemptions" && r.Method == http.MethodGet {
				items, err := s.store.Redemptions(ctx, parts[0])
				if err != nil {
					errorResponse(w, err)
					return
				}
				jsonResponse(w, 200, map[string]any{"items": items})
				return
			}
			if r.Method == http.MethodPost && (parts[1] == "export" || parts[1] == "confirm-export" || parts[1] == "disable") {
				var input p.StockActionRequest
				if err := decodeBody(w, r, &input); err != nil {
					errorResponse(w, err)
					return
				}
				if parts[1] == "export" {
					codes, err := s.ExportStockBatch(ctx, parts[0], input)
					if err != nil {
						errorResponse(w, err)
						return
					}
					jsonResponse(w, 200, map[string]any{"codes": codes})
					return
				}
				result, err := s.StockAction(ctx, parts[0], parts[1], r.Header.Get("Idempotency-Key"), input)
				if err != nil {
					errorResponse(w, err)
					return
				}
				jsonResponse(w, 200, result)
				return
			}
		}
	}
	errorResponse(w, problem("not_found", "route not found", 404))
}
