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
		{"tracking label repeated with its value", svc + "    labels:\n      com.docker.compose.project: shop\n", "", false},
		{"another project", svc + "    labels:\n      com.docker.compose.project: billing\n", "is reserved for Dockmesh", false},
		{"list syntax", svc + "    labels:\n      - com.docker.compose.service=db\n", "is reserved for Dockmesh", false},
		{"dockmesh prefix", svc + "    labels:\n      dockmesh.component: proxy\n", "is reserved for Dockmesh", false},
		{"through a merge key", "x-l: &l\n  com.docker.compose.project: billing\n" + svc + "    labels:\n      <<: *l\n", "is reserved for Dockmesh", false},
		{"interpolated value", svc + "    labels:\n      com.docker.compose.project: ${OTHER}\n", "uses a variable", true},
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
		{"unparsable", "services: [\n", "parse compose file", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckReservedLabelsText("shop", tt.compose)
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
