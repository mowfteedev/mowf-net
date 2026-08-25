package http

import (
	"errors"
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

func NewAllocationHandler(service *service.AllocationService) *AllocationHandler {
	return &AllocationHandler{service: service}
}

// RegisterRoutes registers allocation endpoints on the existing application mux.
func (h *AllocationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/ip-allocations", h.ListAllocations)
}

// ListAllocations handles GET /api/v1/ip-allocations.
func (h *AllocationHandler) ListAllocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

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
