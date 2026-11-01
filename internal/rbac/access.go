package rbac

import (
	"fmt"
	"slices"
)

// Scope types of an assignment.
const (
	ScopeGlobal  = "global"
	ScopeHost    = "host"
	ScopeHostTag = "host_tag"
	ScopeStack   = "stack"
)

// Scope is where an assignment applies. Host is the host id for host and
// stack scopes; Value is the tag or the stack name. Stack names are
// unique across the instance (one deployment host per stack). A stack
// scope with a Host matches the stack while it is deployed there; without
// a Host it matches the stack wherever it runs and while it is not
// deployed.
type Scope struct {
	Type  string `json:"type"`
	Host  string `json:"host,omitempty"`
	Value string `json:"value,omitempty"`
}

// Global is the scope that covers everything.
var Global = Scope{Type: ScopeGlobal}

func (s Scope) IsGlobal() bool { return s.Type == ScopeGlobal || s.Type == "" }

// Validate rejects malformed scopes.
func (s Scope) Validate() error {
	switch s.Type {
	case ScopeGlobal:
		if s.Host != "" || s.Value != "" {
			return fmt.Errorf("a global scope takes no host or value")
		}
	case ScopeHost:
		if s.Host == "" || s.Value != "" {
			return fmt.Errorf("a host scope needs a host and no value")
		}
	case ScopeHostTag:
		if s.Host != "" || s.Value == "" {
			return fmt.Errorf("a host tag scope needs a tag")
		}
	case ScopeStack:
		if s.Value == "" {
			return fmt.Errorf("a stack scope needs a stack name")
		}
	default:
		return fmt.Errorf("unknown scope type %q", s.Type)
	}
	return nil
}

// String is a short label used in audit entries and explanations.
func (s Scope) String() string {
	switch s.Type {
	case ScopeHost:
		return "host " + s.Host
	case ScopeHostTag:
		return "host tag " + s.Value
	case ScopeStack:
		if s.Host == "" {
			return "stack " + s.Value
		}
		return "stack " + s.Value + " on " + s.Host
	default:
		return "everything"
	}
}

// Resource is what a request acts on. Host-level resources (images,
// volumes, the host itself) leave Stack empty; a stack that is not
// deployed leaves HostID empty.
type Resource struct {
	HostID    string
	HostTags  []string
	StackName string
}

func (s Scope) matches(res Resource) bool {
	switch s.Type {
	case ScopeGlobal, "":
		return true
	case ScopeHost:
		return res.HostID != "" && res.HostID == s.Host
	case ScopeHostTag:
		return res.HostID != "" && slices.Contains(res.HostTags, s.Value)
	case ScopeStack:
		return res.StackName != "" && res.StackName == s.Value && (s.Host == "" || res.HostID == s.Host)
	}
	return false
}

