package ldapauth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"

	"github.com/go-ldap/ldap/v3"
)

func attr(name string, raw []byte) *ldap.EntryAttribute {
	return &ldap.EntryAttribute{Name: name, Values: []string{string(raw)}, ByteValues: [][]byte{raw}}
}

func TestSubject(t *testing.T) {
	guid := []byte{0x46, 0x9f, 0xa5, 0xc1, 0x34, 0x12, 0x78, 0x56, 0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78}
	ad := &ldap.Entry{DN: "CN=Ann,OU=Staff,DC=corp", Attributes: []*ldap.EntryAttribute{attr("objectGUID", guid)}}
	openldap := &ldap.Entry{DN: "uid=ann,ou=people,dc=corp", Attributes: []*ldap.EntryAttribute{
		attr("entryUUID", []byte("5A1B6C2E-0000-1000-8000-00AABBCCDDEE")), attr("uid", []byte("Ann")),
	}}
	bare := &ldap.Entry{DN: "uid=ann,ou=people,dc=corp"}
	tests := []struct {
		name      string
		attr      string
		entry     *ldap.Entry
		want      string
		wantField string
		wantErr   bool
	}{
		{name: "objectGUID", entry: ad, want: "c1a59f46-1234-5678-9abc-def012345678", wantField: "objectGUID"},
		{name: "entryUUID", entry: openldap, want: "5a1b6c2e-0000-1000-8000-00aabbccddee", wantField: "entryUUID"},
		{name: "DN fallback", entry: bare, want: "uid=ann,ou=people,dc=corp", wantField: subjectDN},
		{name: "configured attribute keeps case", attr: "UID", entry: openldap, want: "Ann", wantField: "UID"},
		{name: "configured attribute missing", attr: "employeeNumber", entry: openldap, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, field, err := Subject(tt.attr, tt.entry)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want || field != tt.wantField {
				t.Errorf("got %q from %q, want %q from %q", got, field, tt.want, tt.wantField)
			}
		})
	}
}

func TestFormatIDBinary(t *testing.T) {
	if got := formatID("msDS-ConsistencyGuid", []byte{0xff, 0x00, 0x10}); got != "ff0010" {
		t.Errorf("binary id = %q, want hex", got)
	}
	if got := formatID("objectSid", []byte("AbC")); got != "416243" {
		t.Errorf("objectSid = %q, want hex", got)
	}
}

func TestSearchAttributes(t *testing.T) {
	p := &Provider{EmailAttribute: "mail", UsernameAttribute: "uid", GroupMembershipAttribute: "memberOf"}
	if got := searchAttributes(p); len(got) != 5 || got[3] != "objectGUID" || got[4] != "entryUUID" {
		t.Errorf("default attributes = %v", got)
	}
	p.SubjectAttribute = "nsUniqueId"
	if got := searchAttributes(p); len(got) != 4 || got[3] != "nsUniqueId" {
		t.Errorf("configured attributes = %v", got)
	}
}

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewService(database, auth.NewService(database, []byte("0123456789abcdef0123456789abcdef")), nil), database
}

func TestProviderChangeUnlinksAccounts(t *testing.T) {
	in := ProviderInput{Slug: "corp", DisplayName: "Corp", Host: "dc1.corp.example", UserSearchBase: "dc=corp", Enabled: true}
	tests := []struct {
		name       string
		change     func(in ProviderInput) ProviderInput
		wantLinked bool
	}{
		{name: "search base", wantLinked: true, change: func(in ProviderInput) ProviderInput { in.UserSearchBase = "ou=staff,dc=corp"; return in }},
		{name: "host case", wantLinked: true, change: func(in ProviderInput) ProviderInput { in.Host = "DC1.corp.example"; return in }},
		{name: "other port", change: func(in ProviderInput) ProviderInput { in.Port = 3269; return in }},
		{name: "other host", change: func(in ProviderInput) ProviderInput { in.Host = "ldap.other.example"; return in }},
		{name: "subject attribute", change: func(in ProviderInput) ProviderInput { in.SubjectAttribute = "uid"; return in }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, database := newTestService(t)
			p, err := s.CreateProvider(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO users (id, username, password, ldap_provider, ldap_subject) VALUES ('u1', 'ann', 'x', 'corp', 'g-1')`); err != nil {
				t.Fatal(err)
			}
			_, n, err := s.UpdateProvider(ctx, p.ID, tt.change(in))
			if err != nil {
				t.Fatal(err)
			}
			var linked bool
			if err := database.QueryRow(`SELECT ldap_provider IS NOT NULL FROM users WHERE id = 'u1'`).Scan(&linked); err != nil {
				t.Fatal(err)
			}
			if linked != tt.wantLinked || (n == 0) != tt.wantLinked {
				t.Errorf("linked = %v (unlinked %d), want linked %v", linked, n, tt.wantLinked)
			}
		})
	}

	t.Run("delete", func(t *testing.T) {
		ctx := context.Background()
		s, database := newTestService(t)
		p, err := s.CreateProvider(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO users (id, username, password, ldap_provider, ldap_legacy_subject, ldap_rekey_until)
			VALUES ('u1', 'ann', 'x', 'corp', 'uid=ann,dc=corp', datetime('now', '+30 days'))`); err != nil {
			t.Fatal(err)
		}
		if n, err := s.DeleteProvider(ctx, p.ID); err != nil || n != 1 {
			t.Fatalf("delete unlinked %d, %v; want 1", n, err)
		}
		var left int
		if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE ldap_provider IS NOT NULL OR ldap_legacy_subject IS NOT NULL`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Error("account still linked after delete")
		}
	})
}
