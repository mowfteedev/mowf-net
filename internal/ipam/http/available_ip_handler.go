package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

// AvailableIPHandler handles computed available-IP reads.
type AvailableIPHandler struct {
	service *service.AvailableIPService
}

func NewAvailableIPHandler(service *service.AvailableIPService) *AvailableIPHandler {
	return &AvailableIPHandler{service: service}
}

func (h *AvailableIPHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/subnets/{subnet_id}/available-ips", h.ListAvailableIPs)
}

// ListAvailableIPs handles GET /api/v1/subnets/{subnet_id}/available-ips.
func (h *AvailableIPHandler) ListAvailableIPs(w http.ResponseWriter, r *http.Request) {
	subnetID, ok := parsePositivePathID(r.PathValue("subnet_id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid subnet ID")
		return
	}

	req, err := parseAvailableIPRequest(r, subnetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request parameters")
		return
	}
	response, err := h.service.ListAvailableIPs(r.Context(), req)
	if err != nil {
		writeAvailableIPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func parseAvailableIPRequest(r *http.Request, subnetID int64) (service.ListAvailableIPsRequest, error) {
	q := r.URL.Query()
	req := service.ListAvailableIPsRequest{SubnetID: subnetID, Limit: 50}
	for _, name := range []string{"limit", "cursor", "range_start", "range_end", "ip"} {
		if len(q[name]) > 1 {
			return service.ListAvailableIPsRequest{}, domain.ErrInvalidRequest
		}
	}
	if q.Has("limit") {
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return service.ListAvailableIPsRequest{}, domain.ErrInvalidRequest
		}
		req.Limit = limit
	}
	req.Cursor, req.CursorSet = q.Get("cursor"), q.Has("cursor")
	req.RangeStart, req.RangeStartSet = q.Get("range_start"), q.Has("range_start")
	req.RangeEnd, req.RangeEndSet = q.Get("range_end"), q.Has("range_end")
	req.IP, req.IPSet = q.Get("ip"), q.Has("ip")
	return req, nil
}

func writeAvailableIPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request parameters")
	case errors.Is(err, domain.ErrIPOutsideSubnet):
		writeError(w, http.StatusBadRequest, "IP_OUTSIDE_SUBNET", "The IP address is outside the target subnet.")
	case errors.Is(err, domain.ErrSubnetNotFound):
		writeError(w, http.StatusNotFound, "SUBNET_NOT_FOUND", "The requested subnet was not found.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
	}
}
