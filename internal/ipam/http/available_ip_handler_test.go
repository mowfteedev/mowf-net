package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	ipamhttp "github.com/mowfteedev/mowf-net/internal/ipam/http"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

type mockOccupiedAddressRepo struct {
	addresses []netip.Addr
	err       error
	calls     int
}

func (m *mockOccupiedAddressRepo) ListOccupiedAddresses(_ context.Context, _ int64, start, end netip.Addr) ([]netip.Addr, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	result := make([]netip.Addr, 0)
	for _, address := range m.addresses {
		if address.Compare(start) >= 0 && address.Compare(end) <= 0 {
			result = append(result, address)
		}
	}
	return result, nil
}

func availableHandlerSubnet(t *testing.T, id int64, cidrString string) *repository.SubnetRead {
	t.Helper()
	cidr, err := domain.ParseCIDR(cidrString)
	if err != nil {
		t.Fatal(err)
	}
	return &repository.SubnetRead{Subnet: domain.Subnet{ID: id, CIDR: cidr}}
}

func setupAvailableIPTestMux(subnetRepo *mockSubnetRepo, occupied *mockOccupiedAddressRepo) *http.ServeMux {
	mux := http.NewServeMux()
	subnetService := service.NewSubnetService(subnetRepo)
	ipamhttp.NewSubnetHandler(subnetService).RegisterRoutes(mux)
	availableService := service.NewAvailableIPService(subnetRepo, occupied)
	ipamhttp.NewAvailableIPHandler(availableService).RegisterRoutes(mux)
	return mux
}

func TestAvailableIPHandler_RoutesAndResponseEnvelope(t *testing.T) {
	subnet := availableHandlerSubnet(t, 1, "192.168.40.0/24")
	subnetRepo := &mockSubnetRepo{getByIDFn: func(_ context.Context, id int64) (*repository.SubnetRead, error) {
		if id == 1 {
			return subnet, nil
		}
		return nil, domain.ErrSubnetNotFound
	}}
	occupied := &mockOccupiedAddressRepo{addresses: []netip.Addr{netip.MustParseAddr("192.168.40.1")}}
	mux := setupAvailableIPTestMux(subnetRepo, occupied)

	// The existing single-Subnet route must remain distinct and functional.
	subnetRecorder := httptest.NewRecorder()
	mux.ServeHTTP(subnetRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/subnets/1", nil))
	if subnetRecorder.Code != http.StatusOK {
		t.Fatalf("existing subnet route status = %d, body=%s", subnetRecorder.Code, subnetRecorder.Body.String())
	}

	availableRecorder := httptest.NewRecorder()
	mux.ServeHTTP(availableRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/subnets/1/available-ips?limit=1", nil))
	if availableRecorder.Code != http.StatusOK {
		t.Fatalf("available route status = %d, body=%s", availableRecorder.Code, availableRecorder.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
		Page service.PageInfo `json:"page"`
	}
	if err := json.NewDecoder(availableRecorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0]["address"] != "192.168.40.2" || body.Data[0]["state"] != "available" || body.Data[0]["persisted"] != false {
		t.Fatalf("available data = %#v", body.Data)
	}
	if len(body.Data[0]) != 3 {
		t.Fatalf("available item exposes unexpected fields: %#v", body.Data[0])
	}
	if _, hasID := body.Data[0]["id"]; hasID {
		t.Fatal("computed available item exposed an allocation id")
	}
	if body.Page.Limit != 1 || body.Page.NextCursor == nil {
		t.Fatalf("page = %#v", body.Page)
	}
}

func TestAvailableIPHandler_DefaultAndMaximumLimit(t *testing.T) {
	subnet := availableHandlerSubnet(t, 1, "10.30.0.0/24")
	repo := &mockSubnetRepo{getByIDFn: func(_ context.Context, _ int64) (*repository.SubnetRead, error) { return subnet, nil }}
	mux := setupAvailableIPTestMux(repo, &mockOccupiedAddressRepo{})

	for _, tc := range []struct {
		name string
		url  string
		want int
	}{
		{name: "default", url: "/api/v1/subnets/1/available-ips?range_end=10.30.0.2", want: 50},
		{name: "maximum", url: "/api/v1/subnets/1/available-ips?ip=10.30.0.1&limit=100", want: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
			}
			var response service.ListAvailableIPsResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Page.Limit != tc.want || response.Page.NextCursor != nil {
				t.Fatalf("page = %#v", response.Page)
			}
		})
	}
}

func TestAvailableIPHandler_InvalidRequests(t *testing.T) {
	subnet := availableHandlerSubnet(t, 1, "10.40.0.0/24")
	subnetCalls := 0
	repo := &mockSubnetRepo{getByIDFn: func(_ context.Context, id int64) (*repository.SubnetRead, error) {
		subnetCalls++
		if id != 1 {
			return nil, domain.ErrSubnetNotFound
		}
		return subnet, nil
	}}
	occupied := &mockOccupiedAddressRepo{}
	mux := setupAvailableIPTestMux(repo, occupied)
	tests := []struct {
		name string
		url  string
		code string
	}{
		{name: "malformed subnet", url: "/api/v1/subnets/nope/available-ips", code: "INVALID_REQUEST"},
		{name: "zero subnet", url: "/api/v1/subnets/0/available-ips", code: "INVALID_REQUEST"},
		{name: "missing subnet", url: "/api/v1/subnets/999/available-ips", code: "SUBNET_NOT_FOUND"},
		{name: "zero limit", url: "/api/v1/subnets/1/available-ips?limit=0", code: "INVALID_REQUEST"},
		{name: "negative limit", url: "/api/v1/subnets/1/available-ips?limit=-1", code: "INVALID_REQUEST"},
		{name: "large limit", url: "/api/v1/subnets/1/available-ips?limit=101", code: "INVALID_REQUEST"},
		{name: "non numeric limit", url: "/api/v1/subnets/1/available-ips?limit=none", code: "INVALID_REQUEST"},
		{name: "duplicate limit", url: "/api/v1/subnets/1/available-ips?limit=1&limit=2", code: "INVALID_REQUEST"},
		{name: "malformed cursor", url: "/api/v1/subnets/1/available-ips?cursor=not-a-cursor", code: "INVALID_REQUEST"},
		{name: "malformed range", url: "/api/v1/subnets/1/available-ips?range_start=bad", code: "INVALID_REQUEST"},
		{name: "ipv6 range", url: "/api/v1/subnets/1/available-ips?range_end=2001:db8::1", code: "INVALID_REQUEST"},
		{name: "outside range", url: "/api/v1/subnets/1/available-ips?range_start=10.41.0.1", code: "IP_OUTSIDE_SUBNET"},
		{name: "reverse range", url: "/api/v1/subnets/1/available-ips?range_start=10.40.0.20&range_end=10.40.0.10", code: "INVALID_REQUEST"},
		{name: "malformed exact", url: "/api/v1/subnets/1/available-ips?ip=bad", code: "INVALID_REQUEST"},
		{name: "outside exact", url: "/api/v1/subnets/1/available-ips?ip=10.41.0.1", code: "IP_OUTSIDE_SUBNET"},
		{name: "ip cursor", url: "/api/v1/subnets/1/available-ips?ip=10.40.0.1&cursor=x", code: "INVALID_REQUEST"},
		{name: "ip range start", url: "/api/v1/subnets/1/available-ips?ip=10.40.0.1&range_start=10.40.0.1", code: "INVALID_REQUEST"},
		{name: "ip range end", url: "/api/v1/subnets/1/available-ips?ip=10.40.0.1&range_end=10.40.0.2", code: "INVALID_REQUEST"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != errorStatus(tc.code) {
				t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
			}
			var response ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != tc.code {
				t.Fatalf("response = %#v, decode=%v", response, err)
			}
		})
	}
	if occupied.calls != 0 {
		t.Fatalf("invalid requests queried occupied addresses %d times", occupied.calls)
	}
	if subnetCalls == 0 {
		t.Fatal("expected semantic validation cases to load the subnet")
	}
}

