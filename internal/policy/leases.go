package policy

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"cmesh/internal/mesh/schema"
)

type LeaseValidation struct {
	LeaseID        string
	PeerName       string
	PeerNodeID     string
	PeerStableID   string
	Profile        string
	CWD            string
	Capability     string
	Capabilities   []string
	CWDScope       string
	EffectiveCWD   string
	EffectivePeer  string
	EffectiveLease *schema.ProfileLease
}

type LeaseValidator interface {
	ValidateProfileLease(req LeaseValidation) (LeaseValidation, error)
}

type ProfileLeaseStore struct {
	path string
	mu   sync.Mutex
}

func NewProfileLeaseStore(dataDir string) *ProfileLeaseStore {
	return &ProfileLeaseStore{path: filepath.Join(dataDir, "profile-leases.json")}
}

func (s *ProfileLeaseStore) Issue(req LeaseValidation, ttl time.Duration, now time.Time) (schema.ProfileLease, error) {
	if s == nil {
		return schema.ProfileLease{}, errors.New("profile lease store is not configured")
	}
	if ttl <= 0 {
		return schema.ProfileLease{}, errors.New("ttl must be positive")
	}
	req.Profile = strings.TrimSpace(req.Profile)
	if req.Profile == "" {
		return schema.ProfileLease{}, errors.New("profile is required")
	}
	leaseID, err := newLeaseID(now)
	if err != nil {
		return schema.ProfileLease{}, err
	}
	lease := schema.ProfileLease{
		LeaseID:        leaseID,
		OriginNodeID:   strings.TrimSpace(req.PeerNodeID),
		OriginStableID: strings.TrimSpace(req.PeerStableID),
		OriginName:     strings.TrimSpace(req.PeerName),
		Profile:        req.Profile,
		CWD:            normalizeLeasePath(req.CWD),
		CWDScope:       normalizeCWDScope(req.CWDScope),
		Capabilities:   append([]string(nil), req.Capabilities...),
		CreatedAt:      now.UTC(),
		ExpiresAt:      now.UTC().Add(ttl),
	}
	if lease.OriginNodeID == "" && lease.OriginStableID == "" && lease.OriginName == "" {
		return schema.ProfileLease{}, errors.New("origin identity is required")
	}
	if err := s.update(func(file profileLeaseFile) (profileLeaseFile, error) {
		file.Leases = append(pruneLeases(file.Leases, now), lease)
		return file, nil
	}); err != nil {
		return schema.ProfileLease{}, err
	}
	return lease, nil
}

// leaseRetention is how long an expired or revoked lease is kept after it stops
// being usable. Validation already rejects both, so retention exists only to
// keep the record readable for audit and for List(includeExpired). Without it
// the file is append-only and grows without bound.
const leaseRetention = 30 * 24 * time.Hour

// pruneLeases drops leases that stopped being usable longer ago than
// leaseRetention. A lease stops being usable at its first retirement event --
// the earlier of expiry and revocation -- so retention counts from that point.
// Counting from the later one would keep a long-lived lease that was revoked
// early around until long after its original expiry. A lease still within its
// retention window is kept so it continues to appear in listings;
// ValidateProfileLease refuses it either way, so pruning can never turn a
// denial into an acceptance.
func pruneLeases(leases []schema.ProfileLease, now time.Time) []schema.ProfileLease {
	cutoff := now.UTC().Add(-leaseRetention)
	out := leases[:0]
	for _, lease := range leases {
		retired := leaseRetiredAt(lease)
		if !retired.IsZero() && retired.Before(cutoff) {
			continue
		}
		out = append(out, lease)
	}
	return out
}

// leaseRetiredAt returns the earliest non-zero of a lease's expiry and
// revocation times, or the zero time when it has neither.
func leaseRetiredAt(lease schema.ProfileLease) time.Time {
	retired := lease.ExpiresAt
	if !lease.RevokedAt.IsZero() && (retired.IsZero() || lease.RevokedAt.Before(retired)) {
		retired = lease.RevokedAt
	}
	return retired
}

func (s *ProfileLeaseStore) ValidateProfileLease(req LeaseValidation) (LeaseValidation, error) {
	if s == nil {
		return req, errors.New("profile lease store is not configured")
	}
	leaseID := strings.TrimSpace(req.LeaseID)
	if leaseID == "" {
		return req, errors.New("active worker-issued lease is required")
	}
	file, err := s.read()
	if err != nil {
		return req, err
	}
	now := time.Now().UTC()
	for _, lease := range file.Leases {
		if lease.LeaseID != leaseID {
			continue
		}
		req.EffectiveLease = &lease
		if !lease.RevokedAt.IsZero() {
			return req, fmt.Errorf("lease %q has been revoked", leaseID)
		}
		if !lease.ExpiresAt.IsZero() && !now.Before(lease.ExpiresAt) {
			return req, fmt.Errorf("lease %q has expired", leaseID)
		}
		if strings.TrimSpace(lease.Profile) != strings.TrimSpace(req.Profile) {
			return req, fmt.Errorf("lease %q is for profile %q, not %q", leaseID, lease.Profile, req.Profile)
		}
		if !leaseMatchesPeer(lease, req) {
			return req, fmt.Errorf("lease %q was not issued to this origin peer", leaseID)
		}
		if strings.TrimSpace(lease.CWD) != "" && !profileContextCWDAllowed(normalizeLeasePath(req.CWD), normalizeLeasePath(lease.CWD), lease.CWDScope) {
			return req, fmt.Errorf("lease %q is scoped to cwd %q", leaseID, lease.CWD)
		}
		if strings.TrimSpace(req.Capability) != "" && len(lease.Capabilities) > 0 && !contains(lease.Capabilities, req.Capability) {
			return req, fmt.Errorf("lease %q does not grant capability %q", leaseID, req.Capability)
		}
		req.EffectiveCWD = strings.TrimSpace(lease.CWD)
		req.EffectivePeer = firstNonEmptyLease(lease.OriginNodeID, lease.OriginStableID, lease.OriginName)
		return req, nil
	}
	return req, fmt.Errorf("lease %q was not issued by this worker", leaseID)
}

