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
	for _, want := range []string{"2 time series", "{search_mode=vector}: 2.43 now", "rose from 0.024 to this level 3 min ago", "(all): 0.08 now", "steady"} {
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

func TestSeriesIgnoresAnOldSpike(t *testing.T) {
	now := time.Unix(1000, 0)
	// a spike long ago, back to normal, then a new rise 2 minutes ago
	in := `{"data":[{"metric":{},"values":[[400,"3.7"],[460,"3.6"],[520,"0.1"],[580,"0.1"],[640,"0.1"],[700,"0.1"],[760,"0.1"],[820,"0.1"],[880,"2.4"],[940,"2.4"]]}]}`
	out, _ := SeriesFor("my_query", in, now)
	if !strings.Contains(out, "Result of my_query") || !strings.Contains(out, "rose from 0.1 to this level 2 min ago") {
		t.Fatalf("got:\n%s", out)
	}
}
