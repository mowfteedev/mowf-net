package domain

import (
	"time"
)

// Subnet represents the IPAM Subnet domain entity.
type Subnet struct {
	ID          int64
	VlanRefID   *int64
	CIDR        CIDR
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewSubnet constructs a Subnet entity with validated domain CIDR.
func NewSubnet(cidr CIDR, vlanRefID *int64, description string) Subnet {
	return Subnet{
		CIDR:        cidr,
		VlanRefID:   vlanRefID,
		Description: description,
	}
}
