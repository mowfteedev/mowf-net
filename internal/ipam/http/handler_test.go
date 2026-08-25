package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	ipamhttp "github.com/mowfteedev/mowf-net/internal/ipam/http"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

type mockSubnetRepo struct {
	createFn  func(ctx context.Context, subnet *domain.Subnet) error
	getByIDFn func(ctx context.Context, id int64) (*repository.SubnetRead, error)
	listFn    func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error)
	updateFn  func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error)
	deleteFn  func(ctx context.Context, id int64) error
}

func (m *mockSubnetRepo) Create(ctx context.Context, subnet *domain.Subnet) error {
	if m.createFn != nil {
		return m.createFn(ctx, subnet)
	}
	subnet.ID = 1
	return nil
}

func (m *mockSubnetRepo) GetByID(ctx context.Context, id int64) (*repository.SubnetRead, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	return nil, domain.ErrSubnetNotFound
}

func (m *mockSubnetRepo) List(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filter)
	}
	return nil, nil, nil
}

func (m *mockSubnetRepo) Update(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, id, patch)
	}
	return nil, domain.ErrSubnetNotFound
}

func (m *mockSubnetRepo) Delete(ctx context.Context, id int64) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return domain.ErrSubnetNotFound
}

func setupTestServer(repo *mockSubnetRepo) *http.ServeMux {
	svc := service.NewSubnetService(repo)
	handler := ipamhttp.NewSubnetHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux
}

// ----------------------------------------------------
// POST /api/v1/subnets Tests
// ----------------------------------------------------

func TestSubnetHandler_CreateSubnet_Success(t *testing.T) {
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			subnet.ID = 1
			return nil
		},
	}
	mux := setupTestServer(repo)

	body := `{"cidr":"192.168.10.0/24","vlan_ref_id":null,"description":"Lab LAN"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201 Created, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data service.SubnetDTO `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	data := resp.Data
	if data.ID != 1 {
		t.Errorf("ID = %d, want 1", data.ID)
	}
	if data.CIDR != "192.168.10.0/24" {
		t.Errorf("CIDR = %s, want 192.168.10.0/24", data.CIDR)
	}
	if data.Network != "192.168.10.0" {
		t.Errorf("Network = %s, want 192.168.10.0", data.Network)
	}
	if data.Broadcast != "192.168.10.255" {
		t.Errorf("Broadcast = %s, want 192.168.10.255", data.Broadcast)
	}
	if data.FirstUsable != "192.168.10.1" {
		t.Errorf("FirstUsable = %s, want 192.168.10.1", data.FirstUsable)
	}
	if data.LastUsable != "192.168.10.254" {
		t.Errorf("LastUsable = %s, want 192.168.10.254", data.LastUsable)
	}
	if data.UsableCount != 254 {
		t.Errorf("UsableCount = %d, want 254", data.UsableCount)
	}
	if data.AssignedCount != 0 {
		t.Errorf("AssignedCount = %d, want 0", data.AssignedCount)
	}
	if data.ReservedCount != 0 {
		t.Errorf("ReservedCount = %d, want 0", data.ReservedCount)
	}
	if data.AvailableCount != 254 {
		t.Errorf("AvailableCount = %d, want 254", data.AvailableCount)
	}
	if data.VlanRefID != nil {
		t.Errorf("VlanRefID = %v, want nil", data.VlanRefID)
	}
	if data.Description != "Lab LAN" {
		t.Errorf("Description = %s, want 'Lab LAN'", data.Description)
	}
}

