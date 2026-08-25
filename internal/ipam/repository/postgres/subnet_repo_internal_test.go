package postgres

import (
	"errors"
	"testing"

	"github.com/lib/pq"
	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
)

func TestClassifySubnetDeleteErrorRequiresExactStateAndConstraint(t *testing.T) {
	tests := []struct {
		name       string
		code       pq.ErrorCode
		constraint string
		wantDomain bool
	}{
		{"exact", "23503", "ip_allocations_subnet_id_fkey", true},
		{"wrong state", "23505", "ip_allocations_subnet_id_fkey", false},
		{"wrong constraint", "23503", "some_other_fkey", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifySubnetDeleteError(&pq.Error{Code: tc.code, Constraint: tc.constraint})
			if got := errors.Is(err, domain.ErrSubnetHasAllocations); got != tc.wantDomain {
				t.Fatalf("classification=%v, want domain=%v (error=%v)", got, tc.wantDomain, err)
			}
		})
	}
}
