package compose

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

// Whatever the text check accepts must, loaded by compose-go the way an
// older agent loads it, neither read a file outside the stack folder nor
// set a reserved label. The stack folder sits next to files that would
// show up in the project if they were read.
func TestCheckReservedLabelsTextMatchesLoader(t *testing.T) {
	inputs := map[string]string{
		"tagged merge key":               "services:\n  web:\n    image: alpine\n    !!merge x: {env_file: ../outside.env}\n",
		"tagged merge of <<":             "x-b: &b {env_file: ../outside.env}\nservices:\n  web:\n    image: alpine\n    !!merge <<: *b\n",
		"tagged quoted merge key":        "services:\n  web:\n    image: alpine\n    !!merge \"q\": {env_file: ../outside.env}\n",
		"tagged explicit key":            "services:\n  web:\n    image: alpine\n    ? !!merge y\n    : env_file: ../outside.env\n",
		"verbatim merge tag":             "services:\n  web:\n    image: alpine\n    !<tag:yaml.org,2002:merge> x: {env_file: ../outside.env}\n",
		"tagged merge in flow":           "services:\n  web: {image: alpine, !!merge x: {env_file: ../outside.env}}\n",
		"tagged merge of an alias":       "x-a: &a\n  env_file: ../outside.env\nservices:\n  web:\n    image: alpine\n    !!merge x: *a\n",
		"tagged merge in a hook":         "services:\n  web:\n    image: alpine\n    pre_start:\n      - command: [\"true\"]\n        !!merge z: {env_file: ../outside.env}\n",
		"tagged merge of hooks":          "services:\n  web:\n    image: alpine\n    !!merge x: {pre_start: [{command: [\"true\"], env_file: ../outside.env}]}\n",
		"tagged merge of labels":         "services:\n  web:\n    image: alpine\n    !!merge q: {labels: {dockmesh.x: evil}}\n",
		"tagged merge of label_file":     "services:\n  web:\n    image: alpine\n    !!merge x: {label_file: ../outside.env}\n",
		"tagged merge of extends":        "services:\n  web:\n    image: alpine\n    !!merge x: {extends: {file: ../outside.yaml, service: x}}\n",
		"tagged merge in env_file":       "services:\n  web:\n    image: alpine\n    env_file:\n      - path: ok.env\n        !!merge m: {path: ../outside.env}\n",
		"top-level tagged include":       "!!merge x: {include: [../outside.yaml]}\nservices:\n  web:\n    image: alpine\n",
		"top-level tagged services":      "!!merge x: {services: {web: {image: alpine, env_file: ../outside.env}}}\n",
		"plain merge":                    "x-b: &b {env_file: ../outside.env}\nservices:\n  web:\n    image: alpine\n    <<: *b\n",
		"plain merge overridden":         "x-b: &b {env_file: ../outside.env}\nservices:\n  web:\n    image: alpine\n    env_file: ok.env\n    <<: *b\n",
		"plain merge first":              "x-b: &b {env_file: ../outside.env}\nservices:\n  web:\n    <<: *b\n    image: alpine\n    env_file: ok.env\n",
		"merge of a list":                "x-a: &a {env_file: ok.env}\nx-b: &b {env_file: ../outside.env}\nservices:\n  web:\n    image: alpine\n    <<: [*a, *b]\n",
		"nested merge":                   "x-b: &b {env_file: ../outside.env}\nservices:\n  web:\n    image: alpine\n    <<: {<<: *b}\n",
		"quoted << key":                  "services:\n  web:\n    image: alpine\n    \"<<\": {env_file: ../outside.env}\n",
		"merge of a relative file":       "x-a: &a {env_file: ok.env}\nservices:\n  web:\n    image: alpine\n    <<: *a\n",
		"extends in the same file":       "services:\n  base:\n    image: alpine\n    env_file: ok.env\n  web:\n    extends: base\n",
		"list of paths":                  "services:\n  web:\n    image: alpine\n    env_file: [{path: ok.env}, \"../outside.env\"]\n",
		"dot dot after a folder":         "services:\n  web:\n    image: alpine\n    env_file: ok.env/../../outside.env\n",
		"double slash":                   "services:\n  web:\n    image: alpine\n    env_file: \".//../outside.env\"\n",
		"leading space":                  "services:\n  web:\n    image: alpine\n    env_file: \" ../outside.env\"\n",
		"long s in a key":                "ſervices:\n  web:\n    image: alpine\n    env_file: ../outside.env\n",
		"kelvin sign in a key":           "services:\n  web:\n    image: alpine\n    networKs: {}\n",
		"upper-case env_file":            "services:\n  web:\n    image: alpine\n    ENV_FILE: ../outside.env\n",
		"override tag":                   "services:\n  web:\n    image: alpine\n    env_file: !override [../outside.env]\n",
		"job extends":                    "services:\n  web:\n    image: alpine\njobs:\n  j:\n    image: alpine\n    triggers: {manual: true}\n    extends: {file: ../outside.yaml, service: x}\n",
		"job extends a non-compose file": "services:\n  web:\n    image: alpine\njobs:\n  j:\n    image: alpine\n    triggers: {manual: true}\n    extends: {file: ../outside.env, service: x}\n",
		"job env_file":                   "services:\n  web:\n    image: alpine\njobs:\n  j:\n    image: alpine\n    triggers: {manual: true}\n    env_file: ../outside.env\n",
		"relative env_file":              "services:\n  web:\n    image: alpine\n    env_file: ok.env\n",
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "outside.env"), "SECRET=leaked\ndockmesh.x=evil\n")
	writeFile(t, filepath.Join(root, "outside.yaml"), "services:\n  x:\n    image: busybox\n    environment: {SECRET: leaked}\n    labels: {dockmesh.x: evil}\n")
	dir := filepath.Join(root, "stack")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "ok.env"), "SECRET=ok\n")

	for name, y := range inputs {
		t.Run(name, func(t *testing.T) {
			if CheckReservedLabelsText(y) != nil {
				return
			}
			file := filepath.Join(dir, "compose.yaml")
			writeFile(t, file, y)
			opts, err := cli.NewProjectOptions([]string{file},
				cli.WithName("p"), cli.WithWorkingDirectory(dir), cli.WithResolvedPaths(true))
			if err != nil {
				t.Fatal(err)
			}
			proj, err := opts.LoadProject(context.Background())
			if err != nil {
				// An error naming an outside file means compose-go opened
				// or parsed it before giving up. Paths below the stack
				// folder, such as "stack/ ../outside.env", are left out.
				msg := insideStackPath.ReplaceAllString(strings.ReplaceAll(err.Error(), dir, "<stack>"), "")
				if strings.Contains(msg, "outside.") || strings.Contains(msg, root) {
					t.Errorf("accepted, and compose-go reached outside the stack: %v", err)
				}
				return
			}
			specs := map[string]types.ContainerSpec{}
			for n, s := range proj.Services {
				specs["service "+n] = s.ContainerSpec
				for i, h := range s.PreStart {
					specs["service "+n+" hook "+string(rune('0'+i))] = h.ContainerSpec
				}
			}
			for n, j := range proj.Jobs {
				specs["job "+n] = j.ContainerSpec
			}
			for n, s := range specs {
				if v := s.Environment["SECRET"]; v != nil && *v == "leaked" {
					t.Errorf("accepted, but %s read a file outside the stack", n)
				}
				for k := range s.Labels {
					if strings.HasPrefix(k, reservedLabelPrefix) || strings.HasPrefix(k, "com.docker.compose.") {
						t.Errorf("accepted, but %s has label %s", n, k)
					}
				}
				for _, ef := range s.EnvFiles {
					if !strings.HasPrefix(ef.Path, dir+string(filepath.Separator)) {
						t.Errorf("accepted, but %s has env_file %s", n, ef.Path)
					}
				}
				for _, lf := range s.LabelFiles {
					if !strings.HasPrefix(lf, dir+string(filepath.Separator)) {
						t.Errorf("accepted, but %s has label_file %s", n, lf)
					}
				}
			}
		})
	}
}

var insideStackPath = regexp.MustCompile(`<stack>/[^:]*`)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
