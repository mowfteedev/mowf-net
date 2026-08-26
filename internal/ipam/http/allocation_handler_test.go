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
	listFn           func(context.Context, repository.AllocationListFilter) ([]*domain.Allocation, *int64, error)
	beginFn          func(context.Context) (repository.AllocationReservationTransaction, error)
	beginUnreserveFn func(context.Context) (repository.AllocationUnreservationTransaction, error)
}

func (m *mockAllocationRepo) List(ctx context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filter)
	}
	return nil, nil, nil
}

func (m *mockAllocationRepo) BeginReservation(ctx context.Context) (repository.AllocationReservationTransaction, error) {
	if m.beginFn != nil {
		return m.beginFn(ctx)
	}
	return nil, errors.New("unexpected BeginReservation call")
}

func (m *mockAllocationRepo) BeginUnreservation(ctx context.Context) (repository.AllocationUnreservationTransaction, error) {
	if m.beginUnreserveFn != nil {
		return m.beginUnreserveFn(ctx)
	}
	return nil, errors.New("unexpected BeginUnreservation call")
}

type mockReservationTransaction struct {
	lockFn     func(context.Context, int64) (domain.CIDR, error)
	insertFn   func(context.Context, *domain.Allocation) error
	commitFn   func() error
	rollbackFn func() error
}

func (m *mockReservationTransaction) LockSubnet(ctx context.Context, subnetID int64) (domain.CIDR, error) {
	return m.lockFn(ctx, subnetID)
}

func (m *mockReservationTransaction) InsertReserved(ctx context.Context, allocation *domain.Allocation) error {
	return m.insertFn(ctx, allocation)
}

func (m *mockReservationTransaction) Commit() error {
	return m.commitFn()
}

func (m *mockReservationTransaction) Rollback() error {
	return m.rollbackFn()
}

type mockUnreservationTransaction struct {
	lockFn     func(context.Context, int64) (*domain.Allocation, error)
	deleteFn   func(context.Context, int64) error
	commitFn   func() error
	rollbackFn func() error
}

func (m *mockUnreservationTransaction) LockAllocation(ctx context.Context, allocationID int64) (*domain.Allocation, error) {
	return m.lockFn(ctx, allocationID)
}

func (m *mockUnreservationTransaction) DeleteLockedAllocation(ctx context.Context, allocationID int64) error {
	return m.deleteFn(ctx, allocationID)
}

func (m *mockUnreservationTransaction) Commit() error {
	return m.commitFn()
}

func (m *mockUnreservationTransaction) Rollback() error {
	return m.rollbackFn()
}

func mustHTTPTestCIDR(t *testing.T, raw string) domain.CIDR {
	t.Helper()
	cidr, err := domain.ParseCIDR(raw)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", raw, err)
	}
	return cidr
}

func setupAllocationTestServer(repo *mockAllocationRepo) *http.ServeMux {
	mux := http.NewServeMux()
	ipamhttp.NewAllocationHandler(service.NewAllocationService(repo)).RegisterRoutes(mux)
	return mux
}

func setupSuccessfulAllocationReservationServer(t *testing.T, beginCalls *int) *http.ServeMux {
	t.Helper()
	cidr := mustHTTPTestCIDR(t, "192.168.10.0/24")
	tx := &mockReservationTransaction{
		lockFn: func(_ context.Context, subnetID int64) (domain.CIDR, error) {
			if subnetID != 10 {
				t.Fatalf("subnet ID = %d, want 10", subnetID)
			}
			return cidr, nil
		},
		insertFn: func(_ context.Context, allocation *domain.Allocation) error {
			allocation.ID = 100
			return nil
		},
		commitFn:   func() error { return nil },
		rollbackFn: func() error { return nil },
	}
	return setupAllocationTestServer(&mockAllocationRepo{beginFn: func(context.Context) (repository.AllocationReservationTransaction, error) {
		(*beginCalls)++
		return tx, nil
	}})
}

