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
)
