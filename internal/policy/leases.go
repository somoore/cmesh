package policy

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		CWD:            strings.TrimSpace(req.CWD),
		Capabilities:   append([]string(nil), req.Capabilities...),
		CreatedAt:      now.UTC(),
		ExpiresAt:      now.UTC().Add(ttl),
	}
	if lease.OriginNodeID == "" && lease.OriginStableID == "" && lease.OriginName == "" {
		return schema.ProfileLease{}, errors.New("origin identity is required")
	}
	if err := s.update(func(file profileLeaseFile) (profileLeaseFile, error) {
		file.Leases = append(file.Leases, lease)
		return file, nil
	}); err != nil {
		return schema.ProfileLease{}, err
	}
	return lease, nil
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
		if strings.TrimSpace(lease.CWD) != "" && !profileContextCWDAllowed(req.CWD, lease.CWD) {
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

func (s *ProfileLeaseStore) Revoke(leaseID string, now time.Time) (schema.ProfileLease, error) {
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

func (s *ProfileLeaseStore) List(includeExpired bool, now time.Time) ([]schema.ProfileLease, error) {
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

func profileContextCWDAllowed(requested, leased string) bool {
	requested = strings.TrimSpace(requested)
	leased = strings.TrimSpace(leased)
	if leased == "" || requested == "" {
		return true
	}
	return filepath.Clean(requested) == filepath.Clean(leased)
}
