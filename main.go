// Command recoil is a local-first memory for AI coding agents.
//
// recoil records only what surprised the development loop — a failed command,
// a revert, a correction — as a cue (the situation it happened in) plus a gist
// (the lesson), and fires those gists back when the current situation matches.
// Matching is deterministic keyword cue-overlap: no embeddings, no model, no
// network. One static binary, one plain-text store.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		cmdInit(os.Args[2:])
	case "encode":
		cmdEncode(os.Args[2:])
	case "recall":
		cmdRecall(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("recoil", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `recoil `+version+` — local-first memory for AI coding agents

usage:
  recoil init
  recoil encode --gist "<lesson>" --cue "<tokens>" [--trigger T] [--weight N]
  recoil recall [--situation "<text>"] [--files a,b] [--top N]   (also reads stdin)
  recoil list
  recoil version

triggers (default weight): correction=3  revert=2.5  test-fail=2  error=1.5  manual=1
store: $RECOIL_DIR/store.tsv  (default ./.recoil/store.tsv)
`)
}

// --- store location ---

func storeDir() string {
	if d := os.Getenv("RECOIL_DIR"); d != "" {
		return d
	}
	return ".recoil"
}

func storePath() string { return filepath.Join(storeDir(), "store.tsv") }

// --- record model ---

// record is one remembered flinch: a cue (the situation that triggered it) plus
// a gist (the lesson), with salience bookkeeping for ranking and (later) decay.
type record struct {
	ID      string
	Created int64
	Trigger string
	Weight  float64
	Hits    int
	Last    int64
	Cue     string // space-separated, lowercased tokens
	Gist    string
}

func clean(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func (r record) tsv() string {
	return strings.Join([]string{
		r.ID,
		fmt.Sprintf("%d", r.Created),
		r.Trigger,
		fmt.Sprintf("%g", r.Weight),
		fmt.Sprintf("%d", r.Hits),
		fmt.Sprintf("%d", r.Last),
		clean(r.Cue),
		clean(r.Gist),
	}, "\t")
}

func parseRecord(line string) (record, bool) {
	f := strings.Split(line, "\t")
	if len(f) != 8 {
		return record{}, false
	}
	var r record
	r.ID = f[0]
	fmt.Sscanf(f[1], "%d", &r.Created)
	r.Trigger = f[2]
	fmt.Sscanf(f[3], "%g", &r.Weight)
	fmt.Sscanf(f[4], "%d", &r.Hits)
	fmt.Sscanf(f[5], "%d", &r.Last)
	r.Cue = f[6]
	r.Gist = f[7]
	return r, true
}

func loadRecords() ([]record, error) {
	f, err := os.Open(storePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var recs []record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if r, ok := parseRecord(line); ok {
			recs = append(recs, r)
		}
	}
	return recs, sc.Err()
}

func saveRecords(recs []record) error {
	if err := os.MkdirAll(storeDir(), 0o755); err != nil {
		return err
	}
	tmp := storePath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range recs {
		fmt.Fprintln(w, r.tsv())
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, storePath())
}

func appendRecord(r record) error {
	if err := os.MkdirAll(storeDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(storePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, r.tsv())
	return err
}

// --- tokenization: deterministic, no embeddings ---

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "to": true,
	"of": true, "in": true, "on": true, "for": true, "is": true, "it": true,
	"this": true, "that": true, "with": true, "was": true, "at": true, "by": true,
	"i": true, "im": true, "my": true, "we": true, "you": true, "should": true,
}

// tokenize lowercases and splits on any non-alphanumeric run, dropping stopwords
// and single characters. "build-index.mjs" -> {build, index, mjs}.
func tokenize(s string) map[string]bool {
	set := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		t := strings.ToLower(cur.String())
		cur.Reset()
		if len(t) < 2 || stopwords[t] {
			return
		}
		set[t] = true
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			cur.WriteRune(c)
		} else {
			flush()
		}
	}
	flush()
	return set
}

