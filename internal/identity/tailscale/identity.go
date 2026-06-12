package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
)

type Identity struct {
	NodeID       string   `json:"node_id,omitempty"`
	StableID     string   `json:"stable_id,omitempty"`
	NodeName     string   `json:"node_name,omitempty"`
	DNSName      string   `json:"dns_name,omitempty"`
	UserLogin    string   `json:"user_login,omitempty"`
	UserName     string   `json:"user_name,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	TailscaleIPs []string `json:"tailscale_ips,omitempty"`
	Tagged       bool     `json:"tagged"`
	Source       string   `json:"source"`
}

type Status struct {
	BackendState string   `json:"backend_state"`
	Version      string   `json:"version,omitempty"`
	NodeName     string   `json:"node_name,omitempty"`
	DNSName      string   `json:"dns_name,omitempty"`
	TailscaleIPs []string `json:"tailscale_ips,omitempty"`
	Health       []string `json:"health,omitempty"`
	Source       string   `json:"source"`
}

type Provider interface {
	Status(ctx context.Context) (Status, error)
	WhoIs(ctx context.Context, remoteAddr string) (Identity, error)
	Self(ctx context.Context) (Identity, error)
}

type Resolver struct {
	Primary  Provider
	Fallback Provider
}

func NewResolver() *Resolver {
	return &Resolver{
		Primary:  &LocalProvider{Client: &local.Client{}},
		Fallback: CommandProvider{},
	}
}

func (r *Resolver) Status(ctx context.Context) (Status, error) {
	if r == nil {
		r = NewResolver()
	}
	st, err := r.Primary.Status(ctx)
	if err == nil {
		return st, nil
	}
	if r.Fallback == nil {
		return Status{}, err
	}
	fallback, fallbackErr := r.Fallback.Status(ctx)
	if fallbackErr != nil {
		return Status{}, errors.Join(err, fallbackErr)
	}
	return fallback, nil
}

func (r *Resolver) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	if r == nil {
		r = NewResolver()
	}
	id, err := r.Primary.WhoIs(ctx, remoteAddr)
	if err == nil {
		return id, nil
	}
	if r.Fallback == nil {
		return Identity{}, err
	}
	fallback, fallbackErr := r.Fallback.WhoIs(ctx, remoteAddr)
	if fallbackErr != nil {
		return Identity{}, errors.Join(err, fallbackErr)
	}
	return fallback, nil
}

func (r *Resolver) Self(ctx context.Context) (Identity, error) {
	if r == nil {
		r = NewResolver()
	}
	id, err := r.Primary.Self(ctx)
	if err == nil {
		return id, nil
	}
	if r.Fallback == nil {
		return Identity{}, err
	}
	fallback, fallbackErr := r.Fallback.Self(ctx)
	if fallbackErr != nil {
		return Identity{}, errors.Join(err, fallbackErr)
	}
	return fallback, nil
}

type LocalProvider struct {
	Client *local.Client
}

func (p *LocalProvider) client() *local.Client {
	if p != nil && p.Client != nil {
		return p.Client
	}
	return &local.Client{}
}

func (p *LocalProvider) Status(ctx context.Context) (Status, error) {
	st, err := p.client().Status(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("tailscale localapi status: %w", err)
	}
	return statusFromLocal(st), nil
}

func (p *LocalProvider) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	who, err := p.client().WhoIs(ctx, remoteAddr)
	if err != nil {
		return Identity{}, fmt.Errorf("tailscale localapi whois %s: %w", remoteAddr, err)
	}
	return identityFromWhoIs(who, "localapi"), nil
}

func (p *LocalProvider) Self(ctx context.Context) (Identity, error) {
	st, err := p.client().Status(ctx)
	if err != nil {
		return Identity{}, fmt.Errorf("tailscale localapi status: %w", err)
	}
	if st == nil || len(st.TailscaleIPs) == 0 {
		return Identity{}, errors.New("tailscale localapi status returned no tailnet IP")
	}
	return p.WhoIs(ctx, st.TailscaleIPs[0].String())
}

type CommandProvider struct {
	Command string
}

func (p CommandProvider) command() string {
	if p.Command != "" {
		return p.Command
	}
	return "tailscale"
}

func (p CommandProvider) Status(ctx context.Context) (Status, error) {
	out, err := p.output(ctx, "status", "--json")
	if err != nil {
		return Status{}, err
	}
	var raw commandStatus
	if err := json.Unmarshal(out, &raw); err != nil {
		return Status{}, fmt.Errorf("parse tailscale status --json: %w", err)
	}
	return raw.toStatus(), nil
}

func (p CommandProvider) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	out, err := p.output(ctx, "whois", "--json", remoteAddr)
	if err != nil {
		return Identity{}, err
	}
	var raw commandWhoIs
	if err := json.Unmarshal(out, &raw); err != nil {
		return Identity{}, fmt.Errorf("parse tailscale whois --json: %w", err)
	}
	return raw.toIdentity(), nil
}

func (p CommandProvider) Self(ctx context.Context) (Identity, error) {
	st, err := p.Status(ctx)
	if err != nil {
		return Identity{}, err
	}
	if len(st.TailscaleIPs) == 0 {
		return Identity{}, errors.New("tailscale status returned no tailnet IP")
	}
	return p.WhoIs(ctx, st.TailscaleIPs[0])
}

func (p CommandProvider) output(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath(p.command())
	if err != nil {
		return nil, fmt.Errorf("tailscale command unavailable: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tailscale %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return out, nil
}

func IsTailnetIP(addr string) bool {
	host := addr
	if strings.Contains(addr, ":") {
		if parsed, err := netip.ParseAddrPort(addr); err == nil {
			host = parsed.Addr().String()
		}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if ip.Is4() {
		octets := ip.As4()
		return octets[0] == 100 && octets[1] >= 64 && octets[1] <= 127
	}
	return strings.HasPrefix(strings.ToLower(ip.String()), "fd7a:115c:a1e0:")
}

func statusFromLocal(st *ipnstate.Status) Status {
	out := Status{Source: "localapi"}
	if st == nil {
		return out
	}
	out.BackendState = st.BackendState
	out.Version = st.Version
	out.Health = append([]string(nil), st.Health...)
	for _, ip := range st.TailscaleIPs {
		out.TailscaleIPs = append(out.TailscaleIPs, ip.String())
	}
	if st.Self != nil {
		out.NodeName = st.Self.HostName
		out.DNSName = st.Self.DNSName
	}
	return out
}

func identityFromWhoIs(who *apitype.WhoIsResponse, source string) Identity {
	id := Identity{Source: source}
	if who == nil {
		return id
	}
	if who.Node != nil {
		id.NodeID = who.Node.ID.String()
		id.StableID = string(who.Node.StableID)
		id.DNSName = strings.TrimSuffix(who.Node.Name, ".")
		id.NodeName = firstLabel(id.DNSName)
		id.Tags = append([]string(nil), who.Node.Tags...)
		id.Tagged = who.Node.IsTagged()
		for _, pfx := range who.Node.Addresses {
			id.TailscaleIPs = append(id.TailscaleIPs, pfx.Addr().String())
		}
	}
	if who.UserProfile != nil {
		id.UserLogin = who.UserProfile.LoginName
		id.UserName = who.UserProfile.DisplayName
	}
	return id
}

func firstLabel(name string) string {
	name = strings.TrimSuffix(name, ".")
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

type commandStatus struct {
	BackendState string          `json:"BackendState"`
	Version      string          `json:"Version"`
	TailscaleIPs []string        `json:"TailscaleIPs"`
	Health       []string        `json:"Health"`
	Self         commandPeerNode `json:"Self"`
}

func (s commandStatus) toStatus() Status {
	return Status{
		BackendState: s.BackendState,
		Version:      s.Version,
		NodeName:     firstNonEmpty(s.Self.HostName, firstLabel(s.Self.DNSName)),
		DNSName:      strings.TrimSuffix(s.Self.DNSName, "."),
		TailscaleIPs: append([]string(nil), s.TailscaleIPs...),
		Health:       append([]string(nil), s.Health...),
		Source:       "command",
	}
}

type commandWhoIs struct {
	Node        commandPeerNode    `json:"Node"`
	UserProfile commandUserProfile `json:"UserProfile"`
}

func (w commandWhoIs) toIdentity() Identity {
	dns := strings.TrimSuffix(firstNonEmpty(w.Node.Name, w.Node.DNSName), ".")
	tags := append([]string(nil), w.Node.Tags...)
	return Identity{
		NodeID:       firstNonEmpty(w.Node.ID, w.Node.NodeID),
		StableID:     w.Node.StableID,
		NodeName:     firstNonEmpty(w.Node.HostName, firstLabel(dns)),
		DNSName:      dns,
		UserLogin:    w.UserProfile.LoginName,
		UserName:     w.UserProfile.DisplayName,
		Tags:         tags,
		TailscaleIPs: append([]string(nil), w.Node.TailscaleIPs...),
		Tagged:       len(tags) > 0,
		Source:       "command",
	}
}

type commandPeerNode struct {
	ID           string   `json:"ID"`
	NodeID       string   `json:"NodeID"`
	StableID     string   `json:"StableID"`
	Name         string   `json:"Name"`
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	Tags         []string `json:"Tags"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

type commandUserProfile struct {
	LoginName   string `json:"LoginName"`
	DisplayName string `json:"DisplayName"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