func TestSubnetHandler_CreateSubnet_MalformedJSON(t *testing.T) {
	var repoCalled bool
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			repoCalled = true
			return nil
		},
	}
	mux := setupTestServer(repo)

	malformedBodies := []struct {
		name string
		body string
	}{
		{"invalid syntax", `{invalid-json`},
		{"type mismatch", `{"cidr": "192.168.1.0/24", "vlan_ref_id": "not-a-number"}`},
		{"empty string", ``},
		{"whitespace only", `   `},
		{"valid object with trailing garbage", `{"cidr":"192.168.10.0/24"} garbage`},
		{"valid object with second JSON object", `{"cidr":"192.168.10.0/24"} {"another":"object"}`},
		{"valid object with trailing number", `{"cidr":"192.168.10.0/24"} 123`},
	}

	for _, tt := range malformedBodies {
		t.Run(tt.name, func(t *testing.T) {
			repoCalled = false
			req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400 Bad Request, got %d. Body: %s", w.Code, w.Body.String())
			}

			var errResp ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}

			if errResp.Error.Code != "INVALID_REQUEST" {
				t.Errorf("error.code = %s, want INVALID_REQUEST for %s", errResp.Error.Code, tt.name)
			}

			if repoCalled {
				t.Errorf("repository must not be called when request payload is malformed for %s", tt.name)
			}
		})
	}
}

func TestSubnetHandler_CreateSubnet_TrailingWhitespace_Accepted(t *testing.T) {
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			subnet.ID = 10
			return nil
		},
	}
	mux := setupTestServer(repo)

	body := `{"cidr":"192.168.10.0/24","vlan_ref_id":null,"description":"Lab LAN"}   ` + "\n\t  \n"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201 Created for trailing whitespace, got %d. Body: %s", w.Code, w.Body.String())
	}
}

func TestSubnetHandler_CreateSubnet_InvalidCIDR(t *testing.T) {
	mux := setupTestServer(&mockSubnetRepo{})

	tests := []struct {
		name string
		body string
	}{
		{"non-canonical host bits", `{"cidr":"192.168.10.5/24"}`},
		{"unsupported /31", `{"cidr":"192.168.10.0/31"}`},
		{"unsupported /32", `{"cidr":"192.168.10.1/32"}`},
		{"IPv6", `{"cidr":"2001:db8::/32"}`},
		{"invalid format", `{"cidr":"not-an-ip"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400 Bad Request, got %d. Body: %s", w.Code, w.Body.String())
			}

			var errResp ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}

			if errResp.Error.Code != "INVALID_CIDR" {
				t.Errorf("error.code = %s, want INVALID_CIDR", errResp.Error.Code)
			}
			if errResp.Error.Message == "" {
				t.Errorf("error.message should not be empty")
			}
		})
	}
}

func TestSubnetHandler_CreateSubnet_VlanNotFound(t *testing.T) {
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			return domain.ErrVlanNotFound
		},
	}
	mux := setupTestServer(repo)

	body := `{"cidr":"192.168.10.0/24","vlan_ref_id":99999}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 Not Found, got %d. Body: %s", w.Code, w.Body.String())
	}

	var errResp ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Error.Code != "VLAN_NOT_FOUND" {
		t.Errorf("error.code = %s, want VLAN_NOT_FOUND", errResp.Error.Code)
	}
}

func TestSubnetHandler_CreateSubnet_Overlap(t *testing.T) {
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			return domain.ErrSubnetOverlap
		},
	}
	mux := setupTestServer(repo)

	body := `{"cidr":"192.168.10.0/24"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected status 409 Conflict, got %d. Body: %s", w.Code, w.Body.String())
	}

	var errResp ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Error.Code != "SUBNET_OVERLAP" {
		t.Errorf("error.code = %s, want SUBNET_OVERLAP", errResp.Error.Code)
	}
}

func TestSubnetHandler_CreateSubnet_InternalError_Sanitized(t *testing.T) {
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			return errors.New("pq: connection refused password=secret host=10.0.0.1")
		},
	}
	mux := setupTestServer(repo)

	body := `{"cidr":"192.168.10.0/24"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 Internal Server Error, got %d. Body: %s", w.Code, w.Body.String())
	}

	var errResp ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("error.code = %s, want INTERNAL_ERROR", errResp.Error.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("secret")) || bytes.Contains(w.Body.Bytes(), []byte("10.0.0.1")) {
		t.Errorf("error response leaked internal details: %s", w.Body.String())
	}
}

