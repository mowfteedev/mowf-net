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

type mockAllocationRepo struct {
	listFn func(context.Context, repository.AllocationListFilter) ([]*domain.Allocation, *int64, error)
}

func (m *mockAllocationRepo) List(ctx context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filter)
	}
	return nil, nil, nil
}

func setupAllocationTestServer(repo *mockAllocationRepo) *http.ServeMux {
	mux := http.NewServeMux()
	ipamhttp.NewAllocationHandler(service.NewAllocationService(repo)).RegisterRoutes(mux)
	return mux
}

func TestAllocationHandler_ListAllocations_ReturnsPersistedRowsAndFilters(t *testing.T) {
	interfaceID := int64(9)
	var gotFilter repository.AllocationListFilter
	mux := setupAllocationTestServer(&mockAllocationRepo{listFn: func(_ context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
		gotFilter = filter
		return []*domain.Allocation{
			{ID: 1, SubnetID: 10, Address: netip.MustParseAddr("192.168.10.20"), Status: domain.AllocationStatusReserved, Description: "Printer reservation"},
			{ID: 2, SubnetID: 10, Address: netip.MustParseAddr("192.168.10.21"), Status: domain.AllocationStatusAssigned, InterfaceID: &interfaceID},
		}, nil, nil
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ip-allocations?subnet_id=10&status=reserved&address=192.168.10.20&interface_id=9&limit=100", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if gotFilter.SubnetID == nil || *gotFilter.SubnetID != 10 || gotFilter.Status == nil || *gotFilter.Status != domain.AllocationStatusReserved || gotFilter.Address == nil || gotFilter.Address.String() != "192.168.10.20" || gotFilter.InterfaceID == nil || *gotFilter.InterfaceID != 9 || gotFilter.Limit != 100 {
		t.Fatalf("unexpected filter: %#v", gotFilter)
	}

	var response service.ListAllocationsResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 2 || response.Data[0].Address != "192.168.10.20" || response.Data[0].InterfaceID != nil {
		t.Fatalf("unexpected data: %#v", response.Data)
	}
	if response.Data[1].Status != "assigned" || response.Data[1].InterfaceID == nil || *response.Data[1].InterfaceID != 9 {
		t.Fatalf("assigned allocation = %#v", response.Data[1])
	}
}

func TestAllocationHandler_ListAllocations_DefaultLimitsAndPagination(t *testing.T) {
	var filters []repository.AllocationListFilter
	nextID := int64(3)
	mux := setupAllocationTestServer(&mockAllocationRepo{listFn: func(_ context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
		filters = append(filters, filter)
		if filter.Cursor == nil {
			return []*domain.Allocation{{ID: 1, Address: netip.MustParseAddr("10.0.0.1"), Status: domain.AllocationStatusReserved}}, &nextID, nil
		}
		return []*domain.Allocation{{ID: 4, Address: netip.MustParseAddr("10.0.0.4"), Status: domain.AllocationStatusAssigned}}, nil, nil
	}})

	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/ip-allocations", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	var firstResponse service.ListAllocationsResponse
	if err := json.NewDecoder(first.Body).Decode(&firstResponse); err != nil {
		t.Fatal(err)
	}
	if firstResponse.Page.Limit != 50 || firstResponse.Page.NextCursor == nil {
		t.Fatalf("first page = %#v", firstResponse.Page)
	}

	second := httptest.NewRecorder()
	mux.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/ip-allocations?cursor="+*firstResponse.Page.NextCursor+"&limit=1", nil))
	if second.Code != http.StatusOK || len(filters) != 2 || filters[1].Cursor == nil || *filters[1].Cursor != nextID || filters[1].Limit != 1 {
		t.Fatalf("continuation status/filter = %d/%#v", second.Code, filters)
	}
}

func TestAllocationHandler_ListAllocations_InvalidParametersDoNotReachRepository(t *testing.T) {
	called := false
	mux := setupAllocationTestServer(&mockAllocationRepo{listFn: func(_ context.Context, _ repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
		called = true
		return nil, nil, nil
	}})
	invalidQueries := []string{
		"limit=0", "limit=-1", "limit=101", "limit=none", "cursor=not-a-cursor",
		"subnet_id=0", "subnet_id=nope", "interface_id=0", "interface_id=nope",
		"status=available", "status=%20RESERVED", "address=not-an-ip", "address=2001:db8::1", "address=",
	}
	for _, query := range invalidQueries {
		t.Run(query, func(t *testing.T) {
			called = false
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/ip-allocations?"+query, nil))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var response ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != "INVALID_REQUEST" {
				t.Fatalf("error response = %#v, decode error = %v", response, err)
			}
			if called {
				t.Fatal("repository was called for invalid parameters")
			}
		})
	}
}

func TestAllocationHandler_ListAllocations_RepositoryErrorIsSanitized(t *testing.T) {
	mux := setupAllocationTestServer(&mockAllocationRepo{listFn: func(_ context.Context, _ repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
		return nil, nil, errors.New("pq: relation ip_allocations does not exist")
	}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/ip-allocations", nil))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "pq:") {
		t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
	}
	var response ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("error response = %#v, decode error = %v", response, err)
	}
}
