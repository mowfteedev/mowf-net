package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

// AllocationHandler handles HTTP requests for persisted IP allocations.
type AllocationHandler struct {
	service *service.AllocationService
}

const maxReserveAllocationRequestBodyBytes int64 = 16 * 1024

func NewAllocationHandler(service *service.AllocationService) *AllocationHandler {
	return &AllocationHandler{service: service}
}

// RegisterRoutes registers allocation endpoints on the existing application mux.
func (h *AllocationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/ip-allocations", h.ListAllocations)
	mux.HandleFunc("POST /api/v1/ip-allocations", h.ReserveAllocation)
	mux.HandleFunc("DELETE /api/v1/ip-allocations/{allocation_id}", h.UnreserveAllocation)
}

// ReserveAllocation handles POST /api/v1/ip-allocations.
func (h *AllocationHandler) ReserveAllocation(w http.ResponseWriter, r *http.Request) {
	req, err := decodeReserveAllocationRequest(http.MaxBytesReader(w, r.Body, maxReserveAllocationRequestBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request payload")
		return
	}

	allocation, err := h.service.ReserveAllocation(r.Context(), req)
	if err != nil {
		writeReserveAllocationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse{Data: allocation})
}

// UnreserveAllocation handles DELETE /api/v1/ip-allocations/{allocation_id}.
func (h *AllocationHandler) UnreserveAllocation(w http.ResponseWriter, r *http.Request) {
	allocationID, ok := parsePositivePathID(r.PathValue("allocation_id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid allocation ID")
		return
	}

	if err := h.service.UnreserveAllocation(r.Context(), allocationID); err != nil {
		writeUnreserveAllocationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAllocations handles GET /api/v1/ip-allocations.
func (h *AllocationHandler) ListAllocations(w http.ResponseWriter, r *http.Request) {
	req, err := parseAllocationListRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request parameters")
		return
	}
	resp, err := h.service.ListAllocations(r.Context(), req)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidRequest) {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request parameters")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseAllocationListRequest(r *http.Request) (service.ListAllocationsRequest, error) {
	q := r.URL.Query()
	req := service.ListAllocationsRequest{Limit: 50}

	if q.Has("limit") {
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return service.ListAllocationsRequest{}, domain.ErrInvalidRequest
		}
		req.Limit = limit
	}
	if q.Has("cursor") {
		cursor := q.Get("cursor")
		if cursor == "" || cursor != strings.TrimSpace(cursor) {
			return service.ListAllocationsRequest{}, domain.ErrInvalidRequest
		}
		id, err := service.DecodeCursor(cursor)
		if err != nil {
			return service.ListAllocationsRequest{}, domain.ErrInvalidRequest
		}
		req.Cursor = &id
	}

	if q.Has("subnet_id") {
		id, err := parsePositiveQueryID(q.Get("subnet_id"))
		if err != nil {
			return service.ListAllocationsRequest{}, err
		}
		req.SubnetID = &id
	}
	if q.Has("interface_id") {
		id, err := parsePositiveQueryID(q.Get("interface_id"))
		if err != nil {
			return service.ListAllocationsRequest{}, err
		}
		req.InterfaceID = &id
	}
	if q.Has("status") {
		status := domain.AllocationStatus(q.Get("status"))
		if status != domain.AllocationStatusReserved && status != domain.AllocationStatusAssigned {
			return service.ListAllocationsRequest{}, domain.ErrInvalidRequest
		}
		req.Status = &status
	}
	if q.Has("address") {
		address, err := netip.ParseAddr(q.Get("address"))
		if err != nil || !address.Is4() {
			return service.ListAllocationsRequest{}, domain.ErrInvalidRequest
		}
		req.Address = &address
	}
	return req, nil
}

func parsePositiveQueryID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return 0, domain.ErrInvalidRequest
	}
	return id, nil
}

func decodeReserveAllocationRequest(body io.Reader) (service.ReserveAllocationRequest, error) {
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&fields); err != nil {
		return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
	}

	var req service.ReserveAllocationRequest
	subnetRaw, ok := fields["subnet_id"]
	if !ok || bytes.Equal(bytes.TrimSpace(subnetRaw), []byte("null")) || json.Unmarshal(subnetRaw, &req.SubnetID) != nil || req.SubnetID <= 0 {
		return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
	}
	addressRaw, ok := fields["address"]
	if !ok || bytes.Equal(bytes.TrimSpace(addressRaw), []byte("null")) {
		return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
	}
	var address string
	if err := json.Unmarshal(addressRaw, &address); err != nil {
		return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
	}
	parsedAddress, err := netip.ParseAddr(address)
	if err != nil || !parsedAddress.Is4() {
		return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
	}
	req.Address = parsedAddress

	if descriptionRaw, ok := fields["description"]; ok {
		if bytes.Equal(bytes.TrimSpace(descriptionRaw), []byte("null")) || json.Unmarshal(descriptionRaw, &req.Description) != nil {
			return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
		}
	}
	for name := range fields {
		if name != "subnet_id" && name != "address" && name != "description" {
			return service.ReserveAllocationRequest{}, domain.ErrInvalidRequest
		}
	}
	return req, nil
}

func writeReserveAllocationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request payload")
	case errors.Is(err, domain.ErrSubnetNotFound):
		writeError(w, http.StatusNotFound, "SUBNET_NOT_FOUND", "The requested subnet was not found.")
	case errors.Is(err, domain.ErrIPOutsideSubnet):
		writeError(w, http.StatusBadRequest, "IP_OUTSIDE_SUBNET", "The IP address is outside the target subnet.")
	case errors.Is(err, domain.ErrIPNotAssignable):
		writeError(w, http.StatusConflict, "IP_NOT_ASSIGNABLE", "The IP address is not an assignable host.")
	case errors.Is(err, domain.ErrIPAlreadyAllocated):
		writeError(w, http.StatusConflict, "IP_ALREADY_ALLOCATED", "The IP address is already allocated.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
	}
}

func writeUnreserveAllocationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid allocation ID")
	case errors.Is(err, domain.ErrIPAllocationNotFound):
		writeError(w, http.StatusNotFound, "IP_ALLOCATION_NOT_FOUND", "The requested IP allocation was not found.")
	case errors.Is(err, domain.ErrIPNotAssignable):
		writeError(w, http.StatusConflict, "IP_NOT_ASSIGNABLE", "The IP allocation cannot be unreserved.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
	}
}
