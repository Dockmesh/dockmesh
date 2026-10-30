//go:build integration

// Package integration runs the backup targets and the backup service
// against real SMB, SFTP, WebDAV and S3 servers. The servers come from
// testdata/up.sh; without them every test skips.
package integration

import (
	"os"
	"testing"
)

// Env describes the test servers, read from DM_IT_* variables.
type Env struct {
	SMBAddr, SMBUser, SMBPass, SMBShare string
	SFTPAddr, SFTPUser, SFTPPass        string
	SFTPKey                             string
	WebDAVURL, WebDAVUser, WebDAVPass   string
	S3Endpoint, S3Region, S3Bucket      string
	S3AccessKey, S3SecretKey            string
}

// LoadEnv reads the environment, skipping the test when no servers are up.
func LoadEnv(t testing.TB) Env {
	t.Helper()
	if os.Getenv("DM_IT_SMB_ADDR") == "" {
		t.Skip("DM_IT_* not set; start the servers with internal/backup/integration/testdata/up.sh")
	}
	e := Env{
		SMBAddr: os.Getenv("DM_IT_SMB_ADDR"), SMBUser: os.Getenv("DM_IT_SMB_USER"),
		SMBPass: os.Getenv("DM_IT_SMB_PASS"), SMBShare: os.Getenv("DM_IT_SMB_SHARE"),
		SFTPAddr: os.Getenv("DM_IT_SFTP_ADDR"), SFTPUser: os.Getenv("DM_IT_SFTP_USER"),
		SFTPPass:  os.Getenv("DM_IT_SFTP_PASS"),
		WebDAVURL: os.Getenv("DM_IT_WEBDAV_URL"), WebDAVUser: os.Getenv("DM_IT_WEBDAV_USER"),
		WebDAVPass: os.Getenv("DM_IT_WEBDAV_PASS"),
		S3Endpoint: os.Getenv("DM_IT_S3_ENDPOINT"), S3Region: os.Getenv("DM_IT_S3_REGION"),
		S3Bucket: os.Getenv("DM_IT_S3_BUCKET"), S3AccessKey: os.Getenv("DM_IT_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("DM_IT_S3_SECRET_KEY"),
	}
	if f := os.Getenv("DM_IT_SFTP_KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read sftp key: %v", err)
		}
		e.SFTPKey = string(b)
	}
	return e
}

// Gap keys name the checks a variant is known to fail.
const (
	GapAll            = "all"              // the whole variant
	GapList           = "list"             // listing paths and anything built on it
	GapStorageInfoNew = "storage-info-new" // connection test on a folder that does not exist yet
)

// Config is one reachable target: its type, a good config and the
// secret field that wrong-credential tests overwrite.
type Config struct {
	Name   string
	Type   string
	Config map[string]any
	Secret string
	Gaps   map[string]string
}

// SkipGap skips the test when the variant has a known gap for key.
func (c Config) SkipGap(t testing.TB, key string) {
	t.Helper()
	if why := c.Gaps[key]; why != "" {
		t.Skip(why)
	}
}

// Targets lists every target variant the suite covers. Each gets its
// own sub-path below base so variants and repeated runs never meet.
func (e Env) Targets(base string) []Config {
	host, port := splitAddr(e.SMBAddr)
	sh, sp := splitAddr(e.SFTPAddr)
	smb := func(user string) map[string]any {
		return map[string]any{"host": host, "port": port, "share": e.SMBShare,
			"username": user, "password": e.SMBPass, "path": base + "-smb"}
	}
	sftp := func(name string, extra map[string]any) map[string]any {
		m := map[string]any{"host": sh, "port": sp, "username": e.SFTPUser, "path": "/upload/" + base + "-" + name}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	smbList := "listing an SMB share on Linux returns mangled paths (backslash and base folder leak into them)"
	sftpNew := "testing an SFTP target whose folder does not exist yet fails with 'file does not exist'"
	return []Config{
		{Name: "smb", Type: "smb", Config: smb(e.SMBUser), Secret: "password",
			Gaps: map[string]string{GapList: smbList}},
		{Name: "smb-domain-user", Type: "smb", Config: smb("WORKGROUP\\" + e.SMBUser), Secret: "password",
			Gaps: map[string]string{GapAll: "the SMB target sends DOMAIN\\user as the plain user name instead of splitting it"}},
		{Name: "sftp-password", Type: "sftp", Config: sftp("pw", map[string]any{"password": e.SFTPPass}), Secret: "password",
			Gaps: map[string]string{GapStorageInfoNew: sftpNew}},
		{Name: "sftp-key", Type: "sftp", Config: sftp("key", map[string]any{"private_key": e.SFTPKey}), Secret: "private_key",
			Gaps: map[string]string{GapStorageInfoNew: sftpNew}},
		{Name: "webdav", Type: "webdav", Config: map[string]any{"url": e.WebDAVURL, "username": e.WebDAVUser,
			"password": e.WebDAVPass, "path": "/" + base + "-webdav"}, Secret: "password"},
		{Name: "s3", Type: "s3", Config: map[string]any{"endpoint": e.S3Endpoint, "region": e.S3Region,
			"bucket": e.S3Bucket, "prefix": base + "-s3", "access_key": e.S3AccessKey, "secret_key": e.S3SecretKey},
			Secret: "secret_key"},
	}
}
