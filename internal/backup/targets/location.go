package targets

import (
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/redact"
)

// Location returns the settings of a target config that say where it
// stores archives: the config without credentials or a pending host key.
// An unset local path is replaced by the directory it stands for, which
// is fixed once per install.
func Location(typ string, cfg any) map[string]any {
	m := map[string]any{}
	if err := decodeConfig(cfg, &m); err != nil || m == nil {
		m = map[string]any{}
	}
	for _, k := range secretFields {
		delete(m, k)
	}
	delete(m, "host_key_pending")
	if typ == "local" {
		if p, _ := m["path"].(string); p == "" && emptyPathDir != "" {
			m["path"] = emptyPathDir
		}
	}
	return m
}

// placeFields are the settings of each target type that decide where an
// archive lands. Credentials, user names and trusted CAs are not among
// them: they say how to get in, and come from the target as it is now.
var placeFields = map[string][]string{
	"local":  {"path"},
	"sftp":   {"host", "port", "path"},
	"smb":    {"host", "port", "share", "path"},
	"s3":     {"endpoint", "region", "use_ssl", "bucket", "prefix"},
	"webdav": {"url", "path"},
}

// SameLocation reports whether two locations of one type point at the
// same place.
func SameLocation(typ string, a, b map[string]any) bool {
	for _, k := range placeFields[typ] {
		if !reflect.DeepEqual(norm(a[k]), norm(b[k])) {
			return false
		}
	}
	return true
}

// norm makes values read back from JSON compare equal to the ones
// decoded into a map directly (numbers, empty strings vs absent).
func norm(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "" {
			return nil
		}
		return x
	case bool:
		if !x {
			return nil
		}
		return x
	case int:
		if x == 0 {
			return nil
		}
		return float64(x)
	case float64:
		if x == 0 {
			return nil
		}
		return x
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// SameAccount reports whether two locations name the same server and
// user, the fields a stored credential is bound to (redact.Bound).
func SameAccount(a, b map[string]any) bool {
	for _, k := range redact.Bound {
		if !reflect.DeepEqual(norm(a[k]), norm(b[k])) {
			return false
		}
	}
	return true
}

// AtLocation returns the current config cur moved to the place recorded
// in loc on the same server and account: credentials and trusted CA stay
// those of cur. An SFTP config without a host key takes the one recorded
// with loc.
func AtLocation(typ string, loc map[string]any, cur any) map[string]any {
	out := map[string]any{}
	if err := decodeConfig(cur, &out); err != nil || out == nil {
		out = map[string]any{}
	}
	if k, _ := loc["host_key"].(string); typ == "sftp" && k != "" {
		if have, _ := out["host_key"].(string); have == "" {
			out["host_key"] = k
			delete(out, "host_key_pending")
		}
	}
	for _, k := range placeFields[typ] {
		if v, ok := loc[k]; ok {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	return out
}

// DescribeLocation names a location for people: the server and the path,
// bucket or share, never the user name.
func DescribeLocation(typ string, loc map[string]any) string {
	str := func(k string) string { s, _ := loc[k].(string); return s }
	port := func(def int) string {
		var n int
		switch v := loc["port"].(type) {
		case float64:
			n = int(v)
		case int:
			n = v
		}
		if n == 0 {
			n = def
		}
		return strconv.Itoa(n)
	}
	switch typ {
	case "local":
		return "local " + str("path")
	case "sftp":
		p := str("path")
		if p == "" {
			p = "/backups"
		}
		return "sftp " + net.JoinHostPort(str("host"), port(22)) + ":" + p
	case "smb":
		return "smb //" + str("host") + "/" + strings.Trim(str("share")+"/"+str("path"), "/")
	case "s3":
		return "s3 " + strings.TrimRight(str("endpoint")+"/"+str("bucket")+"/"+str("prefix"), "/")
	case "webdav":
		return "webdav " + strings.TrimRight(str("url"), "/") + "/" + strings.TrimLeft(str("path"), "/")
	}
	return fmt.Sprintf("%s target", typ)
}
