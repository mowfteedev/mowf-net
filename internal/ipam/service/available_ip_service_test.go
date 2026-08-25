package service

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

type availableSubnetReader struct {
	subnets map[int64]*repository.SubnetRead
	err     error
	calls   int
}

func (r *availableSubnetReader) GetByID(_ context.Context, id int64) (*repository.SubnetRead, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	subnet, ok := r.subnets[id]
	if !ok {
		return nil, domain.ErrSubnetNotFound
	}
	return subnet, nil
}

type occupiedWindowCall struct {
	subnetID int64
	start    netip.Addr
	end      netip.Addr
}

type occupiedAddressReader struct {
	addresses []netip.Addr
	err       error
	calls     []occupiedWindowCall
}

func (r *occupiedAddressReader) ListOccupiedAddresses(_ context.Context, subnetID int64, start, end netip.Addr) ([]netip.Addr, error) {
	r.calls = append(r.calls, occupiedWindowCall{subnetID: subnetID, start: start, end: end})
	if r.err != nil {
		return nil, r.err
	}
	result := make([]netip.Addr, 0)
	for _, address := range r.addresses {
		if compareIPv4(address, start) >= 0 && compareIPv4(address, end) <= 0 {
			result = append(result, address)
		}
	}
	return result, nil
}

func testSubnetRead(t *testing.T, id int64, cidrString string) *repository.SubnetRead {
	t.Helper()
	cidr, err := domain.ParseCIDR(cidrString)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", cidrString, err)
	}
	return &repository.SubnetRead{Subnet: domain.Subnet{ID: id, CIDR: cidr}}
}

func newAvailableIPTestService(t *testing.T, cidrString string, occupied ...string) (*AvailableIPService, *availableSubnetReader, *occupiedAddressReader) {
	t.Helper()
	subnets := &availableSubnetReader{subnets: map[int64]*repository.SubnetRead{1: testSubnetRead(t, 1, cidrString)}}
	addresses := make([]netip.Addr, len(occupied))
	for i, raw := range occupied {
		addresses[i] = netip.MustParseAddr(raw)
	}
	occupiedReader := &occupiedAddressReader{addresses: addresses}
	return NewAvailableIPService(subnets, occupiedReader), subnets, occupiedReader
}

func availableAddresses(response *ListAvailableIPsResponse) []string {
	addresses := make([]string, len(response.Data))
	for i, item := range response.Data {
		addresses[i] = item.Address
	}
	return addresses
}

func TestAvailableIPService_DefaultRangeExcludesOccupiedInNumericOrder(t *testing.T) {
	service, _, occupied := newAvailableIPTestService(t, "192.168.10.0/24", "192.168.10.2", "192.168.10.4")
	response, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 5})
	if err != nil {
		t.Fatalf("ListAvailableIPs: %v", err)
	}
	want := []string{"192.168.10.1", "192.168.10.3", "192.168.10.5", "192.168.10.6", "192.168.10.7"}
	if got := availableAddresses(response); !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	if response.Page.Limit != 5 || response.Page.NextCursor == nil {
		t.Fatalf("page = %#v", response.Page)
	}
	if len(occupied.calls) != 1 || occupied.calls[0].start.String() != "192.168.10.1" || occupied.calls[0].end.String() != "192.168.10.254" {
		t.Fatalf("occupied calls = %#v", occupied.calls)
	}
	cursor, err := decodeAvailableIPCursor(*response.Page.NextCursor)
	if err != nil || cursor.LastExamined.String() != "192.168.10.7" {
		t.Fatalf("cursor = %#v, %v", cursor, err)
	}
}

func TestAvailableIPService_DefaultAndInvalidLimits(t *testing.T) {
	service, subnets, _ := newAvailableIPTestService(t, "192.168.11.0/24")
	response, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1})
	if err != nil {
		t.Fatalf("default limit: %v", err)
	}
	if response.Page.Limit != 50 || len(response.Data) != 50 {
		t.Fatalf("default response = page:%#v data:%d", response.Page, len(response.Data))
	}
	beforeInvalid := subnets.calls
	for _, limit := range []int{-1, 101} {
		if _, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: limit}); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("limit %d error = %v", limit, err)
		}
	}
	if subnets.calls != beforeInvalid {
		t.Fatal("invalid service limits reached the subnet repository")
	}
}

