package compose

import (
	"errors"
	"strings"
	"testing"
)

// The text check never opens a file: a compose file that names other
// files is refused for agents that don't check labels themselves.
func TestCheckReservedLabelsText(t *testing.T) {
	svc := "services:\n  web:\n    image: nginx\n"
	tests := []struct {
		name, compose string
		want          string // empty: accepted
		needAgent     bool
	}{
		{"own labels", svc + "    labels:\n      team: web\n", "", false},
		{"tracking label repeated with its value", svc + "    labels:\n      com.docker.compose.project: shop\n", "is reserved for Dockmesh", false},
		{"another project", svc + "    labels:\n      com.docker.compose.project: billing\n", "is reserved for Dockmesh", false},
		{"list syntax", svc + "    labels:\n      - com.docker.compose.service=db\n", "is reserved for Dockmesh", false},
		{"dockmesh prefix", svc + "    labels:\n      dockmesh.component: proxy\n", "is reserved for Dockmesh", false},
		{"through a merge key", "x-l: &l\n  com.docker.compose.project: billing\n" + svc + "    labels:\n      <<: *l\n", "is reserved for Dockmesh", false},
		{"interpolated value", svc + "    labels:\n      com.docker.compose.project: ${OTHER}\n", "is reserved for Dockmesh", false},
		{"interpolated key", svc + "    labels:\n      ${K}: billing\n", "uses a variable", true},
		{"variable in an own label", svc + "    labels:\n      team: ${TEAM}\n", "", false},
		{"network label", svc + "networks:\n  n:\n    labels:\n      dockmesh.managed: \"false\"\n", "is reserved for Dockmesh", false},
		{"external volume ignored", svc + "volumes:\n  v:\n    external: true\n    labels:\n      dockmesh.x: y\n", "", false},
		{"include", "include:\n  - /dev/zero\n" + svc, "upgrade the agent", true},
		{"extends from another file", svc + "    extends:\n      file: /dev/zero\n      service: x\n", "upgrade the agent", true},
		{"extends in the same file", svc + "    extends:\n      service: base\n  base:\n    image: nginx\n", "", false},
		{"label_file", svc + "    label_file: /etc/hostname\n", "upgrade the agent", true},
		{"second document", svc + "---\nservices:\n  web:\n    labels:\n      com.docker.compose.project: billing\n", "several YAML documents", true},
		{"extends with an odd key", svc + "    extends:\n      service: base\n      file: /tmp/x.yaml\n      1: x\n", "upgrade the agent", true},
		{"service label passed on through extends", "services:\n  base:\n    image: nginx\n    labels:\n      com.docker.compose.service: base\n  web:\n    extends: base\n", "is reserved for Dockmesh", false},
		{"!reset behind a merge key", "x-b: &b {com.docker.compose.project: billing}\n" + svc + "    labels:\n      <<: *b\n      com.docker.compose.project: !reset shop\n", "YAML tag !reset", true},
		{"!reset on a whole service", "x-s: &s {web: {image: nginx, labels: {dockmesh.component: proxy}}}\nservices:\n  <<: *s\n  web: !reset {image: nginx}\n", "YAML tag !reset", true},
		{"!override", svc + "    labels: !override {team: x}\n", "YAML tag !override", true},
		{"number as service label", "services:\n  \"0\":\n    image: nginx\n    labels:\n      com.docker.compose.service: -0\n", "is reserved for Dockmesh", false},
		{"number that compose rewrites", svc + "    labels:\n      com.docker.compose.project: 1e2\n", "is reserved for Dockmesh", false},
		{"other compose label", svc + "    labels:\n      com.docker.compose.oneoff: \"False\"\n", "is reserved for Dockmesh", false},
		{"empty value in a flow map", svc + "    labels: {com.docker.compose.project:}\n", "plain value", true},
		{"upper-case include", "Include:\n  - /dev/zero\n" + svc, "upgrade the agent", true},
		{"upper-case services", "SERVICES:\n  web:\n    image: nginx\n    labels:\n      dockmesh.x: y\n", "upgrade the agent", true},
		{"upper-case label_file", svc + "    LABEL_FILE: /etc/hostname\n", "upgrade the agent", true},
		{"upper-case labels", svc + "    Labels:\n      dockmesh.x: y\n", "upgrade the agent", true},
		{"upper-case extends", svc + "    Extends:\n      file: /dev/zero\n      service: x\n", "upgrade the agent", true},
		{"upper-case network labels", svc + "networks:\n  n:\n    LABELS:\n      dockmesh.x: y\n", "upgrade the agent", true},
		{"upper-case external", svc + "volumes:\n  v:\n    External: true\n", "upgrade the agent", true},
		{"upper-case key through a merge key", "x-s: &s {LABEL_FILE: /etc/hostname}\n" + svc + "    <<: *s\n", "upgrade the agent", true},
		{"upper-case pre_start", svc + "    PRE_START:\n      - image: busybox\n", "upgrade the agent", true},
		{"pre_start label_file", svc + "    pre_start:\n      - image: busybox\n        label_file: /etc/hostname\n", "upgrade the agent", true},
		{"tagged merge key", svc + "    !!merge x: {label_file: /etc/hostname}\n", "YAML tag !!merge", true},
		{"verbatim merge tag", svc + "    !<tag:yaml.org,2002:merge> x: {labels: {dockmesh.x: y}}\n", "YAML tag !!merge", true},
		{"non-specific tag", svc + "    labels: ! {dockmesh.x: y}\n", "YAML tag !", true},
		{"non-specific tag after an anchor", "x-b: &b {env_file: /etc/hostname}\n" + svc + "    ! <<: *b\n", "YAML tag !", true},
		{"non-specific tag on a key", "! services:\n  web:\n    image: nginx\n", "YAML tag !", true},
		{"anchor then non-specific tag", svc + "    labels: &l ! {team: x}\n", "YAML tag !", true},
		{"non-specific tag on the line after an anchor", svc + "    labels: &l\n      # c\n      ! {team: x}\n", "YAML tag !", true},
		{"anchor before a block map", "x-l: &l\n  team: x\n" + svc + "    labels: *l\n", "", false},
		{"exclamation mark in values", svc + "    command: echo hi!\n    environment:\n      A: \"!x\"\n", "", false},
		{"explicit core tag", svc + "    labels: {team: !!str web}\n", "YAML tag !!str", true},
		{"jobs", svc + "jobs:\n  j:\n    image: alpine\n    extends: {file: ../other/compose.yaml, service: x}\n", "jobs: ", true},
		{"key with a long s", "ſervices:\n  web:\n    image: nginx\n", "upgrade the agent", true},
		{"key with a kelvin sign", svc + "    networKs: {}\n", "upgrade the agent", true},
		{"mixed-case extension key", "x-Common: {a: b}\n" + svc + "    x-Team: web\n", "", false},
		{"anchor that merges itself", "&a\n<<: *a\n", "parse compose file", false},
		{"unparsable", "services: [\n", "parse compose file", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckReservedLabelsText(tt.compose)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
			if errors.Is(err, ErrLabelsNeedAgent) != tt.needAgent {
				t.Fatalf("err = %v, ErrLabelsNeedAgent %v, want %v", err, !tt.needAgent, tt.needAgent)
			}
		})
	}
}

