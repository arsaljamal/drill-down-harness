package lens

import (
	"strings"
	"testing"
	"time"
)

func TestLogsShowAgesNewestFirst(t *testing.T) {
	now := time.Unix(1000, 0)
	in := `{"data":[
	  {"timestamp":"\"700000000000\"","line":"config reloaded: db.max_conns=20","structuredMetadata":{"x":"y"}},
	  {"timestamp":"\"880000000000\"","line":"config reloaded: db.max_conns=1"}]}`
	out, ok := Logs(in, now)
	if !ok {
		t.Fatal("not recognised")
	}
	want := "2 log line(s), newest first (ages computed by the harness):\n- 2 min ago: config reloaded: db.max_conns=1\n- 5 min ago: config reloaded: db.max_conns=20"
	if out != want {
		t.Fatalf("got:\n%s", out)
	}
	if strings.Contains(out, "structuredMetadata") {
		t.Fatal("metadata leaked")
	}
}

func TestLogsLeaveOtherTextAlone(t *testing.T) {
	if _, ok := Logs(`{"data":[{"metric":{},"values":[[1,"2"],[2,"3"]]}]}`, time.Now()); ok {
		t.Fatal("series treated as logs")
	}
}