// ----------------------------------------------------
// GET /api/v1/subnets/{subnet_id} Tests
// ----------------------------------------------------

func TestSubnetHandler_GetSubnet_Success(t *testing.T) {
	cidr, _ := domain.ParseCIDR("192.168.10.0/24")
	vlanRef := int64(5)
	mockSub := &repository.SubnetRead{Subnet: domain.Subnet{
		ID:          10,
		CIDR:        cidr,
		VlanRefID:   &vlanRef,
		Description: "Lab LAN",
	}}

	repo := &mockSubnetRepo{
		getByIDFn: func(ctx context.Context, id int64) (*repository.SubnetRead, error) {
			if id == 10 {
				return mockSub, nil
			}
			return nil, domain.ErrSubnetNotFound
		},
	}
	mux := setupTestServer(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets/10", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data service.SubnetDTO `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	data := resp.Data
	if data.ID != 10 {
		t.Errorf("ID = %d, want 10", data.ID)
	}
	if data.CIDR != "192.168.10.0/24" {
		t.Errorf("CIDR = %s, want 192.168.10.0/24", data.CIDR)
	}
	if data.UsableCount != 254 {
		t.Errorf("UsableCount = %d, want 254", data.UsableCount)
	}
	if data.AvailableCount != 254 {
		t.Errorf("AvailableCount = %d, want 254", data.AvailableCount)
	}
	if data.VlanRefID == nil || *data.VlanRefID != 5 {
		t.Errorf("VlanRefID = %v, want 5", data.VlanRefID)
	}
}

func TestSubnetHandler_GetSubnet_NotFound(t *testing.T) {
	repo := &mockSubnetRepo{
		getByIDFn: func(ctx context.Context, id int64) (*repository.SubnetRead, error) {
			return nil, domain.ErrSubnetNotFound
		},
	}
	mux := setupTestServer(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets/999", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 Not Found, got %d. Body: %s", w.Code, w.Body.String())
	}

	var errResp ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Error.Code != "SUBNET_NOT_FOUND" {
		t.Errorf("error.code = %s, want SUBNET_NOT_FOUND", errResp.Error.Code)
	}
}

func TestSubnetHandler_GetSubnet_InvalidPathID(t *testing.T) {
	mux := setupTestServer(&mockSubnetRepo{})

	invalidPaths := []string{
		"/api/v1/subnets/abc",
		"/api/v1/subnets/0",
		"/api/v1/subnets/-5",
	}

	for _, path := range invalidPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()

			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400 Bad Request for %s, got %d. Body: %s", path, w.Code, w.Body.String())
			}

			var errResp ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}

			if errResp.Error.Code != "INVALID_REQUEST" {
				t.Errorf("error.code = %s, want INVALID_REQUEST for %s", errResp.Error.Code, path)
			}
		})
	}
}

func TestSubnetHandler_GetSubnet_InternalError_Sanitized(t *testing.T) {
	repo := &mockSubnetRepo{
		getByIDFn: func(ctx context.Context, id int64) (*repository.SubnetRead, error) {
			return nil, errors.New("pq: fatal error on db 10.0.0.1 password=secret")
		},
	}
	mux := setupTestServer(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets/10", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 Internal Server Error, got %d", w.Code)
	}

	var errResp ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("error.code = %s, want INTERNAL_ERROR", errResp.Error.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("secret")) || bytes.Contains(w.Body.Bytes(), []byte("10.0.0.1")) {
		t.Errorf("error response leaked internal DB details: %s", w.Body.String())
	}
}

// ----------------------------------------------------
// GET /api/v1/subnets Tests
// ----------------------------------------------------

