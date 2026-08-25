package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

const (
	availableIPScanBudget    = 4096
	availableIPCursorVersion = "v1"
)

// AvailableIPDTO represents a derived, non-persisted available IPv4 address.
type AvailableIPDTO struct {
	Address   string `json:"address"`
	State     string `json:"state"`
	Persisted bool   `json:"persisted"`
}

// ListAvailableIPsRequest preserves query-parameter presence so exact-mode
// exclusivity and cursor range-context rules can be validated precisely.
type ListAvailableIPsRequest struct {
	SubnetID      int64
	Limit         int
	Cursor        string
	CursorSet     bool
	RangeStart    string
	RangeStartSet bool
	RangeEnd      string
	RangeEndSet   bool
	IP            string
	IPSet         bool
}

// ListAvailableIPsResponse is the standard list envelope for derived addresses.
type ListAvailableIPsResponse struct {
	Data []*AvailableIPDTO `json:"data"`
	Page PageInfo          `json:"page"`
}

// AvailableIPService computes available addresses without persisting them.
type AvailableIPService struct {
	subnets  repository.SubnetReader
	occupied repository.OccupiedAddressRepository
}

func NewAvailableIPService(subnets repository.SubnetReader, occupied repository.OccupiedAddressRepository) *AvailableIPService {
	return &AvailableIPService{subnets: subnets, occupied: occupied}
}

// ListAvailableIPs executes either exact-IP lookup or bounded range scanning.
func (s *AvailableIPService) ListAvailableIPs(ctx context.Context, req ListAvailableIPsRequest) (*ListAvailableIPsResponse, error) {
	if req.Limit == 0 {
		req.Limit = 50
	}
	if req.SubnetID <= 0 || req.Limit < 1 || req.Limit > 100 {
		return nil, domain.ErrInvalidRequest
	}
	if req.IPSet && (req.CursorSet || req.RangeStartSet || req.RangeEndSet) {
		return nil, domain.ErrInvalidRequest
	}

	ip, err := parseOptionalIPv4(req.IP, req.IPSet)
	if err != nil {
		return nil, err
	}
	rangeStart, err := parseOptionalIPv4(req.RangeStart, req.RangeStartSet)
	if err != nil {
		return nil, err
	}
	rangeEnd, err := parseOptionalIPv4(req.RangeEnd, req.RangeEndSet)
	if err != nil {
		return nil, err
	}

	var cursor *availableIPCursor
	if req.CursorSet {
		decoded, err := decodeAvailableIPCursor(req.Cursor)
		if err != nil {
			return nil, domain.ErrInvalidRequest
		}
		cursor = &decoded
	}

	subnetRead, err := s.subnets.GetByID(ctx, req.SubnetID)
	if err != nil {
		return nil, err
	}
	if req.IPSet {
		return s.listExact(ctx, subnetRead, ip, req.Limit)
	}
	return s.listRange(ctx, subnetRead, req, rangeStart, rangeEnd, cursor)
}

func (s *AvailableIPService) listExact(ctx context.Context, subnet *repository.SubnetRead, ip netip.Addr, limit int) (*ListAvailableIPsResponse, error) {
	response := emptyAvailableIPResponse(limit)
	if !subnet.CIDR.Contains(ip) {
		return nil, domain.ErrIPOutsideSubnet
	}
	if !subnet.CIDR.IsUsable(ip) {
		return response, nil
	}
	occupied, err := s.occupied.ListOccupiedAddresses(ctx, subnet.ID, ip, ip)
	if err != nil {
		return nil, err
	}
	if len(occupied) == 0 {
		response.Data = append(response.Data, newAvailableIPDTO(ip))
	}
	return response, nil
}

