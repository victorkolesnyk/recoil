package main

import "testing"

func TestTokenizeDropsStopwordsAndSplits(t *testing.T) {
	got := tokenize("Build/ folder in the .gitignore")
	for _, want := range []string{"build", "folder", "gitignore"} {
		if !got[want] {
			t.Errorf("expected token %q", want)
		}
	}
	if got["in"] || got["the"] {
		t.Error("stopwords should be dropped")
	}
}

func TestRecordRoundTrip(t *testing.T) {
	r := record{
		ID: "r1", Created: 100, Trigger: "error", Weight: 1.5,
		Hits: 2, Last: 200, Cue: "alpha beta", Gist: "a lesson",
	}
	got, ok := parseRecord(r.tsv())
	if !ok {
		t.Fatal("parseRecord rejected its own output")
	}
	if got != r {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, r)
	}
}

func TestParseRecordRejectsGarbage(t *testing.T) {
	if _, ok := parseRecord("too\tfew\tcolumns"); ok {
		t.Error("expected failure on wrong column count")
	}
	// 8 columns but a non-numeric weight — must be rejected, not loaded as zero
	if _, ok := parseRecord("r1\t100\terror\tNOTNUM\t0\t200\tcue\tgist"); ok {
		t.Error("expected failure on non-numeric field")
	}
}

func TestScoreRecordsFiresAndIgnores(t *testing.T) {
	recs := []record{
		{Trigger: "correction", Weight: 3, Cue: "menu options proposal"},
		{Trigger: "test-fail", Weight: 2, Cue: "unity build gitignore"},
	}
	fired := scoreRecords(recs, tokenize("about to show a menu of options"))
	if len(fired) != 1 || fired[0].idx != 0 {
		t.Fatalf("expected only the correction record to fire, got %+v", fired)
	}
	if got := scoreRecords(recs, tokenize("tuning the water shader foam")); len(got) != 0 {
		t.Errorf("unrelated situation should fire nothing, got %d", len(got))
	}
}

func TestScoreRecordsRanksBySalience(t *testing.T) {
	// same single-token overlap, but the higher-weight record should rank first
	recs := []record{
		{Trigger: "manual", Weight: 1, Cue: "alpha"},
		{Trigger: "correction", Weight: 3, Cue: "alpha"},
	}
	fired := scoreRecords(recs, tokenize("alpha"))
	if len(fired) != 2 || fired[0].idx != 1 {
		t.Fatalf("expected the weight=3 record first, got %+v", fired)
	}
}
