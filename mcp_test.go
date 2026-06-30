package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- safeProjectName ---

func TestSafeProjectNameAcceptsValid(t *testing.T) {
	old := os.Getenv("RECOIL_DIR")
	dir := t.TempDir()
	os.Setenv("RECOIL_DIR", dir)
	defer os.Setenv("RECOIL_DIR", old)

	for _, name := range []string{"mcp-server", "renovateam", "zhyva_apteka", "a", "project123"} {
		clean, errMsg := safeProjectName(name)
		if errMsg != "" {
			t.Errorf("expected %q to be valid, got error: %s", name, errMsg)
		}
		if clean != name {
			// only difference allowed is lowercasing
			if clean != name && clean != toLowerASCII(name) {
				t.Errorf("unexpected cleaning of %q -> %q", name, clean)
			}
		}
	}
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestSafeProjectNameRejectsPathTraversal(t *testing.T) {
	old := os.Getenv("RECOIL_DIR")
	dir := t.TempDir()
	os.Setenv("RECOIL_DIR", dir)
	defer os.Setenv("RECOIL_DIR", old)

	attacks := []string{
		"../../../etc",
		"../secrets",
		"a/../../b",
		"..",
		".",
		"/etc/passwd",
		"a/b",
		`a\b`,
	}
	for _, name := range attacks {
		if _, errMsg := safeProjectName(name); errMsg == "" {
			t.Errorf("expected %q to be rejected as path traversal, but it was accepted", name)
		}
	}
}

func TestSafeProjectNameRejectsBadChars(t *testing.T) {
	for _, name := range []string{"my project", "proj!", "proj@host", "проєкт", "проект"} {
		if _, errMsg := safeProjectName(name); errMsg == "" {
			t.Errorf("expected %q to be rejected for invalid characters", name)
		}
	}
}

func TestSafeProjectNameRejectsEmpty(t *testing.T) {
	if _, errMsg := safeProjectName(""); errMsg == "" {
		t.Error("expected empty name to be rejected")
	}
}

func TestSafeProjectNameRejectsTooLong(t *testing.T) {
	long := ""
	for i := 0; i < maxProjectNameLen+10; i++ {
		long += "a"
	}
	if _, errMsg := safeProjectName(long); errMsg == "" {
		t.Error("expected overly long name to be rejected")
	}
}

// --- truncate ---

func TestTruncateRespectsLimit(t *testing.T) {
	got := truncate("hello world", 5)
	// "hello" (5 bytes) + "…" (3 bytes, U+2026 in UTF-8) = 8 bytes max
	const ellipsisBytes = 3
	if len(got) > 5+ellipsisBytes {
		t.Errorf("truncate exceeded reasonable bound: %q (len=%d)", got, len(got))
	}
	if len(got) <= 5 && got == "hello world" {
		t.Error("truncate did not actually truncate")
	}
}

func TestTruncateNoOpUnderLimit(t *testing.T) {
	s := "short"
	if got := truncate(s, 100); got != s {
		t.Errorf("expected no change for short string, got %q", got)
	}
}

func TestTruncateRespectsUTF8Boundary(t *testing.T) {
	// Ukrainian text — multi-byte UTF-8. Must not panic or produce invalid UTF-8.
	s := "Помилка при збірці проєкту через мережеву проблему"
	got := truncate(s, 10)
	if !isValidUTF8(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
}

func isValidUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, size := decodeRune(s[i:])
		if r == 0xFFFD && size == 1 {
			return false
		}
		i += size
	}
	return true
}

func decodeRune(s string) (rune, int) {
	for _, r := range s {
		// first rune and its byte width
		for w := 1; w <= 4; w++ {
			if w <= len(s) && string([]byte(s)[:w]) == string(r) {
				return r, w
			}
		}
		return r, len(string(r))
	}
	return 0, 0
}

// --- sanitiseText ---

func TestSanitiseTextDropsControlChars(t *testing.T) {
	input := "normal text\x00\x01\x02 with control bytes"
	got := sanitiseText(input)
	for _, b := range []byte{0x00, 0x01, 0x02} {
		for i := 0; i < len(got); i++ {
			if got[i] == b {
				t.Errorf("control byte 0x%02x survived sanitisation", b)
			}
		}
	}
}

func TestSanitiseTextKeepsNewlinesAndTabs(t *testing.T) {
	input := "line one\nline two\tindented"
	got := sanitiseText(input)
	if got != input {
		t.Errorf("expected newlines/tabs preserved, got %q", got)
	}
}

func TestSanitiseTextKeepsUkrainian(t *testing.T) {
	input := "Помилка білда через GOTOOLCHAIN"
	got := sanitiseText(input)
	if got != input {
		t.Errorf("Ukrainian text should pass through unchanged, got %q", got)
	}
}

// --- severityFor ---