func TestSubnetHandler_ListSubnets_Success(t *testing.T) {
	cidr1, _ := domain.ParseCIDR("192.168.1.0/24")
	cidr2, _ := domain.ParseCIDR("192.168.2.0/24")
	mockList := []*repository.SubnetRead{
		{Subnet: domain.Subnet{ID: 1, CIDR: cidr1, Description: "LAN 1"}},
		{Subnet: domain.Subnet{ID: 2, CIDR: cidr2, Description: "LAN 2"}},
	}

	repo := &mockSubnetRepo{
		listFn: func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
			next := int64(2)
			return mockList, &next, nil
		},
	}
	mux := setupTestServer(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets?limit=2", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp service.ListSubnetsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Fatalf("len(data) = %d, want 2", len(resp.Data))
	}
	if resp.Page.Limit != 2 {
		t.Errorf("page.limit = %d, want 2", resp.Page.Limit)
	}
	if resp.Page.NextCursor == nil {
		t.Fatalf("expected non-nil next_cursor")
	}
}

// TestSubnetHandler_ListSubnets_DefaultLimit verifies that omitting the limit
// parameter causes the service to receive 50 (the canonical default).
func TestSubnetHandler_ListSubnets_DefaultLimit(t *testing.T) {
	var capturedLimit int
	repo := &mockSubnetRepo{
		listFn: func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
			capturedLimit = filter.Limit
			return nil, nil, nil
		},
	}
	mux := setupTestServer(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp service.ListSubnetsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// The canonical default limit is 50; repo must have received it.
	if capturedLimit != 50 {
		t.Errorf("repository received limit=%d, want 50", capturedLimit)
	}
	if resp.Page.Limit != 50 {
		t.Errorf("page.limit = %d, want 50", resp.Page.Limit)
	}
}

// TestSubnetHandler_ListSubnets_MaxLimit verifies that limit=100 is accepted
// and the repository receives exactly 100.
func TestSubnetHandler_ListSubnets_MaxLimit(t *testing.T) {
	var capturedLimit int
	repo := &mockSubnetRepo{
		listFn: func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
			capturedLimit = filter.Limit
			return nil, nil, nil
		},
	}
	mux := setupTestServer(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets?limit=100", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for limit=100, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp service.ListSubnetsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if capturedLimit != 100 {
		t.Errorf("repository received limit=%d, want 100", capturedLimit)
	}
	if resp.Page.Limit != 100 {
		t.Errorf("page.limit = %d, want 100", resp.Page.Limit)
	}
}

// TestSubnetHandler_ListSubnets_AboveMaxRejected verifies that limit values
// exceeding 100 are rejected with 400 INVALID_REQUEST before the repository
// is ever invoked. Public requests must never be silently clamped.
func TestSubnetHandler_ListSubnets_AboveMaxRejected(t *testing.T) {
	tests := []struct {
		name  string
		limit string
	}{
		{"one above max (101)", "101"},
		{"well above max (500)", "500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var repoCalled bool
			repo := &mockSubnetRepo{
				listFn: func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
					repoCalled = true
					return nil, nil, nil
				},
			}
			mux := setupTestServer(repo)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets?limit="+tt.limit, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("limit=%s: expected 400 INVALID_REQUEST, got %d. Body: %s", tt.limit, w.Code, w.Body.String())
			}

			var errResp ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}
			if errResp.Error.Code != "INVALID_REQUEST" {
				t.Errorf("error.code = %s, want INVALID_REQUEST", errResp.Error.Code)
			}
			if repoCalled {
				t.Errorf("limit=%s: repository.List must NOT be called for out-of-range limit", tt.limit)
			}
		})
	}
}

