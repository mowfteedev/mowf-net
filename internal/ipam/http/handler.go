package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

// ErrorDetail defines the standard API error structure.
type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// ErrorResponse defines the JSON envelope for error responses.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// DataResponse defines the standard JSON envelope for successful single-resource responses.
type DataResponse struct {
	Data any `json:"data"`
}

// SubnetHandler handles HTTP requests for Subnet resources.
type SubnetHandler struct {
	service *service.SubnetService
}

// NewSubnetHandler creates a new SubnetHandler.
func NewSubnetHandler(service *service.SubnetService) *SubnetHandler {
	return &SubnetHandler{service: service}
}

// RegisterRoutes registers subnet endpoints on the given ServeMux.
func (h *SubnetHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/subnets", h.CreateSubnet)
	mux.HandleFunc("GET /api/v1/subnets", h.ListSubnets)
	mux.HandleFunc("GET /api/v1/subnets/{subnet_id}", h.GetSubnet)
}

// CreateSubnet handles POST /api/v1/subnets.
func (h *SubnetHandler) CreateSubnet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req service.CreateSubnetRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request payload")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request payload")
		return
	}

	dto, err := h.service.CreateSubnet(r.Context(), req)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCIDR) {
			writeError(w, http.StatusBadRequest, "INVALID_CIDR", "The provided CIDR is invalid or non-canonical.")
			return
		}
		if errors.Is(err, domain.ErrVlanNotFound) {
			writeError(w, http.StatusNotFound, "VLAN_NOT_FOUND", "The referenced VLAN was not found.")
			return
		}
		if errors.Is(err, domain.ErrSubnetOverlap) {
			writeError(w, http.StatusConflict, "SUBNET_OVERLAP", "The subnet overlaps with an existing subnet.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
		return
	}

	writeJSON(w, http.StatusCreated, DataResponse{Data: dto})
}

// GetSubnet handles GET /api/v1/subnets/{subnet_id}.
func (h *SubnetHandler) GetSubnet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := r.PathValue("subnet_id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid subnet ID")
		return
	}

	dto, err := h.service.GetSubnet(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrSubnetNotFound) {
			writeError(w, http.StatusNotFound, "SUBNET_NOT_FOUND", "The requested subnet was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
		return
	}

	writeJSON(w, http.StatusOK, DataResponse{Data: dto})
}

// ListSubnets handles GET /api/v1/subnets with filters and pagination.
func (h *SubnetHandler) ListSubnets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	limit := 50
	if limitStr := q.Get("limit"); limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit <= 0 {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid limit parameter")
			return
		}
		if parsedLimit > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "limit must not exceed 100")
			return
		}
		limit = parsedLimit
	}

	var cursorID *int64
	if cursorStr := q.Get("cursor"); cursorStr != "" {
		id, err := service.DecodeCursor(cursorStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid cursor parameter")
			return
		}
		cursorID = &id
	}

	var vlanRefID *int64
	if vlanRefStr := q.Get("vlan_ref_id"); vlanRefStr != "" {
		id, err := strconv.ParseInt(vlanRefStr, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid vlan_ref_id parameter")
			return
		}
		vlanRefID = &id
	}

	search := strings.TrimSpace(q.Get("search"))

	req := service.ListSubnetsRequest{
		VlanRefID: vlanRefID,
		Search:    search,
		Limit:     limit,
		Cursor:    cursorID,
	}

	resp, err := h.service.ListSubnets(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Details: map[string]any{},
		},
	})
}