func (s *AvailableIPService) listRange(
	ctx context.Context,
	subnet *repository.SubnetRead,
	req ListAvailableIPsRequest,
	rangeStart, rangeEnd netip.Addr,
	cursor *availableIPCursor,
) (*ListAvailableIPsResponse, error) {
	effectiveStart, effectiveEnd, hasCandidates, err := resolveEffectiveRange(subnet, req, rangeStart, rangeEnd, cursor)
	if err != nil {
		return nil, err
	}
	response := emptyAvailableIPResponse(req.Limit)
	if cursor != nil {
		if cursor.SubnetID != req.SubnetID || cursor.RangeStart != effectiveStart || cursor.RangeEnd != effectiveEnd {
			return nil, domain.ErrInvalidRequest
		}
	}
	if !hasCandidates {
		return response, nil
	}

	resume := effectiveStart
	if cursor != nil {
		last := ipv4ToUint32(cursor.LastExamined)
		end := ipv4ToUint32(effectiveEnd)
		if last >= end {
			return nil, domain.ErrInvalidRequest
		}
		resume = uint32ToIPv4(last + 1)
	}

	resumeNumber := uint64(ipv4ToUint32(resume))
	effectiveEndNumber := uint64(ipv4ToUint32(effectiveEnd))
	scanEndNumber := resumeNumber + availableIPScanBudget - 1
	if scanEndNumber > effectiveEndNumber {
		scanEndNumber = effectiveEndNumber
	}
	scanEnd := uint32ToIPv4(uint32(scanEndNumber))
	occupiedAddresses, err := s.occupied.ListOccupiedAddresses(ctx, subnet.ID, resume, scanEnd)
	if err != nil {
		return nil, err
	}
	occupied := make(map[netip.Addr]struct{}, len(occupiedAddresses))
	for _, address := range occupiedAddresses {
		if address.Is4() && compareIPv4(address, resume) >= 0 && compareIPv4(address, scanEnd) <= 0 {
			occupied[address] = struct{}{}
		}
	}

	var lastExamined netip.Addr
	for candidateNumber := resumeNumber; candidateNumber <= scanEndNumber; candidateNumber++ {
		candidate := uint32ToIPv4(uint32(candidateNumber))
		lastExamined = candidate
		if _, isOccupied := occupied[candidate]; !isOccupied {
			response.Data = append(response.Data, newAvailableIPDTO(candidate))
			if len(response.Data) == req.Limit {
				break
			}
		}
	}

	if lastExamined.IsValid() && compareIPv4(lastExamined, effectiveEnd) < 0 {
		encoded := encodeAvailableIPCursor(availableIPCursor{
			SubnetID:     req.SubnetID,
			RangeStart:   effectiveStart,
			RangeEnd:     effectiveEnd,
			LastExamined: lastExamined,
		})
		response.Page.NextCursor = &encoded
	}
	return response, nil
}

func resolveEffectiveRange(
	subnet *repository.SubnetRead,
	req ListAvailableIPsRequest,
	rangeStart, rangeEnd netip.Addr,
	cursor *availableIPCursor,
) (netip.Addr, netip.Addr, bool, error) {
	if req.RangeStartSet && !subnet.CIDR.Contains(rangeStart) {
		return netip.Addr{}, netip.Addr{}, false, domain.ErrIPOutsideSubnet
	}
	if req.RangeEndSet && !subnet.CIDR.Contains(rangeEnd) {
		return netip.Addr{}, netip.Addr{}, false, domain.ErrIPOutsideSubnet
	}

	requestedStart := subnet.CIDR.FirstUsableAddr()
	requestedEnd := subnet.CIDR.LastUsableAddr()
	if req.RangeStartSet {
		requestedStart = rangeStart
	}
	if req.RangeEndSet {
		requestedEnd = rangeEnd
	}
	if compareIPv4(requestedStart, requestedEnd) > 0 {
		return netip.Addr{}, netip.Addr{}, false, domain.ErrInvalidRequest
	}

	effectiveStart := maxIPv4(requestedStart, subnet.CIDR.FirstUsableAddr())
	effectiveEnd := minIPv4(requestedEnd, subnet.CIDR.LastUsableAddr())
	if cursor != nil && !req.RangeStartSet && !req.RangeEndSet {
		effectiveStart = cursor.RangeStart
		effectiveEnd = cursor.RangeEnd
	}
	if compareIPv4(effectiveStart, effectiveEnd) > 0 {
		return effectiveStart, effectiveEnd, false, nil
	}
	if !subnet.CIDR.IsUsable(effectiveStart) || !subnet.CIDR.IsUsable(effectiveEnd) {
		return netip.Addr{}, netip.Addr{}, false, domain.ErrInvalidRequest
	}
	return effectiveStart, effectiveEnd, true, nil
}