func errorStatus(code string) int {
	if code == "SUBNET_NOT_FOUND" {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

func TestAvailableIPHandler_ExactNetworkAndBroadcastReturnEmpty(t *testing.T) {
	subnet := availableHandlerSubnet(t, 1, "10.50.0.0/30")
	repo := &mockSubnetRepo{getByIDFn: func(_ context.Context, _ int64) (*repository.SubnetRead, error) { return subnet, nil }}
	occupied := &mockOccupiedAddressRepo{}
	mux := setupAvailableIPTestMux(repo, occupied)
	for _, ip := range []string{"10.50.0.0", "10.50.0.3"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/subnets/1/available-ips?ip="+ip, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("ip=%s status=%d body=%s", ip, w.Code, w.Body.String())
		}
		var response service.ListAvailableIPsResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil || len(response.Data) != 0 || response.Page.NextCursor != nil {
			t.Fatalf("ip=%s response=%#v decode=%v", ip, response, err)
		}
	}
	if occupied.calls != 0 {
		t.Fatalf("network/broadcast exact queried allocations %d times", occupied.calls)
	}
}

func TestAvailableIPHandler_InternalErrorIsSanitized(t *testing.T) {
	subnet := availableHandlerSubnet(t, 1, "10.60.0.0/24")
	repo := &mockSubnetRepo{getByIDFn: func(_ context.Context, _ int64) (*repository.SubnetRead, error) { return subnet, nil }}
	mux := setupAvailableIPTestMux(repo, &mockOccupiedAddressRepo{err: errors.New("pq: secret SQL detail")})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/subnets/1/available-ips", nil))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "pq:") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
	}
	var response ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("response = %#v, decode=%v", response, err)
	}
}
