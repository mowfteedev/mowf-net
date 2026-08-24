package http_test

import (
	"bytes"
	"context"
	"encoding/json"
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
		{"malformed JSON", `{invalid-json`},
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
