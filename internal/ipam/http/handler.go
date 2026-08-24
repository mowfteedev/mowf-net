package http

import (
	"encoding/json"
	"errors"
	"net/http"

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
}

// CreateSubnet handles POST /api/v1/subnets.
func (h *SubnetHandler) CreateSubnet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req service.CreateSubnetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CIDR", "Invalid request payload")
		return
	}

	dto, err := h.service.CreateSubnet(r.Context(), req)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCIDR) {
			writeError(w, http.StatusBadRequest, "INVALID_CIDR", "The provided CIDR is invalid or non-canonical.")
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
