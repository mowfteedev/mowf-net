package domain

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// CIDR represents a validated canonical IPv4 subnet CIDR with its derived values.
type CIDR struct {
	network      netip.Addr
	broadcast    netip.Addr
	firstUsable  netip.Addr
	lastUsable   netip.Addr
	prefixLength int
	usableCount  int64
	cidrStr      string
}

// ParseCIDR parses and validates a canonical IPv4 CIDR string (e.g., "192.168.1.0/24").
// It supports prefixes /1 through /30.
// It strictly rejects /31, /32, IPv6, invalid syntax, and non-canonical CIDRs (non-zero host bits).
func ParseCIDR(s string) (CIDR, error) {
	if strings.TrimSpace(s) != s || s == "" {
		return CIDR{}, fmt.Errorf("%w: invalid CIDR string format %q", ErrInvalidCIDR, s)
	}

	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return CIDR{}, fmt.Errorf("%w: CIDR must have exactly one '/' separator in %q", ErrInvalidCIDR, s)
	}

	ipStr, prefixStr := parts[0], parts[1]

	// Reject IPv6
	if strings.Contains(ipStr, ":") {
		return CIDR{}, fmt.Errorf("%w: IPv6 is not supported %q", ErrIPv6NotSupported, s)
	}

	// Validate prefix string format strictly
	if len(prefixStr) == 0 || len(prefixStr) > 2 {
		return CIDR{}, fmt.Errorf("%w: invalid prefix length %q", ErrUnsupportedPrefixLength, prefixStr)
	}
	if prefixStr[0] == '0' || prefixStr[0] == '+' || prefixStr[0] == '-' {
		return CIDR{}, fmt.Errorf("%w: invalid prefix length format %q", ErrUnsupportedPrefixLength, prefixStr)
	}
	for _, r := range prefixStr {
		if r < '0' || r > '9' {
			return CIDR{}, fmt.Errorf("%w: non-numeric prefix length %q", ErrUnsupportedPrefixLength, prefixStr)
		}
	}

	prefixLength, err := strconv.Atoi(prefixStr)
	if err != nil {
		return CIDR{}, fmt.Errorf("%w: invalid prefix length %q", ErrUnsupportedPrefixLength, prefixStr)
	}

	// Phase 1 scope: /1 to /30 only. /31 and /32 must be rejected.
	if prefixLength < 1 || prefixLength > 30 {
		return CIDR{}, fmt.Errorf("%w: prefix length /%d is unsupported (must be /1 to /30)", ErrUnsupportedPrefixLength, prefixLength)
	}

	// Parse IPv4 address strictly
	addr, err := netip.ParseAddr(ipStr)
	if err != nil || !addr.Is4() {
		return CIDR{}, fmt.Errorf("%w: invalid IPv4 address %q", ErrInvalidIPSyntax, ipStr)
	}

	ipBytes := addr.As4()
	ipU32 := binary.BigEndian.Uint32(ipBytes[:])

	mask := uint32(0xFFFFFFFF) << (32 - prefixLength)
	hostMask := ^mask
	netU32 := ipU32 & mask

	// Canonical CIDR check: host bits must be strictly 0
	if ipU32 != netU32 {
		canonicalAddr := uint32ToAddr(netU32)
		return CIDR{}, fmt.Errorf("%w: %s is non-canonical for /%d (canonical network address is %s/%d)",
			ErrNonCanonicalCIDR, s, prefixLength, canonicalAddr.String(), prefixLength)
	}

	broadcastU32 := netU32 | hostMask
	firstUsableU32 := netU32 + 1
	lastUsableU32 := broadcastU32 - 1
	usableCount := (int64(1) << (32 - prefixLength)) - 2

	return CIDR{
		network:      addr,
		broadcast:    uint32ToAddr(broadcastU32),
		firstUsable:  uint32ToAddr(firstUsableU32),
		lastUsable:   uint32ToAddr(lastUsableU32),
		prefixLength: prefixLength,
		usableCount:  usableCount,
		cidrStr:      fmt.Sprintf("%s/%d", addr.String(), prefixLength),
	}, nil
}

