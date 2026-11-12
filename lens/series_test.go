package lens

import (
	"strings"
	"testing"
	"time"
)

func TestSeriesSummarisesAJump(t *testing.T) {
	now := time.Unix(1000, 0)
	in := `{"data":[{"metric":{"search_mode":"vector"},"values":[[640,"0.024"],[700,"0.025"],[760,"0.024"],[820,"2.4"],[880,"2.42"],[940,"2.43"]]},
	                {"metric":{},"values":[[640,"0.08"],[700,"0.07"],[760,"0.08"],[820,"0.08"],[880,"0.08"],[940,"NaN"]]}]}`
	out, ok := Series(in, now)
	if !ok {
		t.Fatal("not recognised")
	}
	for _, want := range []string{"2 time series", "{search_mode=vector}: 0.024 at the start", "2.43 now", "jumped from 0.024 to 2.40 3 min ago", "(all): 0.08", "steady"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSeriesLeavesOtherTextAlone(t *testing.T) {
	for _, in := range []string{`not json`, `{"data":[{"line":"config reloaded"}]}`, `{"data":[{"metric":{},"value":[1,"2"]}]}`} {
		if out, ok := Series(in, time.Now()); ok || out != in {
			t.Fatalf("changed %q -> %q", in, out)
		}
	}
}
