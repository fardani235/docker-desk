package secret

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSecretNeverFormats(t *testing.T) {
	s := New("hunter2-value")
	type holder struct{ S Secret }
	j, _ := json.Marshal(holder{s})
	out := fmt.Sprintf("%v %+v %#v %s %q %x", s, holder{s}, holder{s}, s, s, s) + string(j)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("value printed: %s", out)
	}
	if s.Reveal() != "hunter2-value" || !s.Present() || (Secret{}).Present() {
		t.Fatal("Reveal/Present")
	}
}
