package host

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/events"
)

func TestReadBudgetBoundsOneEvent(t *testing.T) {
	small := `{"Action":"die","Actor":{"Attributes":{"name":"web"}}}`
	tests := []struct {
		name    string
		input   io.Reader
		wantN   int
		wantErr error
	}{
		{"many small events", strings.NewReader(strings.Repeat(small+"\n", 5000)), 5000, io.EOF},
		{"endless value", io.MultiReader(strings.NewReader(`{"Actor":{"Attributes":{"name":"`), endless{}), 0, errEventTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := &readBudget{r: tt.input, left: maxEventBytes}
			dec := json.NewDecoder(budget)
			n := 0
			var err error
			for {
				var m events.Message
				err = dec.Decode(&m)
				budget.left = maxEventBytes
				if err != nil {
					break
				}
				n++
			}
			if n != tt.wantN || !errors.Is(err, tt.wantErr) {
				t.Fatalf("decoded %d, err %v; want %d, %v", n, err, tt.wantN, tt.wantErr)
			}
		})
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}