func reserveAllocationRequestBody(t *testing.T, size int) string {
	t.Helper()
	const prefix = `{"subnet_id":10,"address":"192.168.10.20","description":"`
	const suffix = `"}`
	if size < len(prefix)+len(suffix) {
		t.Fatalf("requested body size %d is too small", size)
	}
	body := prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix
	if len(body) != size {
		t.Fatalf("body size = %d, want %d", len(body), size)
	}
	return body
}

func assertReserveAllocationInvalidRequest(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	var response ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != "INVALID_REQUEST" || response.Error.Message != "Invalid request payload" {
		t.Fatalf("error response = %#v", response.Error)
	}
}

func TestAllocationHandler_ListAllocations_HEADAcceptedThroughServeMux(t *testing.T) {
	listCalled := false
	mux := setupAllocationTestServer(&mockAllocationRepo{listFn: func(_ context.Context, _ repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
		listCalled = true
		return nil, nil, nil
	}})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/api/v1/ip-allocations", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !listCalled {
		t.Fatal("list repository path was not reached")
	}
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

func TestAllocationHandler_ReserveAllocationCreatedAndDescriptionDefaults(t *testing.T) {
	for _, tc := range []struct {
		name            string
		body            string
		wantDescription string
	}{
		{name: "preserved", body: `{"subnet_id":10,"address":"192.168.10.20","description":" Printer reservation "}`, wantDescription: " Printer reservation "},
		{name: "omitted defaults empty", body: `{"subnet_id":10,"address":"192.168.10.21"}`, wantDescription: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cidr := mustHTTPTestCIDR(t, "192.168.10.0/24")
			tx := &mockReservationTransaction{
				lockFn: func(_ context.Context, subnetID int64) (domain.CIDR, error) {
					if subnetID != 10 {
						t.Fatalf("subnet ID = %d", subnetID)
					}
					return cidr, nil
				},
				insertFn: func(_ context.Context, allocation *domain.Allocation) error {
					if allocation.Description != tc.wantDescription {
						t.Fatalf("description = %q, want %q", allocation.Description, tc.wantDescription)
					}
					allocation.ID = 100
					return nil
				},
				commitFn:   func() error { return nil },
				rollbackFn: func() error { return nil },
			}
			mux := setupAllocationTestServer(&mockAllocationRepo{beginFn: func(context.Context) (repository.AllocationReservationTransaction, error) {
				return tx, nil
			}})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(tc.body)))
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var response struct {
				Data service.AllocationDTO `json:"data"`
			}
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Data.ID != 100 || response.Data.SubnetID != 10 || response.Data.Status != "reserved" || response.Data.InterfaceID != nil || response.Data.Description != tc.wantDescription {
				t.Fatalf("response data = %#v", response.Data)
			}
		})
	}
}

func TestAllocationHandler_ReserveAllocationRejectsOversizedValidLookingPayload(t *testing.T) {
	beginCalls := 0
	mux := setupSuccessfulAllocationReservationServer(t, &beginCalls)
	body := reserveAllocationRequestBody(t, 16*1024+64)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(body)))

	assertReserveAllocationInvalidRequest(t, w)
	if beginCalls != 0 {
		t.Fatalf("BeginReservation calls = %d, want 0", beginCalls)
	}
}

func TestAllocationHandler_ReserveAllocationRejectsOversizedPayloadWithUnknownContentLength(t *testing.T) {
	beginCalls := 0
	mux := setupSuccessfulAllocationReservationServer(t, &beginCalls)
	body := reserveAllocationRequestBody(t, 16*1024+64)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(body))
	req.ContentLength = -1

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assertReserveAllocationInvalidRequest(t, w)
	if beginCalls != 0 {
		t.Fatalf("BeginReservation calls = %d, want 0", beginCalls)
	}
}

func TestAllocationHandler_ReserveAllocationAcceptsExactRequestBodyLimit(t *testing.T) {
	beginCalls := 0
	mux := setupSuccessfulAllocationReservationServer(t, &beginCalls)
	body := reserveAllocationRequestBody(t, 16*1024)
	if len(body) != 16384 {
		t.Fatalf("body size = %d, want 16384", len(body))
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusCreated, w.Body.String())
	}
	if beginCalls != 1 {
		t.Fatalf("BeginReservation calls = %d, want 1", beginCalls)
	}
}

