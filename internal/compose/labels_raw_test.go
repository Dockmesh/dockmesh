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
	}{
		{"own labels", svc + "    labels:\n      team: web\n", ""},
		{"tracking label repeated with its value", svc + "    labels:\n      com.docker.compose.project: shop\n", ""},
		{"another project", svc + "    labels:\n      com.docker.compose.project: billing\n", "is reserved for Dockmesh"},
		{"list syntax", svc + "    labels:\n      - com.docker.compose.service=db\n", "is reserved for Dockmesh"},
		{"dockmesh prefix", svc + "    labels:\n      dockmesh.component: proxy\n", "is reserved for Dockmesh"},
		{"through a merge key", "x-l: &l\n  com.docker.compose.project: billing\n" + svc + "    labels:\n      <<: *l\n", "is reserved for Dockmesh"},
		{"interpolated value", svc + "    labels:\n      com.docker.compose.project: ${OTHER}\n", "uses a variable"},
		{"interpolated key", svc + "    labels:\n      ${K}: billing\n", "uses a variable"},
		{"variable in an own label", svc + "    labels:\n      team: ${TEAM}\n", ""},
		{"network label", svc + "networks:\n  n:\n    labels:\n      dockmesh.managed: \"false\"\n", "is reserved for Dockmesh"},
		{"external volume ignored", svc + "volumes:\n  v:\n    external: true\n    labels:\n      dockmesh.x: y\n", ""},
		{"include", "include:\n  - /dev/zero\n" + svc, "upgrade the agent"},
		{"extends from another file", svc + "    extends:\n      file: /dev/zero\n      service: x\n", "upgrade the agent"},
		{"extends in the same file", svc + "    extends:\n      service: base\n  base:\n    image: nginx\n", ""},
		{"label_file", svc + "    label_file: /etc/hostname\n", "upgrade the agent"},
		{"unparsable", "services: [\n", "parse compose file"},
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
		})
	}
	if err := CheckReservedLabelsText("shop", "include:\n  - x.yaml\n"); !errors.Is(err, ErrLabelsNeedAgent) {
		t.Fatalf("include: %v", err)
	}
}
