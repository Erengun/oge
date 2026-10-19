package run

import (
	"reflect"
	"testing"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// adviceCuts is a Chat with three turns that each write a file: turn 1
// the user's, turn 2 Öge's advice turn (k-1), turn 3 the user's. Before
// the first cut the log holds k-1 (advised, sent), k-2 (cancelled) and
// k-3 (still running). k-3 ends after the first cut.
func adviceCuts(t *testing.T, between func(c *Chat, first *Result)) (first, second *Result, c *Chat) {
	t.Helper()
	file := func(name string) []multiturn.File {
		return []multiturn.File{{Path: name + ".go", Content: "package fx\n\nfunc " + name + "() {}\n"}}
	}
	c = openCut(t, "",
		multiturn.Turn{Reply: "one", Writes: file("One")},
		multiturn.Turn{Reply: "two", Writes: file("Two")},
		multiturn.Turn{Reply: "three", Writes: file("Three")})
	adv := map[string]string{"agent": "claude", "model": "opus"}
	started := func(id string, turn int) {
		if err := c.log(RecConsultationStarted, map[string]any{"chat": c.ID, "consultation": id, "turn": turn, "advisor": adv}); err != nil {
			t.Fatal(err)
		}
	}
	ended := func(id, outcome, advice string) {
		d := map[string]any{"chat": c.ID, "consultation": id, "outcome": outcome}
		if advice != "" {
			d["advice"] = advice
		}
		if err := c.log(RecConsultationEnded, d); err != nil {
			t.Fatal(err)
		}
	}
	drain(t, send(t, c, "add One"), nil)
	started("k-1", 1)
	ended("k-1", ConsultAdvised, "sha256:aa")
	ch, err := c.SendAdvice(t.Context(), "k-1", "Add Two.")
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch, nil)
	started("k-2", 2)
	ended("k-2", ConsultCancelled, "")
	started("k-3", 2) // running at the first cut
	first = cut(t, c)
	if first.Outcome != Accepted {
		t.Fatalf("first cut %s %v", first.Outcome, first.Why)
	}
	ended("k-3", ConsultCancelled, "")
	between(c, first)
	drain(t, send(t, c, "add Three"), nil)
	return first, cut(t, c), c
}

func adviceOf(t *testing.T, res *Result) map[string]any {
	t.Helper()
	a, _ := runRecords(t, res.Dir, RecRunStarted)[0]["advice"].(map[string]any)
	return a
}

func consultationIDs(a map[string]any, key string) []string {
	var ids []string
	items, _ := a[key].([]any)
	for _, it := range items {
		ids = append(ids, it.(map[string]any)["consultation"].(string))
	}
	return ids
}

// With no delivery between them, a cut records the Consultations of every
// turn since the last delivery (the Task's window): the second repeats the
// first's and adds the one that was still running when the first was cut.
func TestCutsRecordTheirConsultations(t *testing.T) {
	first, second, _ := adviceCuts(t, func(*Chat, *Result) {})
	a := adviceOf(t, first)
	sent, _ := a["sent"].([]any)
	if len(sent) != 1 {
		t.Fatalf("first cut's advice %v", a)
	}
	if s := sent[0].(map[string]any); s["consultation"] != "k-1" || s["turn"] != float64(2) || s["how"] != "verbatim" ||
		s["agent"] != "claude" || s["model"] != "opus" || s["advice"] != "sha256:aa" {
		t.Errorf("first cut's sent %v", s)
	}
	if got := consultationIDs(a, "dismissed"); !reflect.DeepEqual(got, []string{"k-2"}) {
		t.Errorf("first cut's dismissed %v, want k-2 only: the running one is absent", got)
	}
	b := adviceOf(t, second)
	if got := consultationIDs(b, "sent"); !reflect.DeepEqual(got, []string{"k-1"}) {
		t.Errorf("second cut's sent %v", got)
	}
	if got := consultationIDs(b, "dismissed"); !reflect.DeepEqual(got, []string{"k-2", "k-3"}) {
		t.Errorf("second cut's dismissed %v, want k-2 and k-3", got)
	}
}

// A delivery moves the Task: the next cut records none of the earlier
// cut's Consultations.
func TestACutAfterADeliveryRecordsNoEarlierConsultations(t *testing.T) {
	_, second, _ := adviceCuts(t, func(c *Chat, first *Result) {
		if err := c.Delivered(first.ID, "apply", first.Candidate); err != nil {
			t.Fatal(err)
		}
	})
	// k-3 was running at the delivered cut and ended after it: this cut
	// records it, once. The counted ones (k-1 sent, k-2) are not repeated.
	a := adviceOf(t, second)
	if got := consultationIDs(a, "dismissed"); !reflect.DeepEqual(got, []string{"k-3"}) || len(consultationIDs(a, "sent")) != 0 {
		t.Errorf("a cut after a delivery: %v, want k-3 only", a)
	}
}