func TestAvailableIPService_Slash30Boundaries(t *testing.T) {
	service, _, occupied := newAvailableIPTestService(t, "255.255.255.252/30")
	response, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := availableAddresses(response), []string{"255.255.255.253", "255.255.255.254"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("/30 addresses = %v, want %v", got, want)
	}
	if response.Page.NextCursor != nil {
		t.Fatalf("exhausted /30 cursor = %q", *response.Page.NextCursor)
	}
	if len(occupied.calls) != 1 || occupied.calls[0].start.String() != "255.255.255.253" || occupied.calls[0].end.String() != "255.255.255.254" {
		t.Fatalf("/30 query window = %#v", occupied.calls)
	}
}

func TestAvailableIPService_ExactMode(t *testing.T) {
	service, _, occupied := newAvailableIPTestService(t, "192.168.20.0/24", "192.168.20.2", "192.168.20.3")
	tests := []struct {
		name      string
		ip        string
		want      []string
		wantError error
	}{
		{name: "available", ip: "192.168.20.1", want: []string{"192.168.20.1"}},
		{name: "reserved", ip: "192.168.20.2", want: []string{}},
		{name: "assigned", ip: "192.168.20.3", want: []string{}},
		{name: "network", ip: "192.168.20.0", want: []string{}},
		{name: "broadcast", ip: "192.168.20.255", want: []string{}},
		{name: "outside", ip: "192.168.21.1", wantError: domain.ErrIPOutsideSubnet},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 100, IP: tc.ip, IPSet: true})
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v, want %v", err, tc.wantError)
			}
			if tc.wantError != nil {
				return
			}
			if got := availableAddresses(response); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("addresses = %v, want %v", got, tc.want)
			}
			if response.Page.Limit != 100 || response.Page.NextCursor != nil {
				t.Fatalf("page = %#v", response.Page)
			}
		})
	}
	// Network, broadcast, and outside checks do not query allocation existence.
	if len(occupied.calls) != 3 {
		t.Fatalf("exact occupied query count = %d, want 3", len(occupied.calls))
	}
}

func TestAvailableIPService_ExactShapeAndMalformedValidation(t *testing.T) {
	service, subnets, occupied := newAvailableIPTestService(t, "10.0.0.0/24")
	tests := []ListAvailableIPsRequest{
		{SubnetID: 1, Limit: 50, IP: "bad", IPSet: true},
		{SubnetID: 1, Limit: 50, IP: "2001:db8::1", IPSet: true},
		{SubnetID: 1, Limit: 50, IP: "10.0.0.1", IPSet: true, Cursor: "x", CursorSet: true},
		{SubnetID: 1, Limit: 50, IP: "10.0.0.1", IPSet: true, RangeStart: "10.0.0.1", RangeStartSet: true},
		{SubnetID: 1, Limit: 50, IP: "10.0.0.1", IPSet: true, RangeEnd: "10.0.0.2", RangeEndSet: true},
	}
	for i, req := range tests {
		if _, err := service.ListAvailableIPs(context.Background(), req); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("case %d error = %v, want ErrInvalidRequest", i, err)
		}
	}
	if subnets.calls != 0 || len(occupied.calls) != 0 {
		t.Fatalf("invalid exact queries reached repositories: subnet=%d occupied=%d", subnets.calls, len(occupied.calls))
	}
}

