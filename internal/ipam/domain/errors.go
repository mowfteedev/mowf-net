package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidCIDR is the base sentinel error for all invalid CIDR cases.
	ErrInvalidCIDR = errors.New("INVALID_CIDR")

	// Specific sentinel errors that wrap ErrInvalidCIDR:
	ErrNonCanonicalCIDR        = fmt.Errorf("%w: non-canonical CIDR (host bits must be 0)", ErrInvalidCIDR)
	ErrUnsupportedPrefixLength = fmt.Errorf("%w: prefix length must be between 1 and 30", ErrInvalidCIDR)
	ErrIPv6NotSupported        = fmt.Errorf("%w: IPv6 is not supported in Phase 1", ErrInvalidCIDR)
	ErrInvalidIPSyntax         = fmt.Errorf("%w: invalid IPv4 address format", ErrInvalidCIDR)

	// Subnet domain errors
	ErrSubnetOverlap        = errors.New("SUBNET_OVERLAP")
	ErrSubnetNotFound       = errors.New("SUBNET_NOT_FOUND")
	ErrSubnetResizeConflict = errors.New("SUBNET_RESIZE_CONFLICT")
	ErrSubnetHasAllocations = errors.New("SUBNET_HAS_ALLOCATIONS")
	ErrVlanNotFound         = errors.New("VLAN_NOT_FOUND")
	ErrIPOutsideSubnet      = errors.New("IP_OUTSIDE_SUBNET")
	ErrIPNotAssignable      = errors.New("IP_NOT_ASSIGNABLE")
	ErrIPAlreadyAllocated   = errors.New("IP_ALREADY_ALLOCATED")
	ErrIPAllocationNotFound = errors.New("IP_ALLOCATION_NOT_FOUND")
	ErrInvalidRequest       = errors.New("INVALID_REQUEST")
)
