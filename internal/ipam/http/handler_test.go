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
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

type mockSubnetRepo struct {
	createFn func(ctx context.Context, subnet *domain.Subnet) error
}

func (m *mockSubnetRepo) Create(ctx context.Context, subnet *domain.Subnet) error {
	if m.createFn != nil {
		return m.createFn(ctx, subnet)
	}
	subnet.ID = 1
	return nil
}

func setupTestServer(repo *mockSubnetRepo) *http.ServeMux {
	svc := service.NewSubnetService(repo)
	handler := ipamhttp.NewSubnetHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux
}

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
