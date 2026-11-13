package auth

import (
	"context"
	"testing"
)

type memSettings map[string]string

func (m memSettings) Get(key, def string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func (m memSettings) Set(_ context.Context, key, value string) error {
	m[key] = value
	return nil
}

func TestPasswordFloor(t *testing.T) {
	tests := []struct {
		name     string
		policy   PolicyConfig
		password string
		wantErr  bool
	}{
		{name: "11 chars under an old 8 policy", policy: PolicyConfig{MinLength: 8}, password: "abcdefghijk", wantErr: true},
		{name: "12 chars", policy: PolicyConfig{MinLength: 8}, password: "abcdefghijkl"},
		{name: "policy above the floor", policy: PolicyConfig{MinLength: 16}, password: "abcdefghijklmno", wantErr: true},
		{name: "zero policy", policy: PolicyConfig{}, password: "short", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePassword(tt.policy, tt.password); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPolicyMinLengthNeverBelowFloor(t *testing.T) {
	tests := []struct {
		name   string
		stored map[string]string
		want   int
	}{
		{name: "default", want: MinPasswordLength},
		{name: "stored 8 from an older version", stored: map[string]string{PolicyMinLengthKey: "8"}, want: MinPasswordLength},
		{name: "stored 20", stored: map[string]string{PolicyMinLengthKey: "20"}, want: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := memSettings{}
			for k, v := range tt.stored {
				s[k] = v
			}
			if got := LoadPolicy(s).MinLength; got != tt.want {
				t.Fatalf("MinLength = %d, want %d", got, tt.want)
			}
		})
	}
	for _, n := range []int{1, 8, 11} {
		if err := SavePolicy(context.Background(), memSettings{}, PolicyConfig{MinLength: n}); err == nil {
			t.Errorf("SavePolicy accepted min_length %d", n)
		}
	}
	if err := SavePolicy(context.Background(), memSettings{}, PolicyConfig{MinLength: 12}); err != nil {
		t.Errorf("SavePolicy(12): %v", err)
	}
}

// Existing accounts keep signing in with a password set under an older,
// shorter policy.
func TestShortExistingPasswordStillSignsIn(t *testing.T) {
	svc, db := newTestService(t)
	hash, err := HashPassword("old-pw8!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, password) VALUES ('u-old', 'olduser', ?)`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(context.Background(), "olduser", "old-pw8!", "test", "192.0.2.1"); err != nil {
		t.Fatalf("login with an existing 8-character password: %v", err)
	}
	if err := svc.ChangePassword(context.Background(), "u-old", "new-pw8!"); err == nil {
		t.Fatal("changed to an 8-character password")
	}
}
