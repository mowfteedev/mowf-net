package domain

import (
	"errors"
	"testing"
)

func TestParseCIDR_Valid(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedNet    string
		expectedBcast  string
		expectedFirst  string
		expectedLast   string
		expectedUsable int64
		expectedPrefix int
	}{
		{
			name:           "standard /24 subnet",
			input:          "192.168.1.0/24",
			expectedNet:    "192.168.1.0",
			expectedBcast:  "192.168.1.255",
			expectedFirst:  "192.168.1.1",
			expectedLast:   "192.168.1.254",
			expectedUsable: 254,
			expectedPrefix: 24,
		},
		{
			name:           "standard /8 subnet",
			input:          "10.0.0.0/8",
			expectedNet:    "10.0.0.0",
			expectedBcast:  "10.255.255.255",
			expectedFirst:  "10.0.0.1",
			expectedLast:   "10.255.255.254",
			expectedUsable: 16777214,
			expectedPrefix: 8,
		},
		{
			name:           "standard /16 subnet",
			input:          "172.16.0.0/16",
			expectedNet:    "172.16.0.0",
			expectedBcast:  "172.16.255.255",
			expectedFirst:  "172.16.0.1",
			expectedLast:   "172.16.255.254",
			expectedUsable: 65534,
			expectedPrefix: 16,
		},
		{
			name:           "boundary /1 lower half",
			input:          "0.0.0.0/1",
			expectedNet:    "0.0.0.0",
			expectedBcast:  "127.255.255.255",
			expectedFirst:  "0.0.0.1",
			expectedLast:   "127.255.255.254",
			expectedUsable: 2147483646,
			expectedPrefix: 1,
		},
		{
			name:           "boundary /1 upper half",
			input:          "128.0.0.0/1",
			expectedNet:    "128.0.0.0",
			expectedBcast:  "255.255.255.255",
			expectedFirst:  "128.0.0.1",
			expectedLast:   "255.255.255.254",
			expectedUsable: 2147483646,
			expectedPrefix: 1,
		},
		{
			name:           "boundary /30 standard",
			input:          "192.168.1.0/30",
			expectedNet:    "192.168.1.0",
			expectedBcast:  "192.168.1.3",
			expectedFirst:  "192.168.1.1",
			expectedLast:   "192.168.1.2",
			expectedUsable: 2,
			expectedPrefix: 30,
		},
		{
			name:           "boundary /30 offset",
			input:          "10.0.0.4/30",
			expectedNet:    "10.0.0.4",
			expectedBcast:  "10.0.0.7",
			expectedFirst:  "10.0.0.5",
			expectedLast:   "10.0.0.6",
			expectedUsable: 2,
			expectedPrefix: 30,
		},
		{
			name:           "boundary /30 high octet",
			input:          "172.16.0.252/30",
			expectedNet:    "172.16.0.252",
			expectedBcast:  "172.16.0.255",
			expectedFirst:  "172.16.0.253",
			expectedLast:   "172.16.0.254",
			expectedUsable: 2,
			expectedPrefix: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cidr, err := ParseCIDR(tt.input)
			if err != nil {
				t.Fatalf("ParseCIDR(%q) unexpected error: %v", tt.input, err)
			}

			if cidr.Network() != tt.expectedNet {
				t.Errorf("Network() = %v, want %v", cidr.Network(), tt.expectedNet)
			}
			if cidr.Broadcast() != tt.expectedBcast {
				t.Errorf("Broadcast() = %v, want %v", cidr.Broadcast(), tt.expectedBcast)
			}
			if cidr.FirstUsable() != tt.expectedFirst {
				t.Errorf("FirstUsable() = %v, want %v", cidr.FirstUsable(), tt.expectedFirst)
			}
			if cidr.LastUsable() != tt.expectedLast {
				t.Errorf("LastUsable() = %v, want %v", cidr.LastUsable(), tt.expectedLast)
			}
			if cidr.UsableCount() != tt.expectedUsable {
				t.Errorf("UsableCount() = %v, want %v", cidr.UsableCount(), tt.expectedUsable)
			}
			if cidr.PrefixLength() != tt.expectedPrefix {
				t.Errorf("PrefixLength() = %v, want %v", cidr.PrefixLength(), tt.expectedPrefix)
			}
			if cidr.CIDR() != tt.input {
				t.Errorf("CIDR() = %v, want %v", cidr.CIDR(), tt.input)
			}
			if cidr.String() != tt.input {
				t.Errorf("String() = %v, want %v", cidr.String(), tt.input)
			}
		})
	}
}

