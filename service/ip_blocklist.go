package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/darron/fbhook/storage"
)

const ipBlocklistKey = "config/ip-blocklist.json"
const ipBlocklistTTL = 5 * time.Second
const maxIPBlocklistBytes = 64 * 1024

type IPRule struct {
	CIDR string `json:"cidr"`
	Note string `json:"note,omitempty"`
}

type IPBlocklist struct {
	Schema    int      `json:"schema"`
	Rules     []IPRule `json:"rules"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
}

func emptyIPBlocklist() IPBlocklist {
	return IPBlocklist{Schema: 1, Rules: []IPRule{}}
}

func cloneIPBlocklist(config IPBlocklist) IPBlocklist {
	config.Rules = append([]IPRule(nil), config.Rules...)
	if config.Rules == nil {
		config.Rules = []IPRule{}
	}
	return config
}

func decodeIPBlocklist(body []byte) (IPBlocklist, []netip.Prefix, error) {
	if len(body) > maxIPBlocklistBytes || !utf8.Valid(body) {
		return IPBlocklist{}, nil, errors.New("stored IP blocklist is invalid")
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		return IPBlocklist{}, nil, errors.New("stored IP blocklist is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config IPBlocklist
	if err := decoder.Decode(&config); err != nil {
		return IPBlocklist{}, nil, errors.New("stored IP blocklist is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return IPBlocklist{}, nil, errors.New("stored IP blocklist is invalid")
	}
	if config.Schema != 1 || config.Rules == nil {
		return IPBlocklist{}, nil, errors.New("unsupported or invalid stored IP blocklist")
	}
	if config.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, config.UpdatedAt); err != nil {
			return IPBlocklist{}, nil, errors.New("stored IP blocklist has invalid audit metadata")
		}
	}
	if !utf8.ValidString(config.UpdatedBy) || len(config.UpdatedBy) > 200 {
		return IPBlocklist{}, nil, errors.New("stored IP blocklist has invalid audit metadata")
	}
	rules, prefixes, err := normalizeIPRules(config.Rules)
	if err != nil {
		return IPBlocklist{}, nil, errors.New("stored IP blocklist contains invalid rules")
	}
	config.Rules = rules
	return config, prefixes, nil
}

func rejectDuplicateJSONFields(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := scanUniqueJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func scanUniqueJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := scanUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func normalizeIPRules(rules []IPRule) ([]IPRule, []netip.Prefix, error) {
	if rules == nil {
		rules = []IPRule{}
	}
	if len(rules) > 1024 {
		return nil, nil, problem(400, "IP blocklist exceeds 1024 rules")
	}
	normalized := make([]IPRule, 0, len(rules))
	prefixes := make([]netip.Prefix, 0, len(rules))
	seen := make(map[netip.Prefix]struct{}, len(rules))
	for _, rule := range rules {
		if !utf8.ValidString(rule.CIDR) || !utf8.ValidString(rule.Note) || utf8.RuneCountInString(rule.Note) > 200 {
			return nil, nil, problem(400, "invalid IP blocklist rule")
		}
		if rule.CIDR == "" || strings.TrimSpace(rule.CIDR) != rule.CIDR {
			return nil, nil, problem(400, "invalid IP blocklist CIDR")
		}
		prefix, err := parseIPPrefix(rule.CIDR)
		if err != nil {
			return nil, nil, problem(400, "invalid IP blocklist CIDR")
		}
		if _, duplicate := seen[prefix]; duplicate {
			return nil, nil, problem(400, "duplicate IP blocklist CIDR")
		}
		seen[prefix] = struct{}{}
		normalized = append(normalized, IPRule{CIDR: prefix.String(), Note: rule.Note})
		prefixes = append(prefixes, prefix)
	}
	return normalized, prefixes, nil
}

func parseIPPrefix(value string) (netip.Prefix, error) {
	if addr, err := netip.ParseAddr(value); err == nil {
		if addr.Zone() != "" {
			return netip.Prefix{}, errors.New("zoned address")
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.IsValid() || prefix.Addr().Zone() != "" || prefix != prefix.Masked() {
		return netip.Prefix{}, errors.New("invalid prefix")
	}
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < 96 {
			return netip.Prefix{}, errors.New("mapped IPv4 prefix includes non-IPv4 addresses")
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	return prefix, nil
}

func (s *Service) GetIPBlocklist(ctx context.Context) (IPBlocklist, string, error) {
	object, err := s.store.Get(ctx, ipBlocklistKey)
	if errors.Is(err, storage.ErrNotFound) {
		return emptyIPBlocklist(), "", nil
	}
	if err != nil {
		return IPBlocklist{}, "", err
	}
	config, _, err := decodeIPBlocklist(object.Body)
	if err != nil {
		return IPBlocklist{}, "", err
	}
	return config, object.Version, nil
}

func (s *Service) SaveIPBlocklist(ctx context.Context, rules []IPRule, version, actor string) (IPBlocklist, string, error) {
	if !utf8.ValidString(actor) || strings.TrimSpace(actor) == "" || len(actor) > 200 {
		return IPBlocklist{}, "", problem(400, "invalid IP blocklist actor")
	}
	normalized, prefixes, err := normalizeIPRules(rules)
	if err != nil {
		return IPBlocklist{}, "", err
	}
	config := IPBlocklist{
		Schema:    1,
		Rules:     normalized,
		UpdatedAt: s.now().Format(time.RFC3339Nano),
		UpdatedBy: actor,
	}
	body, err := json.Marshal(config)
	if err != nil || len(body) > maxIPBlocklistBytes {
		return IPBlocklist{}, "", problem(400, "encoded IP blocklist exceeds 64 KiB")
	}
	s.ipMu.Lock()
	defer s.ipMu.Unlock()
	next, err := writeJSON(ctx, s.store, ipBlocklistKey, config, version)
	if err != nil {
		return IPBlocklist{}, "", err
	}
	s.ipRules = append([]netip.Prefix(nil), prefixes...)
	s.ipLoaded = true
	s.ipCheck = s.now()
	s.ipErr = nil
	return cloneIPBlocklist(config), next, nil
}

func (s *Service) IPBlocked(ctx context.Context, addr netip.Addr) (bool, error) {
	if !addr.IsValid() || addr.Zone() != "" {
		return false, problem(503, "client address unavailable")
	}
	addr = addr.Unmap()
	s.ipMu.Lock()
	defer s.ipMu.Unlock()
	now := s.now()
	if s.ipLoaded && now.Sub(s.ipCheck) >= 0 && now.Sub(s.ipCheck) < ipBlocklistTTL {
		if s.ipErr != nil {
			return false, s.ipErr
		}
		return prefixContains(s.ipRules, addr), nil
	}

	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	object, err := s.store.Get(refreshCtx, ipBlocklistKey)
	var prefixes []netip.Prefix
	if errors.Is(err, storage.ErrNotFound) {
		err = nil
	} else if err == nil {
		_, prefixes, err = decodeIPBlocklist(object.Body)
	}
	s.ipLoaded = true
	s.ipCheck = now
	s.ipErr = err
	if err != nil {
		return false, err
	}
	s.ipRules = append([]netip.Prefix(nil), prefixes...)
	return prefixContains(s.ipRules, addr), nil
}

func prefixContains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