func (s *ProfileLeaseStore) RevokeAnyForLocalOperator(leaseID string, now time.Time) (schema.ProfileLease, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return schema.ProfileLease{}, errors.New("lease id is required")
	}
	var revoked schema.ProfileLease
	err := s.update(func(file profileLeaseFile) (profileLeaseFile, error) {
		for i := range file.Leases {
			if file.Leases[i].LeaseID == leaseID {
				file.Leases[i].RevokedAt = now.UTC()
				revoked = file.Leases[i]
				return file, nil
			}
		}
		return file, fmt.Errorf("lease %q not found", leaseID)
	})
	return revoked, err
}

func (s *ProfileLeaseStore) RevokeForPeer(identity LeaseValidation, leaseID string, now time.Time) (schema.ProfileLease, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return schema.ProfileLease{}, errors.New("lease id is required")
	}
	var revoked schema.ProfileLease
	err := s.update(func(file profileLeaseFile) (profileLeaseFile, error) {
		for i := range file.Leases {
			if file.Leases[i].LeaseID != leaseID {
				continue
			}
			if !leaseMatchesPeer(file.Leases[i], identity) {
				return file, fmt.Errorf("lease %q was not issued to this origin peer", leaseID)
			}
			file.Leases[i].RevokedAt = now.UTC()
			revoked = file.Leases[i]
			return file, nil
		}
		return file, fmt.Errorf("lease %q not found", leaseID)
	})
	return revoked, err
}

func (s *ProfileLeaseStore) Revoke(leaseID string, now time.Time) (schema.ProfileLease, error) {
	return s.RevokeAnyForLocalOperator(leaseID, now)
}

func (s *ProfileLeaseStore) ListAllForLocalOperator(includeExpired bool, now time.Time) ([]schema.ProfileLease, error) {
	file, err := s.read()
	if err != nil {
		return nil, err
	}
	var out []schema.ProfileLease
	for _, lease := range file.Leases {
		if !includeExpired && (!lease.RevokedAt.IsZero() || (!lease.ExpiresAt.IsZero() && !now.UTC().Before(lease.ExpiresAt))) {
			continue
		}
		out = append(out, lease)
	}
	return out, nil
}

func (s *ProfileLeaseStore) ListForPeer(identity LeaseValidation, includeExpired bool, now time.Time) ([]schema.ProfileLease, error) {
	leases, err := s.ListAllForLocalOperator(includeExpired, now)
	if err != nil {
		return nil, err
	}
	var out []schema.ProfileLease
	for _, lease := range leases {
		if leaseMatchesPeer(lease, identity) {
			out = append(out, lease)
		}
	}
	return out, nil
}

func (s *ProfileLeaseStore) List(includeExpired bool, now time.Time) ([]schema.ProfileLease, error) {
	return s.ListAllForLocalOperator(includeExpired, now)
}

type profileLeaseFile struct {
	Leases []schema.ProfileLease `json:"leases"`
}

func (s *ProfileLeaseStore) read() (profileLeaseFile, error) {
	if s == nil {
		return profileLeaseFile{}, errors.New("profile lease store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *ProfileLeaseStore) readLocked() (profileLeaseFile, error) {
	body, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return profileLeaseFile{}, nil
	}
	if err != nil {
		return profileLeaseFile{}, err
	}
	var file profileLeaseFile
	if err := json.Unmarshal(body, &file); err != nil {
		return profileLeaseFile{}, err
	}
	return file, nil
}

func (s *ProfileLeaseStore) update(fn func(profileLeaseFile) (profileLeaseFile, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.readLocked()
	if err != nil {
		return err
	}
	next, err := fn(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(body, '\n'), 0o600)
}

func leaseMatchesPeer(lease schema.ProfileLease, req LeaseValidation) bool {
	for _, pair := range [][2]string{
		{lease.OriginNodeID, req.PeerNodeID},
		{lease.OriginStableID, req.PeerStableID},
		{lease.OriginName, req.PeerName},
	} {
		if strings.TrimSpace(pair[0]) != "" && strings.TrimSpace(pair[0]) == strings.TrimSpace(pair[1]) {
			return true
		}
	}
	return false
}

func newLeaseID(now time.Time) (string, error) {
	id, err := randomToken()
	if err != nil {
		return "", err
	}
	return "lease_" + now.UTC().Format("20060102T150405Z") + "_" + id, nil
}

func randomToken() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func firstNonEmptyLease(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func profileContextCWDAllowed(requested, leased, scope string) bool {
	requested = strings.TrimSpace(requested)
	leased = strings.TrimSpace(leased)
	if leased == "" || requested == "" {
		return true
	}
	requested = filepath.Clean(requested)
	leased = filepath.Clean(leased)
	if strings.EqualFold(normalizeCWDScope(scope), "recursive") {
		return samePath(requested, leased) || strings.HasPrefix(pathForCompare(requested), pathForCompare(leased)+string(filepath.Separator))
	}
	return samePath(requested, leased)
}

func normalizeLeasePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs)
}

func normalizeCWDScope(scope string) string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case "recursive":
		return "recursive"
	default:
		return "exact"
	}
}

func samePath(a, b string) bool {
	return pathForCompare(a) == pathForCompare(b)
}

func pathForCompare(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(path)
	}
	return path
}