// Assignment gives a user a role on a scope. Source is "manual" for
// assignments an admin made and "idp" for those an identity provider's
// group mapping produced at sign-in; Via names the group.
type Assignment struct {
	ID        int64  `json:"id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	Scope     Scope  `json:"scope"`
	Source    string `json:"source"`
	Via       string `json:"via,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

const (
	SourceManual = "manual"
	SourceIdP    = "idp"
)

// Grant is one evaluated assignment: the permissions it gives and the
// scopes that must all match. A grant without scopes is global. API-token
// restrictions append their own scope, so a token grant only matches
// where the owner's assignment and the restriction both match.
type Grant struct {
	Perms      map[Perm]bool
	Scopes     []Scope
	Assignment Assignment
}

func (g Grant) global() bool {
	for _, s := range g.Scopes {
		if !s.IsGlobal() {
			return false
		}
	}
	return true
}

func (g Grant) has(p Perm) bool {
	if !g.Perms[p] {
		return false
	}
	return !IsGlobalOnly(p) || g.global()
}

// Covers reports whether g reaches res, whatever it permits.
func (g Grant) Covers(res Resource) bool {
	for _, s := range g.Scopes {
		if !s.matches(res) {
			return false
		}
	}
	return true
}

// Access is everything a caller may do: the union of its grants plus the
// base rights. The zero value grants only the base rights.
type Access struct {
	Grants []Grant
	// Authenticated callers get the base rights.
	Authenticated bool
}

// Allows reports whether some grant holds p anywhere. Handlers acting on
// a concrete resource must also check AllowsOn.
func (a *Access) Allows(p Perm) bool {
	if a == nil {
		return false
	}
	if a.Authenticated && IsBase(p) {
		return true
	}
	for _, g := range a.Grants {
		if g.has(p) {
			return true
		}
	}
	return false
}

// AllowsGlobally reports whether a global grant holds p.
func (a *Access) AllowsGlobally(p Perm) bool {
	if a == nil {
		return false
	}
	if a.Authenticated && IsBase(p) {
		return true
	}
	for _, g := range a.Grants {
		if g.global() && g.has(p) {
			return true
		}
	}
	return false
}

// AllowsOn reports whether some grant holds p on res.
func (a *Access) AllowsOn(p Perm, res Resource) bool {
	_, ok := a.GrantFor(p, res)
	return ok
}

// GrantFor returns the first grant that holds p on res.
func (a *Access) GrantFor(p Perm, res Resource) (Grant, bool) {
	if a == nil {
		return Grant{}, false
	}
	if a.Authenticated && IsBase(p) {
		return Grant{}, true
	}
	for _, g := range a.Grants {
		if g.has(p) && g.Covers(res) {
			return g, true
		}
	}
	return Grant{}, false
}

// Covers reports whether some grant reaches res, whatever it permits.
func (a *Access) Covers(res Resource) bool {
	if a == nil {
		return false
	}
	for _, g := range a.Grants {
		if g.Covers(res) {
			return true
		}
	}
	return false
}

// AllowsNear reports whether some grant holds p on hostID itself or on a
// stack that may run there. It is a pre-filter for fan-out over hosts;
// the exact check on each item still has to follow.
func (a *Access) AllowsNear(p Perm, hostID string, hostTags []string) bool {
	if a == nil {
		return false
	}
	if a.Authenticated && IsBase(p) {
		return true
	}
	for _, g := range a.Grants {
		if g.has(p) && g.near(hostID, hostTags) {
			return true
		}
	}
	return false
}

// CoversNear is AllowsNear for any permission.
func (a *Access) CoversNear(hostID string, hostTags []string) bool {
	if a == nil {
		return false
	}
	for _, g := range a.Grants {
		if g.near(hostID, hostTags) {
			return true
		}
	}
	return false
}

func (g Grant) near(hostID string, hostTags []string) bool {
	for _, s := range g.Scopes {
		switch s.Type {
		case ScopeStack:
			if s.Host != "" && s.Host != hostID {
				return false
			}
		default:
			if !s.matches(Resource{HostID: hostID, HostTags: hostTags}) {
				return false
			}
		}
	}
	return true
}

// Unscoped reports whether every permission the caller holds is held
// globally, so list handlers can skip per-item filtering.
func (a *Access) Unscoped() bool {
	if a == nil || len(a.Grants) == 0 {
		return false
	}
	global := map[Perm]bool{}
	for _, g := range a.Grants {
		if g.global() {
			for p := range g.Perms {
				global[p] = true
			}
		}
	}
	for _, g := range a.Grants {
		if g.global() {
			continue
		}
		for p := range g.Perms {
			if !global[p] && !IsGlobalOnly(p) {
				return false
			}
		}
	}
	return true
}

// Perms returns every permission the caller holds anywhere.
func (a *Access) Perms() []Perm {
	set := map[Perm]bool{}
	if a == nil {
		return nil
	}
	if a.Authenticated {
		for p := range basePerms {
			set[p] = true
		}
	}
	for _, g := range a.Grants {
		for p := range g.Perms {
			if g.has(p) {
				set[p] = true
			}
		}
	}
	return SortedPerms(set)
}

// Restrict narrows a to the role perms on scope: each grant keeps only the
// permissions perms also has and must additionally match scope. It never
// widens a.
func (a *Access) Restrict(perms map[Perm]bool, scope Scope) *Access {
	out := &Access{Authenticated: a != nil && a.Authenticated}
	if a == nil {
		return out
	}
	for _, g := range a.Grants {
		ng := Grant{Perms: map[Perm]bool{}, Assignment: g.Assignment}
		for p := range g.Perms {
			if perms[p] {
				ng.Perms[p] = true
			}
		}
		if len(ng.Perms) == 0 {
			continue
		}
		ng.Scopes = append(slices.Clone(g.Scopes), scope)
		out.Grants = append(out.Grants, ng)
	}
	return out
}

// GroupRule maps an identity provider group to a role on a scope.
type GroupRule struct {
	Group string
	Role  string
	Scope Scope
}

// IdPAssignments returns what a sign-in through provider gets: one
// assignment per rule whose group the user is in, or defaultRole on
// everything when no rule matched. match compares a group the IdP sent
// with a rule's group; nil means exact match.
func IdPAssignments(provider string, groups []string, rules []GroupRule, defaultRole string, match func(have, want string) bool) []Assignment {
	if match == nil {
		match = func(have, want string) bool { return have == want }
	}
	var out []Assignment
	for _, r := range rules {
		if r.Group == "" || r.Role == "" {
			continue
		}
		for _, g := range groups {
			if match(g, r.Group) {
				sc := r.Scope
				if sc.Type == "" {
					sc = Global
				}
				out = append(out, Assignment{Role: r.Role, Scope: sc, Source: SourceIdP, Via: provider + " group " + r.Group})
				break
			}
		}
	}
	if len(out) == 0 && defaultRole != "" {
		out = append(out, Assignment{Role: defaultRole, Scope: Global, Source: SourceIdP, Via: provider + " default role"})
	}
	return out
}

// ScopeContains reports whether scope c reaches everything s reaches.
func ScopeContains(c, s Scope, tags func(hostID string) []string) bool {
	switch c.Type {
	case ScopeGlobal, "":
		return true
	case ScopeHost:
		return (s.Type == ScopeHost || s.Type == ScopeStack) && s.Host == c.Host
	case ScopeHostTag:
		switch s.Type {
		case ScopeHostTag:
			return s.Value == c.Value
		case ScopeHost, ScopeStack:
			return s.Host != "" && slices.Contains(tags(s.Host), c.Value)
		}
	case ScopeStack:
		return s.Type == ScopeStack && s.Value == c.Value && (c.Host == "" || c.Host == s.Host)
	}
	return false
}

// HoldsOn reports whether the caller holds p everywhere scope s reaches,
// so it may hand out p on s. tags returns the current tags of a host.
func (a *Access) HoldsOn(p Perm, s Scope, tags func(hostID string) []string) bool {
	if a == nil {
		return false
	}
	if tags == nil {
		tags = func(string) []string { return nil }
	}
	for _, g := range a.Grants {
		if !g.has(p) {
			continue
		}
		ok := true
		for _, c := range g.Scopes {
			ok = ok && ScopeContains(c, s, tags)
		}
		if ok {
			return true
		}
	}
	return false
}