func TestCheckReservedLabelsTextEnvFile(t *testing.T) {
	svc := "services:\n  web:\n    image: nginx\n"
	tests := []struct {
		name, compose string
		want          string // empty: accepted
	}{
		{"dot env", svc + "    env_file: .env\n", ""},
		{"dot slash", svc + "    env_file: ./app.env\n", ""},
		{"subfolder", svc + "    env_file: conf/app.env\n", ""},
		{"list of relative", svc + "    env_file:\n      - .env\n      - conf/app.env\n", ""},
		{"map form relative", svc + "    env_file:\n      - path: ./app.env\n        required: false\n        format: raw\n", ""},
		{"pre_start relative", svc + "    pre_start:\n      - image: busybox\n        env_file: .env\n", ""},
		{"absolute", svc + "    env_file: /etc/hostname\n", "is absolute"},
		{"parent", svc + "    env_file: ../x\n", "contains .."},
		{"parent in the middle", svc + "    env_file: a/../../x\n", "contains .."},
		{"home", svc + "    env_file: ~/.env\n", "starts with ~"},
		{"variable", svc + "    env_file: ${HOME}/x\n", "uses a variable"},
		{"bare variable", svc + "    env_file: $F\n", "uses a variable"},
		{"empty", svc + "    env_file: \"\"\n", "is empty"},
		{"number", svc + "    env_file: 1\n", "must be a path or a list"},
		{"bool", svc + "    env_file: true\n", "must be a path or a list"},
		{"null", svc + "    env_file:\n", "must be a path or a list"},
		{"map instead of a list", svc + "    env_file: {path: .env}\n", "must be a path or a list"},
		{"list with absolute", svc + "    env_file:\n      - .env\n      - /etc/hostname\n", "is absolute"},
		{"list with number", svc + "    env_file:\n      - 1\n", "must be a path or a mapping"},
		{"map form absolute", svc + "    env_file:\n      - path: /etc/hostname\n        required: true\n", "is absolute"},
		{"map form without path", svc + "    env_file:\n      - required: false\n", "must be a path or a mapping"},
		{"map form number path", svc + "    env_file:\n      - path: 1\n", "must be a path or a mapping"},
		{"map form upper-case path", svc + "    env_file:\n      - path: .env\n        PATH: /etc/hostname\n", "unsupported key"},
		{"upper-case key", svc + "    ENV_FILE: /etc/hostname\n", "doesn't check env_file paths"},
		{"through a merge key", "x-e: &e {env_file: /etc/hostname}\n" + svc + "    <<: *e\n", "is absolute"},
		{"pre_start absolute", svc + "    pre_start:\n      - image: busybox\n        env_file: /etc/hostname\n", "is absolute"},
		{"pre_start not a list", svc + "    pre_start: {image: busybox}\n", "must be a list"},
		{"tag", svc + "    env_file: !reset []\n", "YAML tag !reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckReservedLabelsText(tt.compose)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "upgrade the agent from the Hosts page") {
				t.Fatalf("err = %v, want the upgrade hint the API answers with 422", err)
			}
		})
	}
}
