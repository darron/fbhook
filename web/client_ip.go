package web

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

const maxForwardedBytes = 4096
const maxForwardedHops = 32

func parseTrustedProxyCIDRs(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[netip.Prefix]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return nil, errors.New("invalid trusted proxy CIDR")
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil || !prefix.IsValid() || prefix.Addr().Zone() != "" || prefix != prefix.Masked() {
			return nil, errors.New("invalid trusted proxy CIDR")
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, errors.New("invalid trusted proxy CIDR")
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		if _, ok := seen[prefix]; ok {
			return nil, errors.New("duplicate trusted proxy CIDR")
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func parseSocketIP(remoteAddr string) (netip.Addr, error) {
	if addrPort, err := netip.ParseAddrPort(remoteAddr); err == nil {
		addr := addrPort.Addr()
		if addr.Zone() != "" {
			return netip.Addr{}, errors.New("zoned socket address")
		}
		return addr.Unmap(), nil
	}
	addr, err := netip.ParseAddr(remoteAddr)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, errors.New("invalid socket address")
	}
	return addr.Unmap(), nil
}

func parseForwardedIP(value string) (netip.Addr, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return netip.Addr{}, errors.New("invalid forwarded address")
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, errors.New("invalid forwarded address")
	}
	return addr.Unmap(), nil
}

func (s *Server) trustedProxyIP(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxy {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Server) clientIP(r *http.Request) (netip.Addr, error) {
	socketIP, err := parseSocketIP(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	if s.options.ClientIPHeader == "" || !s.trustedProxyIP(socketIP) {
		return socketIP, nil
	}
	values := r.Header.Values(s.options.ClientIPHeader)
	if len(values) != 1 || len(values[0]) > maxForwardedBytes {
		return netip.Addr{}, errors.New("missing, duplicate, or oversized client IP header")
	}
	if s.options.ClientIPHeader == "Fly-Client-IP" {
		if strings.Contains(values[0], ",") {
			return netip.Addr{}, errors.New("invalid Fly-Client-IP header")
		}
		return parseForwardedIP(values[0])
	}

	parts := strings.Split(values[0], ",")
	if len(parts) == 0 || len(parts) > maxForwardedHops {
		return netip.Addr{}, errors.New("invalid X-Forwarded-For chain")
	}
	hops := make([]netip.Addr, len(parts))
	for i, part := range parts {
		hops[i], err = parseForwardedIP(strings.TrimSpace(part))
		if err != nil {
			return netip.Addr{}, err
		}
	}
	client := socketIP
	for i := len(hops) - 1; i >= 0; i-- {
		if !s.trustedProxyIP(client) {
			break
		}
		client = hops[i]
	}
	return client, nil
}