// TestSubnetHandler_ListSubnets_InternalError_Sanitized verifies that a List
// repository failure producing internal sensitive data is sanitized before
// returning to the client.
func TestSubnetHandler_ListSubnets_InternalError_Sanitized(t *testing.T) {
	repo := &mockSubnetRepo{
		listFn: func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
			// Simulate a raw DB error containing sensitive internal details.
			return nil, nil, errors.New("pq: connection failed host=10.0.0.1 password=secret sql=SELECT * FROM subnets")
		},
	}
	mux := setupTestServer(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subnets", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error, got %d. Body: %s", w.Code, w.Body.String())
	}

	var errResp ipamhttp.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("error.code = %s, want INTERNAL_ERROR", errResp.Error.Code)
	}

	body := w.Body.Bytes()
	sensitiveTerms := []string{"secret", "10.0.0.1", "SELECT", "pq:", "password", "sql="}
	for _, term := range sensitiveTerms {
		if bytes.Contains(body, []byte(term)) {
			t.Errorf("response body leaked sensitive internal data %q: %s", term, string(body))
		}
	}
}

func TestSubnetHandler_ListSubnets_MalformedParameters(t *testing.T) {
	mux := setupTestServer(&mockSubnetRepo{})

	tests := []struct {
		name string
		url  string
	}{
		{"invalid limit not integer", "/api/v1/subnets?limit=abc"},
		{"negative limit", "/api/v1/subnets?limit=-5"},
		{"zero limit", "/api/v1/subnets?limit=0"},
		{"limit=-1", "/api/v1/subnets?limit=-1"},
		{"invalid cursor not base64", "/api/v1/subnets?cursor=invalid_base64_string!"},
		{"cursor not numeric base64", "/api/v1/subnets?cursor=YWJj"},
		{"invalid vlan_ref_id", "/api/v1/subnets?vlan_ref_id=not-int"},
		{"negative vlan_ref_id", "/api/v1/subnets?vlan_ref_id=-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()

			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400 Bad Request for %s, got %d. Body: %s", tt.name, w.Code, w.Body.String())
			}

			var errResp ipamhttp.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}

			if errResp.Error.Code != "INVALID_REQUEST" {
				t.Errorf("error.code = %s, want INVALID_REQUEST for %s", errResp.Error.Code, tt.name)
			}
		})
	}
}