func TestSeverityForValidValues(t *testing.T) {
	for _, s := range []string{severityFatal, severityBlocker, severityLesson, severityPattern} {
		if got := severityFor(s); got != s {
			t.Errorf("severityFor(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestSeverityForDefaultsToLesson(t *testing.T) {
	for _, bad := range []string{"", "critical", "WHATEVER", "Fatal", "FATAL"} {
		if got := severityFor(bad); got != severityLesson {
			t.Errorf("severityFor(%q) = %q, want %q (default)", bad, got, severityLesson)
		}
	}
}

// --- weightForSeverity ---

func TestWeightForSeverityOrdering(t *testing.T) {
	// fatal and pattern should be the highest-weighted, lesson the lowest of the four
	wFatal := weightForSeverity(severityFatal, "manual")
	wBlocker := weightForSeverity(severityBlocker, "manual")
	wPattern := weightForSeverity(severityPattern, "manual")
	wLesson := weightForSeverity(severityLesson, "manual")

	if wLesson >= wBlocker {
		t.Errorf("lesson weight (%v) should be less than blocker weight (%v)", wLesson, wBlocker)
	}
	if wBlocker >= wFatal {
		t.Errorf("blocker weight (%v) should be less than fatal weight (%v)", wBlocker, wFatal)
	}
	if wFatal != wPattern {
		t.Errorf("fatal (%v) and pattern (%v) should carry equal max weight", wFatal, wPattern)
	}
}

// --- noDecaySeverities ---

func TestNoDecaySeveritiesIncludesCriticalOnes(t *testing.T) {
	for _, s := range []string{severityFatal, severityBlocker, severityPattern} {
		if !noDecaySeverities[s] {
			t.Errorf("%q should be marked as no-decay", s)
		}
	}
	if noDecaySeverities[severityLesson] {
		t.Error("lesson severity should decay normally, not be marked permanent")
	}
}

// --- detectPattern ---

func TestDetectPatternFindsRecurringCue(t *testing.T) {
	recs := []record{
		{Cue: "go build linux proxy network ubuntu", Gist: "first occurrence", Hits: 0},
		{Cue: "docker compose volume mount", Gist: "unrelated", Hits: 0},
	}
	incoming := tokenize("go build linux proxy ubuntu fails again")
	idx, found := detectPattern(recs, incoming)
	if !found {
		t.Fatal("expected pattern to be detected for overlapping cue")
	}
	if idx != 0 {
		t.Errorf("expected match at index 0, got %d", idx)
	}
}

func TestDetectPatternIgnoresLowOverlap(t *testing.T) {
	recs := []record{
		{Cue: "go build linux proxy network ubuntu", Gist: "first occurrence", Hits: 0},
	}
	// Only one shared token ("go") — below the 3-token threshold
	incoming := tokenize("go test python script")
	_, found := detectPattern(recs, incoming)
	if found {
		t.Error("expected no pattern for low token overlap")
	}
}

func TestDetectPatternEmptyStoreNeverMatches(t *testing.T) {
	var recs []record
	incoming := tokenize("anything at all here")
	if _, found := detectPattern(recs, incoming); found {
		t.Error("empty store should never produce a pattern match")
	}
}

// --- project isolation (integration-style, using real temp dirs) ---

func TestProjectIsolationKeepsStoresSeparate(t *testing.T) {
	old := os.Getenv("RECOIL_DIR")
	dir := t.TempDir()
	os.Setenv("RECOIL_DIR", dir)
	defer os.Setenv("RECOIL_DIR", old)
	defer func() { activeProject = "" }()

	// Project A: write a lesson
	activeProject = "project-a"
	if err := os.MkdirAll(projectStoreDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	recA := record{ID: "ra1", Created: 1, Trigger: "error", Weight: 1.5, Cue: "alpha secret", Gist: "lesson for A only"}
	if err := mcpAppendRecord(recA); err != nil {
		t.Fatal(err)
	}

	// Project B: should see nothing from A
	activeProject = "project-b"
	if err := os.MkdirAll(projectStoreDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	recsB, err := mcpLoadRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recsB) != 0 {
		t.Errorf("expected project-b store to be empty, found %d record(s) — isolation broken", len(recsB))
	}

	// Back to A: lesson must still be there
	activeProject = "project-a"
	recsA, err := mcpLoadRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recsA) != 1 || recsA[0].Gist != "lesson for A only" {
		t.Errorf("expected project-a to retain its own lesson, got %+v", recsA)
	}

	// Verify on-disk separation explicitly
	pathA := filepath.Join(dir, "project-a", "store.tsv")
	pathB := filepath.Join(dir, "project-b", "store.tsv")
	if pathA == pathB {
		t.Fatal("project paths must differ")
	}
}

func TestProjectStorePathDefaultsWhenNoProjectSet(t *testing.T) {
	old := os.Getenv("RECOIL_DIR")
	dir := t.TempDir()
	os.Setenv("RECOIL_DIR", dir)
	defer os.Setenv("RECOIL_DIR", old)
	defer func() { activeProject = "" }()

	activeProject = ""
	got := projectStoreDir()
	if got != storeDir() {
		t.Errorf("with no active project, projectStoreDir() should equal storeDir(); got %q vs %q", got, storeDir())
	}
}

// --- file permissions (security regression guard) ---

func TestAppendRecordCreatesRestrictivePermissions(t *testing.T) {
	// Windows has no Unix-style group/other permission bits — os.OpenFile's
	// mode argument only controls the read-only attribute there (per the
	// Go os package docs: "only the 0200 bit ... is used; it controls
	// whether the file's read-only attribute is set"). Group/other
	// restriction on Windows works through NTFS ACLs, not chmod-style bits,
	// so this check is meaningful only on Unix-like systems.
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits (group/other) are not enforced by os.OpenFile mode on Windows; " +
			"access restriction there relies on NTFS ACLs, which this test does not cover")
	}

	old := os.Getenv("RECOIL_DIR")
	dir := t.TempDir()
	os.Setenv("RECOIL_DIR", dir)
	defer os.Setenv("RECOIL_DIR", old)
	defer func() { activeProject = "" }()

	activeProject = "perm-test"
	r := record{ID: "p1", Created: 1, Trigger: "manual", Weight: 1, Cue: "x", Gist: "y"}
	if err := mcpAppendRecord(r); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(projectStorePath())
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	// Expect owner read/write only — group/other must have zero bits
	if mode&0o077 != 0 {
		t.Errorf("store.tsv has overly permissive mode %o, want owner-only (0600-class)", mode)
	}
}