func TestAllocationHandler_ReserveAllocationRejectsOneByteAboveRequestBodyLimit(t *testing.T) {
	beginCalls := 0
	mux := setupSuccessfulAllocationReservationServer(t, &beginCalls)
	body := reserveAllocationRequestBody(t, 16*1024+1)
	if len(body) != 16385 {
		t.Fatalf("body size = %d, want 16385", len(body))
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(body)))

	assertReserveAllocationInvalidRequest(t, w)
	if beginCalls != 0 {
		t.Fatalf("BeginReservation calls = %d, want 0", beginCalls)
	}
}

func TestAllocationHandler_ReserveAllocationStrictInvalidPayloadDoesNotBeginTransaction(t *testing.T) {
	beginCalls := 0
	mux := setupAllocationTestServer(&mockAllocationRepo{beginFn: func(context.Context) (repository.AllocationReservationTransaction, error) {
		beginCalls++
		return nil, errors.New("must not begin")
	}})
	invalidBodies := map[string]string{
		"empty":                 "",
		"null object":           "null",
		"empty object":          `{}`,
		"missing subnet":        `{"address":"192.168.10.20"}`,
		"missing address":       `{"subnet_id":10}`,
		"zero subnet":           `{"subnet_id":0,"address":"192.168.10.20"}`,
		"negative subnet":       `{"subnet_id":-1,"address":"192.168.10.20"}`,
		"subnet wrong type":     `{"subnet_id":"10","address":"192.168.10.20"}`,
		"address wrong type":    `{"subnet_id":10,"address":20}`,
		"description number":    `{"subnet_id":10,"address":"192.168.10.20","description":1}`,
		"description null":      `{"subnet_id":10,"address":"192.168.10.20","description":null}`,
		"malformed address":     `{"subnet_id":10,"address":"not-an-ip"}`,
		"IPv6":                  `{"subnet_id":10,"address":"2001:db8::1"}`,
		"unknown status":        `{"subnet_id":10,"address":"192.168.10.20","status":"reserved"}`,
		"unknown interface":     `{"subnet_id":10,"address":"192.168.10.20","interface_id":null}`,
		"malformed JSON":        `{"subnet_id":10,`,
		"second JSON value":     `{"subnet_id":10,"address":"192.168.10.20"} {}`,
		"trailing JSON content": `{"subnet_id":10,"address":"192.168.10.20"} true`,
	}
	for name, body := range invalidBodies {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(body)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var response ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != "INVALID_REQUEST" {
				t.Fatalf("response = %#v, decode error = %v", response, err)
			}
		})
	}
	if beginCalls != 0 {
		t.Fatalf("BeginReservation calls = %d, want 0", beginCalls)
	}
}

func TestAllocationHandler_ReserveAllocationErrorMappingAndSanitization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "subnet missing", err: domain.ErrSubnetNotFound, wantStatus: http.StatusNotFound, wantCode: "SUBNET_NOT_FOUND"},
		{name: "outside subnet", err: domain.ErrIPOutsideSubnet, wantStatus: http.StatusBadRequest, wantCode: "IP_OUTSIDE_SUBNET"},
		{name: "not assignable", err: domain.ErrIPNotAssignable, wantStatus: http.StatusConflict, wantCode: "IP_NOT_ASSIGNABLE"},
		{name: "duplicate", err: domain.ErrIPAlreadyAllocated, wantStatus: http.StatusConflict, wantCode: "IP_ALREADY_ALLOCATED"},
		{name: "unexpected", err: errors.New("pq: secret SQL constraint failure"), wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &mockReservationTransaction{
				lockFn:     func(context.Context, int64) (domain.CIDR, error) { return domain.CIDR{}, tc.err },
				insertFn:   func(context.Context, *domain.Allocation) error { return nil },
				commitFn:   func() error { return nil },
				rollbackFn: func() error { return nil },
			}
			mux := setupAllocationTestServer(&mockAllocationRepo{beginFn: func(context.Context) (repository.AllocationReservationTransaction, error) {
				return tx, nil
			}})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(`{"subnet_id":10,"address":"192.168.10.20"}`)))
			if w.Code != tc.wantStatus || strings.Contains(w.Body.String(), "pq:") || strings.Contains(w.Body.String(), "constraint") {
				t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
			}
			var response ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != tc.wantCode {
				t.Fatalf("response = %#v, decode error = %v", response, err)
			}
		})
	}
}