func TestAvailableIPService_RangeBounds(t *testing.T) {
	tests := []struct {
		name      string
		request   ListAvailableIPsRequest
		want      []string
		wantError error
	}{
		{name: "range start only", request: ListAvailableIPsRequest{RangeStart: "192.168.30.10", RangeStartSet: true, Limit: 2}, want: []string{"192.168.30.10", "192.168.30.11"}},
		{name: "range end only", request: ListAvailableIPsRequest{RangeEnd: "192.168.30.2", RangeEndSet: true, Limit: 10}, want: []string{"192.168.30.1", "192.168.30.2"}},
		{name: "both bounds", request: ListAvailableIPsRequest{RangeStart: "192.168.30.5", RangeStartSet: true, RangeEnd: "192.168.30.6", RangeEndSet: true, Limit: 10}, want: []string{"192.168.30.5", "192.168.30.6"}},
		{name: "network broadcast bounds", request: ListAvailableIPsRequest{RangeStart: "192.168.30.0", RangeStartSet: true, RangeEnd: "192.168.30.255", RangeEndSet: true, Limit: 2}, want: []string{"192.168.30.1", "192.168.30.2"}},
		{name: "network only intersection empty", request: ListAvailableIPsRequest{RangeStart: "192.168.30.0", RangeStartSet: true, RangeEnd: "192.168.30.0", RangeEndSet: true, Limit: 10}, want: []string{}},
		{name: "reverse", request: ListAvailableIPsRequest{RangeStart: "192.168.30.20", RangeStartSet: true, RangeEnd: "192.168.30.10", RangeEndSet: true, Limit: 10}, wantError: domain.ErrInvalidRequest},
		{name: "start outside", request: ListAvailableIPsRequest{RangeStart: "192.168.31.1", RangeStartSet: true, Limit: 10}, wantError: domain.ErrIPOutsideSubnet},
		{name: "end outside", request: ListAvailableIPsRequest{RangeEnd: "192.168.29.1", RangeEndSet: true, Limit: 10}, wantError: domain.ErrIPOutsideSubnet},
		{name: "malformed start", request: ListAvailableIPsRequest{RangeStart: "bad", RangeStartSet: true, Limit: 10}, wantError: domain.ErrInvalidRequest},
		{name: "ipv6 end", request: ListAvailableIPsRequest{RangeEnd: "2001:db8::1", RangeEndSet: true, Limit: 10}, wantError: domain.ErrInvalidRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, _, _ := newAvailableIPTestService(t, "192.168.30.0/24")
			tc.request.SubnetID = 1
			response, err := service.ListAvailableIPs(context.Background(), tc.request)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v, want %v", err, tc.wantError)
			}
			if tc.wantError == nil && !reflect.DeepEqual(availableAddresses(response), tc.want) {
				t.Fatalf("addresses = %v, want %v", availableAddresses(response), tc.want)
			}
		})
	}
}

func TestAvailableIPService_CursorContinuationAndContext(t *testing.T) {
	service, _, occupied := newAvailableIPTestService(t, "10.10.0.0/24", "10.10.0.10")
	first, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{
		SubnetID: 1, Limit: 2,
		RangeStart: "10.10.0.10", RangeStartSet: true,
		RangeEnd: "10.10.0.15", RangeEndSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := availableAddresses(first), []string{"10.10.0.11", "10.10.0.12"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first page = %v, want %v", got, want)
	}
	decoded, err := decodeAvailableIPCursor(*first.Page.NextCursor)
	if err != nil || decoded.RangeStart.String() != "10.10.0.10" || decoded.RangeEnd.String() != "10.10.0.15" || decoded.LastExamined.String() != "10.10.0.12" {
		t.Fatalf("decoded cursor = %#v, %v", decoded, err)
	}

	second, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 2, Cursor: *first.Page.NextCursor, CursorSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := availableAddresses(second), []string{"10.10.0.13", "10.10.0.14"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second page = %v, want %v", got, want)
	}
	if call := occupied.calls[1]; call.start.String() != "10.10.0.13" {
		t.Fatalf("continuation query starts at %s, want strictly after last examined", call.start)
	}
	third, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 2, Cursor: *second.Page.NextCursor, CursorSet: true})
	if err != nil {
		t.Fatal(err)
	}
	allPages := append(append(availableAddresses(first), availableAddresses(second)...), availableAddresses(third)...)
	if want := []string{"10.10.0.11", "10.10.0.12", "10.10.0.13", "10.10.0.14", "10.10.0.15"}; !reflect.DeepEqual(allPages, want) {
		t.Fatalf("stable pages = %v, want %v", allPages, want)
	}
	if third.Page.NextCursor != nil {
		t.Fatalf("exhausted final page cursor = %v", third.Page.NextCursor)
	}

	_, err = service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{
		SubnetID: 1, Limit: 2, Cursor: *first.Page.NextCursor, CursorSet: true,
		RangeStart: "10.10.0.10", RangeStartSet: true,
		RangeEnd: "10.10.0.15", RangeEndSet: true,
	})
	if err != nil {
		t.Fatalf("matching explicit cursor range failed: %v", err)
	}
	_, err = service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{
		SubnetID: 1, Limit: 2, Cursor: *first.Page.NextCursor, CursorSet: true,
		RangeStart: "10.10.0.11", RangeStartSet: true,
		RangeEnd: "10.10.0.15", RangeEndSet: true,
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("conflicting cursor range error = %v", err)
	}

	otherSubnets := &availableSubnetReader{subnets: map[int64]*repository.SubnetRead{2: testSubnetRead(t, 2, "10.10.0.0/24")}}
	otherService := NewAvailableIPService(otherSubnets, &occupiedAddressReader{})
	_, err = otherService.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 2, Limit: 2, Cursor: *first.Page.NextCursor, CursorSet: true})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("cross-subnet cursor error = %v", err)
	}
}

