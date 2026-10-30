package scripts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// --dry-run only holds if every mutating command goes through run().
func TestUninstallMutationsUseRun(t *testing.T) {
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	mutating := regexp.MustCompile(`^\s*(pkill|kill|rm|systemctl|userdel|groupdel|docker|launchctl|mv|chown)\b`)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if mutating.MatchString(line) {
			t.Errorf("uninstall.sh:%d runs a mutating command outside run(): %s", i+1, strings.TrimSpace(line))
		}
	}
}