func TestAllocationHandler_UnreserveAllocationReturnsEmptyNoContent(t *testing.T) {
	deleted := false
	tx := &mockUnreservationTransaction{
		lockFn: func(_ context.Context, allocationID int64) (*domain.Allocation, error) {
			if allocationID != 100 {
				t.Fatalf("allocation ID = %d, want 100", allocationID)
			}
			return &domain.Allocation{ID: allocationID, Status: domain.AllocationStatusReserved}, nil
		},
		deleteFn: func(_ context.Context, allocationID int64) error {
			if allocationID != 100 {
				t.Fatalf("deleted allocation ID = %d, want 100", allocationID)
			}
			deleted = true
			return nil
		},
		commitFn:   func() error { return nil },
		rollbackFn: func() error { return nil },
	}
	mux := setupAllocationTestServer(&mockAllocationRepo{beginUnreserveFn: func(context.Context) (repository.AllocationUnreservationTransaction, error) {
		return tx, nil
	}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/ip-allocations/100", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 body length = %d, body = %q", w.Body.Len(), w.Body.String())
	}
	if !deleted {
		t.Fatal("reserved allocation was not deleted")
	}
}

func TestAllocationHandler_UnreserveAllocationInvalidIDsDoNotBeginTransaction(t *testing.T) {
	beginCalls := 0
	mux := setupAllocationTestServer(&mockAllocationRepo{beginUnreserveFn: func(context.Context) (repository.AllocationUnreservationTransaction, error) {
		beginCalls++
		return nil, errors.New("must not begin")
	}})
	for _, path := range []string{
		"/api/v1/ip-allocations/nope",
		"/api/v1/ip-allocations/0",
		"/api/v1/ip-allocations/-1",
	} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, path, nil))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var response ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != "INVALID_REQUEST" {
				t.Fatalf("response = %#v, decode error = %v", response, err)
			}
		})
	}
	if beginCalls != 0 {
		t.Fatalf("BeginUnreservation calls = %d, want 0", beginCalls)
	}
}

func TestAllocationHandler_UnreserveAllocationErrorMappingAndSanitization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		allocation *domain.Allocation
		lockErr    error
		beginErr   error
		wantStatus int
		wantCode   string
	}{
		{name: "missing", lockErr: domain.ErrIPAllocationNotFound, wantStatus: http.StatusNotFound, wantCode: "IP_ALLOCATION_NOT_FOUND"},
		{name: "assigned", allocation: &domain.Allocation{ID: 100, Status: domain.AllocationStatusAssigned}, wantStatus: http.StatusConflict, wantCode: "IP_NOT_ASSIGNABLE"},
		{name: "unexpected", beginErr: errors.New("pq: secret SQL table ip_allocations failure"), wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &mockUnreservationTransaction{
				lockFn: func(context.Context, int64) (*domain.Allocation, error) { return tc.allocation, tc.lockErr },
				deleteFn: func(context.Context, int64) error {
					t.Fatal("unexpected delete")
					return nil
				},
				commitFn:   func() error { return nil },
				rollbackFn: func() error { return nil },
			}
			mux := setupAllocationTestServer(&mockAllocationRepo{beginUnreserveFn: func(context.Context) (repository.AllocationUnreservationTransaction, error) {
				if tc.beginErr != nil {
					return nil, tc.beginErr
				}
				return tx, nil
			}})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/ip-allocations/100", nil))
			if w.Code != tc.wantStatus || strings.Contains(w.Body.String(), "pq:") || strings.Contains(w.Body.String(), "ip_allocations") {
				t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
			}
			var response ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != tc.wantCode {
				t.Fatalf("response = %#v, decode error = %v", response, err)
			}
		})
	}
}