func TestAvailableIPService_CursorRejectsMalformedPayloads(t *testing.T) {
	service, subnets, _ := newAvailableIPTestService(t, "10.20.0.0/24")
	encode := func(payload string) string { return base64.RawURLEncoding.EncodeToString([]byte(payload)) }
	cursors := []string{
		"%%%",
		encode("v1|1|10.20.0.1"),
		encode("v2|1|10.20.0.1|10.20.0.10|10.20.0.2"),
		encode("v1|1|bad|10.20.0.10|10.20.0.2"),
		encode("v1|1|10.20.0.1|10.20.0.10|10.20.0.11"),
		encode("v1|1|10.20.0.1|10.20.0.10|10.20.0.10"),
	}
	for _, cursor := range cursors {
		_, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 50, Cursor: cursor, CursorSet: true})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("cursor %q error = %v", cursor, err)
		}
	}
	if subnets.calls != 0 {
		t.Fatalf("malformed cursors reached subnet repository %d times", subnets.calls)
	}
}

func TestAvailableIPService_BoundedSlashOneOccupiedWindow(t *testing.T) {
	subnets := &availableSubnetReader{subnets: map[int64]*repository.SubnetRead{1: testSubnetRead(t, 1, "0.0.0.0/1")}}
	occupied := &occupiedAddressReader{}
	occupied.addresses = make([]netip.Addr, availableIPScanBudget)
	for i := 0; i < availableIPScanBudget; i++ {
		occupied.addresses[i] = uint32ToIPv4(uint32(i + 1))
	}
	service := NewAvailableIPService(subnets, occupied)
	response, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 0 || response.Page.NextCursor == nil {
		t.Fatalf("occupied /1 response = %#v", response)
	}
	if len(occupied.calls) != 1 {
		t.Fatalf("occupied repository calls = %d, want 1", len(occupied.calls))
	}
	call := occupied.calls[0]
	windowSize := uint64(ipv4ToUint32(call.end)) - uint64(ipv4ToUint32(call.start)) + 1
	if windowSize != availableIPScanBudget || call.start.String() != "0.0.0.1" || call.end.String() != "0.0.16.0" {
		t.Fatalf("bounded /1 window = %s..%s (%d)", call.start, call.end, windowSize)
	}
	cursor, err := decodeAvailableIPCursor(*response.Page.NextCursor)
	if err != nil || cursor.LastExamined != call.end {
		t.Fatalf("bounded cursor = %#v, %v; query end=%s", cursor, err, call.end)
	}

	occupied.addresses = occupied.addresses[:availableIPScanBudget-1]
	occupied.calls = nil
	response, err = service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := availableAddresses(response), []string{"0.0.16.0"}; !reflect.DeepEqual(got, want) || response.Page.NextCursor == nil {
		t.Fatalf("partially available bounded page = %v, cursor=%v", got, response.Page.NextCursor)
	}
}

func TestAvailableIPService_RepositoryErrorsPropagate(t *testing.T) {
	subnetFailure := errors.New("subnet database failure")
	service := NewAvailableIPService(&availableSubnetReader{err: subnetFailure}, &occupiedAddressReader{})
	if _, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 50}); !errors.Is(err, subnetFailure) {
		t.Fatalf("subnet error = %v", err)
	}

	occupiedFailure := errors.New("allocation database failure")
	service, _, occupied := newAvailableIPTestService(t, "10.0.0.0/24")
	occupied.err = occupiedFailure
	if _, err := service.ListAvailableIPs(context.Background(), ListAvailableIPsRequest{SubnetID: 1, Limit: 50}); !errors.Is(err, occupiedFailure) {
		t.Fatalf("occupied error = %v", err)
	}
}
