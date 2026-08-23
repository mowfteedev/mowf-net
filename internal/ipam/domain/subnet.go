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