// NewCIDRFromParts constructs and validates a CIDR from network IP string and prefix length integer.
// It enforces the exact same strict canonical format as ParseCIDR without silent trimming.
func NewCIDRFromParts(networkIP string, prefixLength int) (CIDR, error) {
	return ParseCIDR(fmt.Sprintf("%s/%d", networkIP, prefixLength))
}

// Network returns the network address as a string.
func (c CIDR) Network() string {
	return c.network.String()
}

// Broadcast returns the broadcast address as a string.
func (c CIDR) Broadcast() string {
	return c.broadcast.String()
}

// FirstUsable returns the first usable host IP address as a string.
func (c CIDR) FirstUsable() string {
	return c.firstUsable.String()
}

// LastUsable returns the last usable host IP address as a string.
func (c CIDR) LastUsable() string {
	return c.lastUsable.String()
}

// PrefixLength returns the prefix length integer (/1 to /30).
func (c CIDR) PrefixLength() int {
	return c.prefixLength
}

// UsableCount returns the total number of usable host addresses in the subnet.
func (c CIDR) UsableCount() int64 {
	return c.usableCount
}

// String returns the CIDR string representation (e.g. "192.168.1.0/24").
func (c CIDR) String() string {
	return c.cidrStr
}

// CIDR returns the CIDR string representation (e.g. "192.168.1.0/24").
func (c CIDR) CIDR() string {
	return c.cidrStr
}

// NetworkAddr returns the network address as netip.Addr.
func (c CIDR) NetworkAddr() netip.Addr {
	return c.network
}

// BroadcastAddr returns the broadcast address as netip.Addr.
func (c CIDR) BroadcastAddr() netip.Addr {
	return c.broadcast
}

// FirstUsableAddr returns the first usable host address as netip.Addr.
func (c CIDR) FirstUsableAddr() netip.Addr {
	return c.firstUsable
}

// LastUsableAddr returns the last usable host address as netip.Addr.
func (c CIDR) LastUsableAddr() netip.Addr {
	return c.lastUsable
}

// Contains checks if the given address falls within the entire subnet range [Network, Broadcast].
func (c CIDR) Contains(addr netip.Addr) bool {
	if !addr.Is4() {
		return false
	}
	addrU32 := addrToUint32(addr)
	netU32 := addrToUint32(c.network)
	bcastU32 := addrToUint32(c.broadcast)
	return addrU32 >= netU32 && addrU32 <= bcastU32
}

// ContainsIPString checks if the given IP string falls within the entire subnet range [Network, Broadcast].
func (c CIDR) ContainsIPString(ipStr string) (bool, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil || !addr.Is4() {
		return false, fmt.Errorf("%w: invalid IPv4 address %q", ErrInvalidIPSyntax, ipStr)
	}
	return c.Contains(addr), nil
}

// IsUsable checks if the given address falls strictly within the usable host range [FirstUsable, LastUsable].
func (c CIDR) IsUsable(addr netip.Addr) bool {
	if !addr.Is4() {
		return false
	}
	addrU32 := addrToUint32(addr)
	firstU32 := addrToUint32(c.firstUsable)
	lastU32 := addrToUint32(c.lastUsable)
	return addrU32 >= firstU32 && addrU32 <= lastU32
}

// IsUsableIPString checks if the given IP string falls strictly within the usable host range [FirstUsable, LastUsable].
func (c CIDR) IsUsableIPString(ipStr string) (bool, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil || !addr.Is4() {
		return false, fmt.Errorf("%w: invalid IPv4 address %q", ErrInvalidIPSyntax, ipStr)
	}
	return c.IsUsable(addr), nil
}

// Overlaps checks if this subnet overlaps with another subnet.
func (c CIDR) Overlaps(other CIDR) bool {
	cNet := addrToUint32(c.network)
	cBcast := addrToUint32(c.broadcast)
	oNet := addrToUint32(other.network)
	oBcast := addrToUint32(other.broadcast)

	return cNet <= oBcast && cBcast >= oNet
}

func uint32ToAddr(n uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	return netip.AddrFrom4(b)
}

func addrToUint32(addr netip.Addr) uint32 {
	b := addr.As4()
	return binary.BigEndian.Uint32(b[:])
}