func sortedTokens(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// --- commands ---

func cmdInit(args []string) {
	if err := os.MkdirAll(storeDir(), 0o755); err != nil {
		die(err)
	}
	if _, err := os.Stat(storePath()); os.IsNotExist(err) {
		if err := saveRecords(nil); err != nil {
			die(err)
		}
	}
	fmt.Printf("recoil: store ready at %s\n", storePath())
}

var triggerWeights = map[string]float64{
	"correction": 3.0,
	"revert":     2.5,
	"test-fail":  2.0,
	"error":      1.5,
	"manual":     1.0,
}

func cmdEncode(args []string) {
	fs := flag.NewFlagSet("encode", flag.ExitOnError)
	gist := fs.String("gist", "", "the lesson to remember (required)")
	cue := fs.String("cue", "", "situation tokens: files, error text, keywords (required)")
	trigger := fs.String("trigger", "manual", "what surprised the loop: correction|revert|test-fail|error|manual")
	weight := fs.Float64("weight", -1, "salience weight (default: by trigger)")
	fs.Parse(args)

	if *gist == "" || *cue == "" {
		fmt.Fprintln(os.Stderr, "recoil encode: --gist and --cue are required")
		os.Exit(2)
	}
	w := *weight
	if w < 0 {
		if tw, ok := triggerWeights[*trigger]; ok {
			w = tw
		} else {
			w = 1.0
		}
	}
	now := time.Now().Unix()
	r := record{
		ID:      fmt.Sprintf("r%d", time.Now().UnixNano()),
		Created: now,
		Trigger: *trigger,
		Weight:  w,
		Hits:    0,
		Last:    now,
		Cue:     strings.Join(sortedTokens(tokenize(*cue)), " "),
		Gist:    *gist,
	}
	if err := appendRecord(r); err != nil {
		die(err)
	}
	fmt.Printf("recoil: remembered [%s w=%g] %s\n", r.Trigger, r.Weight, r.Gist)
}

func cmdRecall(args []string) {
	fs := flag.NewFlagSet("recall", flag.ExitOnError)
	situation := fs.String("situation", "", "describe the current situation")
	files := fs.String("files", "", "comma-separated files in play")
	top := fs.Int("top", 3, "max memories to fire")
	fs.Parse(args)

	var sb strings.Builder
	sb.WriteString(*situation)
	sb.WriteByte(' ')
	sb.WriteString(strings.ReplaceAll(*files, ",", " "))
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		if b, err := io.ReadAll(os.Stdin); err == nil {
			sb.WriteByte(' ')
			sb.Write(b)
		}
	}

	situationSet := tokenize(sb.String())
	if len(situationSet) == 0 {
		fmt.Fprintln(os.Stderr, "recoil recall: no situation given (use --situation, --files, or pipe text)")
		os.Exit(2)
	}

	recs, err := loadRecords()
	if err != nil {
		die(err)
	}

	type scored struct {
		idx     int
		score   float64
		matched []string
	}
	var fired []scored
	for i, r := range recs {
		cueSet := tokenize(r.Cue)
		var matched []string
		for t := range cueSet {
			if situationSet[t] {
				matched = append(matched, t)
			}
		}
		if len(matched) == 0 {
			continue
		}
		sort.Strings(matched)
		// salience grows with the encoded surprise weight and re-fire count
		salience := r.Weight * (1 + math.Log(1+float64(r.Hits)))
		fired = append(fired, scored{i, float64(len(matched)) * salience, matched})
	}
	if len(fired) == 0 {
		fmt.Fprintln(os.Stderr, "recoil: nothing fired (no cue overlap)")
		return
	}
	sort.SliceStable(fired, func(a, b int) bool { return fired[a].score > fired[b].score })

	n := *top
	if n > len(fired) {
		n = len(fired)
	}
	now := time.Now().Unix()
	for k := 0; k < n; k++ {
		f := fired[k]
		r := recs[f.idx]
		fmt.Printf(">> %s\n   [%s w=%g hits=%d] matched: %s\n",
			r.Gist, r.Trigger, r.Weight, r.Hits, strings.Join(f.matched, " "))
		recs[f.idx].Hits++  // recall reinforces — re-fired memories persist longer
		recs[f.idx].Last = now
	}
	if err := saveRecords(recs); err != nil {
		die(err)
	}
}

func cmdList(args []string) {
	recs, err := loadRecords()
	if err != nil {
		die(err)
	}
	if len(recs) == 0 {
		fmt.Println("recoil: empty")
		return
	}
	for _, r := range recs {
		fmt.Printf("[%s w=%g hits=%d] %s\n   cue: %s\n", r.Trigger, r.Weight, r.Hits, r.Gist, r.Cue)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "recoil:", err)
	os.Exit(1)
}