func parseOptionalIPv4(raw string, present bool) (netip.Addr, error) {
	if !present {
		return netip.Addr{}, nil
	}
	if raw == "" || raw != strings.TrimSpace(raw) {
		return netip.Addr{}, domain.ErrInvalidRequest
	}
	address, err := netip.ParseAddr(raw)
	if err != nil || !address.Is4() {
		return netip.Addr{}, domain.ErrInvalidRequest
	}
	return address, nil
}

func emptyAvailableIPResponse(limit int) *ListAvailableIPsResponse {
	return &ListAvailableIPsResponse{
		Data: make([]*AvailableIPDTO, 0),
		Page: PageInfo{Limit: limit},
	}
}

func newAvailableIPDTO(address netip.Addr) *AvailableIPDTO {
	return &AvailableIPDTO{Address: address.String(), State: "available", Persisted: false}
}

type availableIPCursor struct {
	SubnetID     int64
	RangeStart   netip.Addr
	RangeEnd     netip.Addr
	LastExamined netip.Addr
}

func encodeAvailableIPCursor(cursor availableIPCursor) string {
	payload := strings.Join([]string{
		availableIPCursorVersion,
		strconv.FormatInt(cursor.SubnetID, 10),
		cursor.RangeStart.String(),
		cursor.RangeEnd.String(),
		cursor.LastExamined.String(),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeAvailableIPCursor(raw string) (availableIPCursor, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return availableIPCursor{}, fmt.Errorf("invalid available-IP cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return availableIPCursor{}, fmt.Errorf("invalid available-IP cursor encoding: %w", err)
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 5 || parts[0] != availableIPCursorVersion {
		return availableIPCursor{}, fmt.Errorf("invalid available-IP cursor payload")
	}
	subnetID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || subnetID <= 0 {
		return availableIPCursor{}, fmt.Errorf("invalid available-IP cursor subnet")
	}
	start, err := parseCursorIPv4(parts[2])
	if err != nil {
		return availableIPCursor{}, err
	}
	end, err := parseCursorIPv4(parts[3])
	if err != nil {
		return availableIPCursor{}, err
	}
	last, err := parseCursorIPv4(parts[4])
	if err != nil {
		return availableIPCursor{}, err
	}
	if compareIPv4(start, end) > 0 || compareIPv4(last, start) < 0 || compareIPv4(last, end) >= 0 {
		return availableIPCursor{}, fmt.Errorf("invalid available-IP cursor progress")
	}
	return availableIPCursor{SubnetID: subnetID, RangeStart: start, RangeEnd: end, LastExamined: last}, nil
}

func parseCursorIPv4(raw string) (netip.Addr, error) {
	address, err := netip.ParseAddr(raw)
	if err != nil || !address.Is4() {
		return netip.Addr{}, fmt.Errorf("invalid available-IP cursor address")
	}
	return address, nil
}

func compareIPv4(left, right netip.Addr) int {
	leftNumber, rightNumber := ipv4ToUint32(left), ipv4ToUint32(right)
	switch {
	case leftNumber < rightNumber:
		return -1
	case leftNumber > rightNumber:
		return 1
	default:
		return 0
	}
}

func minIPv4(left, right netip.Addr) netip.Addr {
	if compareIPv4(left, right) <= 0 {
		return left
	}
	return right
}

func maxIPv4(left, right netip.Addr) netip.Addr {
	if compareIPv4(left, right) >= 0 {
		return left
	}
	return right
}

func ipv4ToUint32(address netip.Addr) uint32 {
	bytes := address.As4()
	return binary.BigEndian.Uint32(bytes[:])
}

func uint32ToIPv4(value uint32) netip.Addr {
	var bytes [4]byte
	binary.BigEndian.PutUint32(bytes[:], value)
	return netip.AddrFrom4(bytes)
}