func TestParseCIDR_RejectUnsupportedPrefixes(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"reject /31", "192.168.1.0/31"},
		{"reject /32", "192.168.1.0/32"},
		{"reject /31 class A", "10.0.0.0/31"},
		{"reject /32 host", "10.0.0.1/32"},
		{"reject /0 default route", "0.0.0.0/0"},
		{"reject /33", "192.168.1.0/33"},
		{"reject /100", "192.168.1.0/100"},
		{"reject negative prefix", "192.168.1.0/-1"},
		{"reject empty prefix", "192.168.1.0/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCIDR(tt.input)
			if err == nil {
				t.Fatalf("ParseCIDR(%q) expected error, got nil", tt.input)
			}
			if !errors.Is(err, ErrInvalidCIDR) {
				t.Errorf("ParseCIDR(%q) error %v should wrap ErrInvalidCIDR", tt.input, err)
			}
		})
	}
}

func TestParseCIDR_RejectIPv6(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"IPv6 documentation prefix", "2001:db8::/32"},
		{"IPv6 loopback", "::1/128"},
		{"IPv6 link local", "fe80::1/64"},
		{"IPv6 all zeros", "::/0"},
		{"IPv6 full address", "2001:0db8:85a3:0000:0000:8a2e:0370:7334/64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCIDR(tt.input)
			if err == nil {
				t.Fatalf("ParseCIDR(%q) expected error for IPv6, got nil", tt.input)
			}
			if !errors.Is(err, ErrInvalidCIDR) {
				t.Errorf("ParseCIDR(%q) error %v should wrap ErrInvalidCIDR", tt.input, err)
			}
		})
	}
}

func TestParseCIDR_RejectNonCanonical(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"non-canonical /24 host 10", "192.168.1.10/24"},
		{"non-canonical /8 host 1", "10.0.0.1/8"},
		{"non-canonical /30 host 1", "192.168.1.1/30"},
		{"non-canonical /30 broadcast", "192.168.1.3/30"},
		{"non-canonical /1 host 1", "0.0.0.1/1"},
		{"non-canonical /1 high host", "128.0.0.1/1"},
		{"non-canonical /16 host bit", "172.16.0.1/16"},
		{"non-canonical /16 third octet set", "172.16.255.0/16"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCIDR(tt.input)
			if err == nil {
				t.Fatalf("ParseCIDR(%q) expected error for non-canonical CIDR, got nil", tt.input)
			}
			if !errors.Is(err, ErrInvalidCIDR) {
				t.Errorf("ParseCIDR(%q) error %v should wrap ErrInvalidCIDR", tt.input, err)
			}
		})
	}
}

func TestParseCIDR_RejectInvalidSyntax(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"missing slash", "192.168.1.0"},
		{"multiple slashes", "192.168.1.0/24/24"},
		{"alphabetic prefix", "192.168.1.0/abc"},
		{"prefix with plus", "192.168.1.0/+24"},
		{"prefix with leading zero 024", "192.168.1.0/024"},
		{"prefix with leading zero 08", "10.0.0.0/08"},
		{"octet out of range", "256.0.0.0/24"},
		{"octet 999", "999.999.999.999/24"},
		{"five octets", "192.168.1.0.1/24"},
		{"three octets", "192.168.1/24"},
		{"leading zero in octet", "192.168.01.0/24"},
		{"alphabetic IP", "abc.def.ghi.jkl/24"},
		{"leading space", " 192.168.1.0/24"},
		{"trailing space", "192.168.1.0/24 "},
		{"random text", "invalid cidr format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCIDR(tt.input)
			if err == nil {
				t.Fatalf("ParseCIDR(%q) expected error for invalid syntax, got nil", tt.input)
			}
			if !errors.Is(err, ErrInvalidCIDR) {
				t.Errorf("ParseCIDR(%q) error %v should wrap ErrInvalidCIDR", tt.input, err)
			}
		})
	}
}

