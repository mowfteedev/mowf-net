package postgres

import (
	"errors"
	"testing"

	"github.com/lib/pq"
	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
)

func TestClassifyAllocationInsertErrorRequiresExactStateAndConstraint(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       pq.ErrorCode
		constraint string
		wantMapped bool
	}{
		{name: "exact duplicate", code: "23505", constraint: "ip_allocations_address_uq", wantMapped: true},
		{name: "other unique constraint", code: "23505", constraint: "some_other_unique", wantMapped: false},
		{name: "wrong state exact constraint", code: "23503", constraint: "ip_allocations_address_uq", wantMapped: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &pq.Error{Code: tc.code, Constraint: tc.constraint}
			err := classifyAllocationInsertError(raw)
			if got := errors.Is(err, domain.ErrIPAlreadyAllocated); got != tc.wantMapped {
				t.Fatalf("mapped = %v, want %v (error: %v)", got, tc.wantMapped, err)
			}
			if !tc.wantMapped && !errors.Is(err, raw) {
				t.Fatalf("unmapped error does not wrap original error: %v", err)
			}
		})
	}
}