func TestSubnetHandler_Patch(t *testing.T) {
	t.Run("successful presence-aware fields", func(t *testing.T) {
		tests := []struct {
			name  string
			body  string
			check func(t *testing.T, patch repository.UpdateSubnet)
		}{
			{"CIDR only", `{"cidr":"192.168.10.0/25"}`, func(t *testing.T, p repository.UpdateSubnet) {
				if !p.CIDRSet || p.CIDR != "192.168.10.0/25" || p.DescriptionSet || p.VlanRefIDSet {
					t.Fatalf("unexpected CIDR patch: %+v", p)
				}
			}},
			{"description only", `{"description":"new"}`, func(t *testing.T, p repository.UpdateSubnet) {
				if !p.DescriptionSet || p.Description != "new" || p.CIDRSet || p.VlanRefIDSet {
					t.Fatalf("unexpected description patch: %+v", p)
				}
			}},
			{"description empty", `{"description":""}`, func(t *testing.T, p repository.UpdateSubnet) {
				if !p.DescriptionSet || p.Description != "" {
					t.Fatalf("unexpected empty description patch: %+v", p)
				}
			}},
			{"VLAN set", `{"vlan_ref_id":5}`, func(t *testing.T, p repository.UpdateSubnet) {
				if !p.VlanRefIDSet || p.VlanRefID == nil || *p.VlanRefID != 5 {
					t.Fatalf("unexpected VLAN patch: %+v", p)
				}
			}},
			{"VLAN null unlink", `{"vlan_ref_id":null}`, func(t *testing.T, p repository.UpdateSubnet) {
				if !p.VlanRefIDSet || p.VlanRefID != nil {
					t.Fatalf("unexpected VLAN unlink patch: %+v", p)
				}
			}},
			{"combined", `{"cidr":"192.168.10.0/25","description":"combined","vlan_ref_id":7}`, func(t *testing.T, p repository.UpdateSubnet) {
				if !p.CIDRSet || !p.DescriptionSet || !p.VlanRefIDSet || p.VlanRefID == nil || *p.VlanRefID != 7 {
					t.Fatalf("unexpected combined patch: %+v", p)
				}
			}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				baseCIDR, _ := domain.ParseCIDR("192.168.10.0/24")
				repo := &mockSubnetRepo{updateFn: func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
					if id != 10 {
						t.Fatalf("update id=%d, want 10", id)
					}
					tc.check(t, patch)
					subnet := domain.Subnet{ID: id, CIDR: baseCIDR, Description: "old"}
					if patch.CIDRSet {
						cidr, err := domain.ParseCIDR(patch.CIDR)
						if err != nil {
							t.Fatalf("mock received invalid CIDR: %v", err)
						}
						subnet.CIDR = cidr
					}
					if patch.DescriptionSet {
						subnet.Description = patch.Description
					}
					if patch.VlanRefIDSet {
						subnet.VlanRefID = patch.VlanRefID
					}
					return &repository.SubnetRead{Subnet: subnet, AssignedCount: 2, ReservedCount: 3}, nil
				}}
				mux := setupTestServer(repo)
				req := httptest.NewRequest(http.MethodPatch, "/api/v1/subnets/10", bytes.NewBufferString(tc.body))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
				}
				var response struct {
					Data service.SubnetDTO `json:"data"`
				}
				if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
					t.Fatalf("failed to decode PATCH response: %v", err)
				}
				if response.Data.AssignedCount != 2 || response.Data.ReservedCount != 3 {
					t.Fatalf("PATCH counts=%d/%d, want 2/3", response.Data.AssignedCount, response.Data.ReservedCount)
				}
			})
		}
	})

	t.Run("invalid payload never calls repository", func(t *testing.T) {
		tests := []struct {
			name string
			body string
		}{
			{"empty object", `{}`},
			{"unknown field", `{"description":"x","unknown":1}`},
			{"only unknown", `{"unknown":1}`},
			{"malformed", `{"cidr":`},
			{"trailing garbage", `{"description":"x"} garbage`},
			{"second JSON value", `{"description":"x"} {"vlan_ref_id":1}`},
			{"CIDR null", `{"cidr":null}`},
			{"description null", `{"description":null}`},
			{"VLAN zero", `{"vlan_ref_id":0}`},
			{"VLAN negative", `{"vlan_ref_id":-1}`},
			{"CIDR wrong type", `{"cidr":10}`},
			{"description wrong type", `{"description":false}`},
			{"VLAN wrong type", `{"vlan_ref_id":"5"}`},
			{"VLAN fractional", `{"vlan_ref_id":1.5}`},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				called := false
				repo := &mockSubnetRepo{updateFn: func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
					called = true
					return nil, nil
				}}
				mux := setupTestServer(repo)
				req := httptest.NewRequest(http.MethodPatch, "/api/v1/subnets/10", bytes.NewBufferString(tc.body))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status=%d, want 400; body=%s", w.Code, w.Body.String())
				}
				var response ipamhttp.ErrorResponse
				if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Error.Code != "INVALID_REQUEST" {
					t.Fatalf("error=%+v decode=%v, want INVALID_REQUEST", response, err)
				}
				if called {
					t.Fatal("repository called for malformed PATCH")
				}
			})
		}
	})

	t.Run("invalid and noncanonical CIDR", func(t *testing.T) {
		called := false
		repo := &mockSubnetRepo{updateFn: func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
			called = true
			if !patch.CIDRSet || patch.CIDR != "192.168.10.5/24" {
				t.Fatalf("unexpected raw invalid CIDR patch: %+v", patch)
			}
			return nil, domain.ErrInvalidCIDR
		}}
		mux := setupTestServer(repo)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/subnets/10", bytes.NewBufferString(`{"cidr":"192.168.10.5/24"}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"INVALID_CIDR"`)) {
			t.Fatalf("unexpected invalid CIDR response: status=%d body=%s", w.Code, w.Body.String())
		}
		if !called {
			t.Fatal("repository was not called for CIDR-present PATCH")
		}
	})

	t.Run("path ID errors", func(t *testing.T) {
		for _, path := range []string{"/api/v1/subnets/abc", "/api/v1/subnets/0", "/api/v1/subnets/-1"} {
			called := false
			repo := &mockSubnetRepo{updateFn: func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
				called = true
				return nil, nil
			}}
			mux := setupTestServer(repo)
			req := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(`{"description":"x"}`))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"INVALID_REQUEST"`)) || called {
				t.Fatalf("path=%s status=%d called=%v body=%s", path, w.Code, called, w.Body.String())
			}
		}
	})

	t.Run("domain and internal errors", func(t *testing.T) {
		tests := []struct {
			name       string
			err        error
			wantStatus int
			wantCode   string
		}{
			{"missing subnet", domain.ErrSubnetNotFound, 404, "SUBNET_NOT_FOUND"},
			{"missing VLAN", domain.ErrVlanNotFound, 404, "VLAN_NOT_FOUND"},
			{"overlap", domain.ErrSubnetOverlap, 409, "SUBNET_OVERLAP"},
			{"unsafe resize", domain.ErrSubnetResizeConflict, 409, "SUBNET_RESIZE_CONFLICT"},
			{"internal", errors.New("pq: host=10.0.0.1 password=secret constraint=raw"), 500, "INTERNAL_ERROR"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				repo := &mockSubnetRepo{updateFn: func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
					return nil, tc.err
				}}
				mux := setupTestServer(repo)
				req := httptest.NewRequest(http.MethodPatch, "/api/v1/subnets/10", bytes.NewBufferString(`{"description":"x"}`))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != tc.wantStatus || !bytes.Contains(w.Body.Bytes(), []byte(`"`+tc.wantCode+`"`)) {
					t.Fatalf("status=%d body=%s, want %d %s", w.Code, w.Body.String(), tc.wantStatus, tc.wantCode)
				}
				for _, secret := range []string{"secret", "10.0.0.1", "constraint=raw"} {
					if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
						t.Fatalf("PATCH response leaked %q: %s", secret, w.Body.String())
					}
				}
			})
		}
	})
}

func TestSubnetHandler_Delete(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		repoErr    error
		wantStatus int
		wantCode   string
	}{
		{"success", "/api/v1/subnets/10", nil, http.StatusNoContent, ""},
		{"invalid ID", "/api/v1/subnets/abc", nil, http.StatusBadRequest, "INVALID_REQUEST"},
		{"nonpositive ID", "/api/v1/subnets/0", nil, http.StatusBadRequest, "INVALID_REQUEST"},
		{"missing", "/api/v1/subnets/10", domain.ErrSubnetNotFound, http.StatusNotFound, "SUBNET_NOT_FOUND"},
		{"allocations", "/api/v1/subnets/10", domain.ErrSubnetHasAllocations, http.StatusConflict, "SUBNET_HAS_ALLOCATIONS"},
		{"internal", "/api/v1/subnets/10", errors.New("pq: password=secret host=10.0.0.1"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			repo := &mockSubnetRepo{deleteFn: func(ctx context.Context, id int64) error {
				called = true
				return tc.repoErr
			}}
			mux := setupTestServer(repo)
			req := httptest.NewRequest(http.MethodDelete, tc.path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantStatus == http.StatusNoContent {
				if w.Body.Len() != 0 {
					t.Fatalf("204 response body length=%d, want zero", w.Body.Len())
				}
			} else if !bytes.Contains(w.Body.Bytes(), []byte(`"`+tc.wantCode+`"`)) {
				t.Fatalf("response body=%s, want code %s", w.Body.String(), tc.wantCode)
			}
			if (tc.path == "/api/v1/subnets/abc" || tc.path == "/api/v1/subnets/0") && called {
				t.Fatal("repository called for invalid DELETE ID")
			}
			for _, secret := range []string{"secret", "10.0.0.1", "pq:"} {
				if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
					t.Fatalf("DELETE response leaked %q: %s", secret, w.Body.String())
				}
			}
		})
	}
}
