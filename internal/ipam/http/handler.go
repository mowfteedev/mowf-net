package http

import (
	"bytes"
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
	mux.HandleFunc("PATCH /api/v1/subnets/{subnet_id}", h.PatchSubnet)
	mux.HandleFunc("DELETE /api/v1/subnets/{subnet_id}", h.DeleteSubnet)
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

// PatchSubnet handles presence-aware PATCH /api/v1/subnets/{subnet_id}.
func (h *SubnetHandler) PatchSubnet(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePositivePathID(r.PathValue("subnet_id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid subnet ID")
		return
	}

	req, err := decodeUpdateSubnetRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request payload")
		return
	}

	dto, err := h.service.UpdateSubnet(r.Context(), id, req)
	if err != nil {
		writeSubnetMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse{Data: dto})
}

// DeleteSubnet handles DELETE /api/v1/subnets/{subnet_id}.
func (h *SubnetHandler) DeleteSubnet(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePositivePathID(r.PathValue("subnet_id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid subnet ID")
		return
	}
	if err := h.service.DeleteSubnet(r.Context(), id); err != nil {
		writeSubnetMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeUpdateSubnetRequest(body io.Reader) (service.UpdateSubnetRequest, error) {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(body)
	if err := dec.Decode(&fields); err != nil {
		return service.UpdateSubnetRequest{}, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
	}
	if len(fields) == 0 {
		return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
	}

	var req service.UpdateSubnetRequest
	for name, raw := range fields {
		switch name {
		case "cidr":
			req.CIDRSet = true
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
			}
			req.CIDR = &value
		case "description":
			req.DescriptionSet = true
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
			}
			req.Description = &value
		case "vlan_ref_id":
			req.VlanRefIDSet = true
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				req.VlanRefID = nil
				continue
			}
			var value int64
			if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
				return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
			}
			req.VlanRefID = &value
		default:
			return service.UpdateSubnetRequest{}, domain.ErrInvalidRequest
		}
	}
	return req, nil
}

func parsePositivePathID(value string) (int64, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func writeSubnetMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request payload")
	case errors.Is(err, domain.ErrInvalidCIDR):
		writeError(w, http.StatusBadRequest, "INVALID_CIDR", "The provided CIDR is invalid or non-canonical.")
	case errors.Is(err, domain.ErrSubnetNotFound):
		writeError(w, http.StatusNotFound, "SUBNET_NOT_FOUND", "The requested subnet was not found.")
	case errors.Is(err, domain.ErrVlanNotFound):
		writeError(w, http.StatusNotFound, "VLAN_NOT_FOUND", "The referenced VLAN was not found.")
	case errors.Is(err, domain.ErrSubnetOverlap):
		writeError(w, http.StatusConflict, "SUBNET_OVERLAP", "The subnet overlaps with an existing subnet.")
	case errors.Is(err, domain.ErrSubnetResizeConflict):
		writeError(w, http.StatusConflict, "SUBNET_RESIZE_CONFLICT", "The subnet cannot be resized while allocations would leave its usable range.")
	case errors.Is(err, domain.ErrSubnetHasAllocations):
		writeError(w, http.StatusConflict, "SUBNET_HAS_ALLOCATIONS", "The subnet cannot be deleted while allocations exist.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
	}
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