func TestNewCIDRFromParts(t *testing.T) {
	t.Run("valid parts", func(t *testing.T) {
		cidr, err := NewCIDRFromParts("192.168.1.0", 24)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cidr.CIDR() != "192.168.1.0/24" {
			t.Errorf("CIDR() = %v, want 192.168.1.0/24", cidr.CIDR())
		}
	})

	t.Run("non-canonical parts", func(t *testing.T) {
		_, err := NewCIDRFromParts("192.168.1.10", 24)
		if err == nil {
			t.Fatalf("expected error for non-canonical network IP, got nil")
		}
	})

	t.Run("unsupported prefix part /31", func(t *testing.T) {
		_, err := NewCIDRFromParts("192.168.1.0", 31)
		if err == nil {
			t.Fatalf("expected error for /31, got nil")
		}
	})
}

func TestCIDR_ContainsAndIsUsable(t *testing.T) {
	cidr, err := ParseCIDR("192.168.1.0/24")
	if err != nil {
		t.Fatalf("failed to parse CIDR: %v", err)
	}

	tests := []struct {
		ip            string
		expectContain bool
		expectUsable  bool
	}{
		{"192.168.1.0", true, false},    // network address: contained, not usable
		{"192.168.1.1", true, true},     // first usable
		{"192.168.1.100", true, true},   // intermediate host
		{"192.168.1.254", true, true},   // last usable
		{"192.168.1.255", true, false},  // broadcast address: contained, not usable
		{"192.168.2.1", false, false},   // outside
		{"192.168.0.254", false, false}, // outside
		{"10.0.0.1", false, false},      // outside
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			contained, err := cidr.ContainsIPString(tt.ip)
			if err != nil {
				t.Fatalf("ContainsIPString(%q) error: %v", tt.ip, err)
			}
			if contained != tt.expectContain {
				t.Errorf("ContainsIPString(%q) = %v, want %v", tt.ip, contained, tt.expectContain)
			}

			usable, err := cidr.IsUsableIPString(tt.ip)
			if err != nil {
				t.Fatalf("IsUsableIPString(%q) error: %v", tt.ip, err)
			}
			if usable != tt.expectUsable {
				t.Errorf("IsUsableIPString(%q) = %v, want %v", tt.ip, usable, tt.expectUsable)
			}
		})
	}
}

func TestCIDR_Overlaps(t *testing.T) {
	tests := []struct {
		name            string
		cidrA           string
		cidrB           string
		expectedOverlap bool
	}{
		{
			name:            "subset overlap (/24 inside /16)",
			cidrA:           "192.168.0.0/16",
			cidrB:           "192.168.1.0/24",
			expectedOverlap: true,
		},
		{
			name:            "subset overlap (/25 inside /24)",
			cidrA:           "192.168.1.0/24",
			cidrB:           "192.168.1.128/25",
			expectedOverlap: true,
		},
		{
			name:            "exact same CIDR",
			cidrA:           "192.168.1.0/24",
			cidrB:           "192.168.1.0/24",
			expectedOverlap: true,
		},
		{
			name:            "adjacent /24 subnets (no overlap)",
			cidrA:           "192.168.1.0/24",
			cidrB:           "192.168.2.0/24",
			expectedOverlap: false,
		},
		{
			name:            "completely distinct networks (no overlap)",
			cidrA:           "10.0.0.0/8",
			cidrB:           "172.16.0.0/12",
			expectedOverlap: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := ParseCIDR(tt.cidrA)
			if err != nil {
				t.Fatalf("ParseCIDR(%q) error: %v", tt.cidrA, err)
			}
			b, err := ParseCIDR(tt.cidrB)
			if err != nil {
				t.Fatalf("ParseCIDR(%q) error: %v", tt.cidrB, err)
			}

			if got := a.Overlaps(b); got != tt.expectedOverlap {
				t.Errorf("(%s).Overlaps(%s) = %v, want %v", tt.cidrA, tt.cidrB, got, tt.expectedOverlap)
			}
			if got := b.Overlaps(a); got != tt.expectedOverlap {
				t.Errorf("(%s).Overlaps(%s) = %v, want %v", tt.cidrB, tt.cidrA, got, tt.expectedOverlap)
			}
		})
	}
}
