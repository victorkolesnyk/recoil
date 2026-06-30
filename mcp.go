// mcp.go — MCP (Model Context Protocol) server for recoil.
//
// Run with: recoil serve --mcp
//
// Privacy guarantee: ALL data stays on the local machine. No network calls,
// no telemetry, no cloud sync. The only I/O is stdin/stdout (JSON-RPC to the
// MCP client) and reads/writes to the local TSV store on disk.
//
// This exposes five tools to any MCP-compatible client (e.g. Claude Desktop):
//   - recoil_project  — set or query the active project (isolates memory per project)
//   - recoil_recall   — surface lessons matching a situation
//   - recoil_encode   — record a new lesson with severity classification
//   - recoil_guard    — warn if about to repeat a known-bad change
//   - recoil_analyse  — analyse error patterns and mastery progress
//
// The server speaks JSON-RPC 2.0 over stdio (one JSON object per line),
// which is exactly what the MCP specification requires. No external
// dependencies — stdlib only, consistent with the rest of recoil.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// activeProject holds the current project name for this MCP server session.
// Empty string means "use RECOIL_DIR as-is" (default behaviour, backward compatible).
// Set via recoil_project tool. Scoped to the process — each Claude Desktop session
// gets its own server process, so projects are naturally session-isolated.
var activeProject = ""

// --- Security constants ---

const (
	maxProjectNameLen = 64    // prevent absurdly long names
	maxGistLen        = 2000  // prevent store bloat / memory exhaustion
	maxCueLen         = 500   // cue is tokenized — long cues waste cycles
	maxSituationLen   = 2000  // recall/guard input limit
	maxRequestBytes   = 64 * 1024 // 64 KB per JSON-RPC line — prevents DoS
)

// --- Severity system ---
//
// Severity classifies how serious an error is and drives weight + decay behaviour.
//
//	fatal   — broke the project, data loss, security issue. Weight=4, no decay.
//	blocker — stopped work completely. Weight=3, no decay.
//	lesson  — understood something new. Weight=2, normal decay.
//	pattern — same mistake twice: systemic, not accidental. Weight=4, no decay.
//
// "pattern" is auto-assigned when the same cue tokens fire an existing record
// a second time — the encode handler detects this and upgrades severity.

const (
	severityFatal   = "fatal"
	severityBlocker = "blocker"
	severityLesson  = "lesson"
	severityPattern = "pattern"
)

var severityWeights = map[string]float64{
	severityFatal:   4.0,
	severityBlocker: 3.0,
	severityLesson:  2.0,
	severityPattern: 4.0,
}

// noDecaySeverities — these severities never fade regardless of time.
var noDecaySeverities = map[string]bool{
	severityFatal:   true,
	severityBlocker: true,
	severityPattern: true,
}

// severityFor returns a valid severity or "lesson" as default.
func severityFor(s string) string {
	switch s {
	case severityFatal, severityBlocker, severityLesson, severityPattern:
		return s
	default:
		return severityLesson
	}
}

// weightForSeverity returns the effective weight for a severity+trigger combo.
// Severity takes precedence when set; falls back to trigger weight.
func weightForSeverity(sev, trigger string) float64 {
	if w, ok := severityWeights[sev]; ok {
		return w
	}
	return weightFor(trigger, -1)
}

// detectPattern checks whether an incoming cue significantly overlaps with
// an existing record (≥3 shared tokens). If so, returns the matching record
// index so the caller can mark it as a pattern.
func detectPattern(recs []record, cueTokens map[string]bool) (int, bool) {
	const patternOverlapThreshold = 3
	for i, r := range recs {
		overlap := 0
		for _, t := range strings.Fields(r.Cue) {
			if cueTokens[t] {
				overlap++
			}
		}
		if overlap >= patternOverlapThreshold {
			return i, true
		}
	}
	return -1, false
}

// safeProjectName validates a project name:
// only lowercase letters, digits, hyphens, underscores; no path traversal.
// Returns cleaned name and error message (empty = ok).
func safeProjectName(name string) (string, string) {
	if name == "" {
		return "", "project name cannot be empty"
	}
	if len(name) > maxProjectNameLen {
		return "", fmt.Sprintf("project name too long (max %d characters)", maxProjectNameLen)
	}
	// Reject any path separators or dots — prevents directory traversal
	if strings.ContainsAny(name, `/\. `) {
		return "", "project name may not contain slashes, dots, or spaces"
	}
	clean := strings.ToLower(name)
	for _, c := range clean {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return "", "project name may only contain letters, digits, hyphens, underscores"
		}
	}
	// Final check: cleaned path must not escape base directory
	base := storeDir()
	candidate := filepath.Join(base, clean)
	rel, err := filepath.Rel(base, candidate)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "invalid project name (path traversal detected)"
	}
	return clean, ""
}

// truncate cuts s to maxLen bytes, respecting UTF-8 boundaries.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Walk back to a valid UTF-8 boundary
	for !utf8.ValidString(s[:maxLen]) {
		maxLen--
	}
	return s[:maxLen] + "…"
}

// sanitiseText removes control characters (except newlines/tabs) from user input
// before storing — prevents TSV corruption and terminal injection.
func sanitiseText(s string) string {
	var b strings.Builder
	for _, r := range s {
		// Allow printable, tab, newline; drop other control chars
		if r >= 0x20 || r == '\t' || r == '\n' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// projectStoreDir returns the store directory for the active project.
// If no project is set — falls back to the standard storeDir() from main.go.
// Layout:  <RECOIL_DIR>/<project>/   when project is set
//          <RECOIL_DIR>/             when no project (default)
func projectStoreDir() string {
	base := storeDir()
	if activeProject == "" {
		return base
	}
	return filepath.Join(base, activeProject)
}

// projectStorePath returns store.tsv inside the active project directory.
func projectStorePath() string {
	return filepath.Join(projectStoreDir(), "store.tsv")
}

// listProjects returns all project names found under RECOIL_DIR.
// A project is any subdirectory that contains store.tsv.
func listProjects() ([]string, error) {
	base := storeDir()
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var projects []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only include directories that pass the name safety check
		if _, errMsg := safeProjectName(e.Name()); errMsg != "" {
			continue
		}
		tsv := filepath.Join(base, e.Name(), "store.tsv")
		if _, err := os.Stat(tsv); err == nil {
			projects = append(projects, e.Name())
		}
	}
	return projects, nil
}

// --- JSON-RPC 2.0 types ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- MCP protocol types ---

type mcpToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- Project-aware store access (MCP layer overrides path from main.go) ---

// mcpLoadRecords loads records from the active project store.
func mcpLoadRecords() ([]record, error) {
	f, err := os.Open(projectStorePath())
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

// mcpAppendRecord appends a record to the active project store,
// creating the directory if needed.
func mcpAppendRecord(r record) error {
	if err := os.MkdirAll(projectStoreDir(), 0o700); err != nil { // 0700 = owner only
		return err
	}
	f, err := os.OpenFile(projectStorePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // 0600 = owner read/write only
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, r.tsv())
	return err
}

// mcpSaveRecords rewrites the active project store atomically (used by reinforce).
func mcpSaveRecords(recs []record) error {
	if err := os.MkdirAll(projectStoreDir(), 0o700); err != nil {
		return err
	}
	tmp := projectStorePath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, rec := range recs {
		fmt.Fprintln(w, rec.tsv())
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, projectStorePath())
}

// mcpReinforce bumps hit count and last-seen for fired records, project-aware.
func mcpReinforce(recs []record, fired []scored) {
	now := time.Now().Unix()
	for _, f := range fired {
		recs[f.idx].Hits++
		recs[f.idx].Last = now
	}
	if err := mcpSaveRecords(recs); err != nil {
		fmt.Fprintln(os.Stderr, "recoil mcp: reinforce:", err)
	}
}

// --- Tool definitions ---

var mcpTools = []mcpToolDef{
	{
		Name: "recoil_project",
		Description: "Set or query the active project for this session. " +
			"Each project has its own isolated memory store — lessons from one project " +
			"never appear in another. Always call this at the start of a session to " +
			"activate the correct memory context. Use action=list to see existing projects. " +
			"🔒 All data is stored locally — nothing ever leaves this machine.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "What to do: 'set' to activate a project, 'current' to see active project, 'list' to see all projects.",
					"enum":        []string{"set", "current", "list"},
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Project name (required for action=set). Use short slug-like names: 'mcp-server', 'renovateam', 'zhyva-apteka'. Letters, digits, hyphens, underscores only.",
				},
			},
			"required": []string{"action"},
		},
	},
	{
		Name:        "recoil_recall",
		Description: "Surface lessons from recoil memory that match the current situation. Use this before writing code, running commands, or making changes — especially in areas that have caused problems before. 🔒 Local only.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"situation": map[string]any{
					"type":        "string",
					"description": "Describe what you are about to do: filenames, error text, keywords, technologies involved.",
				},
				"top": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lessons to return (default: 3).",
					"default":     3,
				},
			},
			"required": []string{"situation"},
		},
	},
	{
		Name:        "recoil_encode",
		Description: "Record a new lesson into recoil memory with severity classification. Use this when you discover a mistake, fix a bug, or learn something that should not be forgotten next time. Fatal/blocker/pattern severities never decay. 🔒 Stored locally only — never sent anywhere.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"gist": map[string]any{
					"type":        "string",
					"description": "The lesson itself — what went wrong and what to do instead.",
				},
				"cue": map[string]any{
					"type":        "string",
					"description": "Keywords describing the situation: filenames, error messages, technologies, function names.",
				},
				"trigger": map[string]any{
					"type":        "string",
					"description": "What kind of event this is. One of: correction, revert, test-fail, error, manual.",
					"default":     "manual",
					"enum":        []string{"correction", "revert", "test-fail", "error", "manual"},
				},
				"severity": map[string]any{
					"type":        "string",
					"description": "How serious is this error? fatal=data loss/security; blocker=stopped work; lesson=new understanding; pattern=same mistake again (auto-detected if omitted).",
					"default":     "lesson",
					"enum":        []string{"fatal", "blocker", "lesson", "pattern"},
				},
			},
			"required": []string{"gist", "cue"},
		},
	},
	{
		Name:        "recoil_guard",
		Description: "Check whether the current situation matches any known-bad changes recorded in recoil memory. Use this before touching files that have caused problems before. 🔒 Local only.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"situation": map[string]any{
					"type":        "string",
					"description": "Describe what you are about to change: filenames, technologies, what the change does.",
				},
				"min_overlap": map[string]any{
					"type":        "integer",
					"description": "Minimum number of cue tokens that must match to trigger a warning (default: 2).",
					"default":     2,
				},
			},
			"required": []string{"situation"},
		},
	},
	{
		Name: "recoil_analyse",
		Description: "Analyse error patterns and mastery progress for the active project. " +
			"Shows: most frequent errors (blind spots), patterns (systemic issues), " +
			"mastered lessons (no longer recurring), severity distribution, and growth trajectory. " +
			"Use this periodically to understand where you are on the path to mastery. " +
			"🔒 Local analysis only — nothing leaves this machine.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"focus": map[string]any{
					"type":        "string",
					"description": "What to show: 'full' for complete report, 'patterns' for systemic issues only, 'mastered' for lessons no longer recurring, 'blind-spots' for most frequent errors.",
					"enum":        []string{"full", "patterns", "mastered", "blind-spots"},
					"default":     "full",
				},
			},
			"required": []string{},
		},
	},
}

// --- Tool handlers ---

func handleProject(params json.RawMessage) mcpToolResult {
	var p struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return errResult("invalid params: " + err.Error())
	}

	switch p.Action {
	case "set":
		clean, errMsg := safeProjectName(p.Name)
		if errMsg != "" {
			return errResult(errMsg)
		}
		activeProject = clean
		// Auto-create store directory with restrictive permissions
		if err := os.MkdirAll(projectStoreDir(), 0o700); err != nil {
			return errResult("could not create project directory: " + err.Error())
		}
		// Touch store.tsv if it doesn't exist yet
		if _, err := os.Stat(projectStorePath()); os.IsNotExist(err) {
			f, err := os.OpenFile(projectStorePath(), os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return errResult("could not initialise project store: " + err.Error())
			}
			f.Close()
		}
		return okResult(fmt.Sprintf(
			"✅ Active project: %s\n🔒 Store: %s\n🔒 All data is local — nothing leaves this machine.",
			activeProject, projectStorePath(),
		))

	case "current":
		if activeProject == "" {
			return okResult(fmt.Sprintf(
				"No project set. Using default store: %s\n\n"+
					"Tip: call recoil_project with action=set and a project name to isolate memory.\n"+
					"🔒 All data is local — nothing leaves this machine.",
				projectStorePath(),
			))
		}
		recs, _ := mcpLoadRecords()
		return okResult(fmt.Sprintf(
			"Active project: %s\n🔒 Store: %s\nLessons stored: %d\n🔒 All data is local — nothing leaves this machine.",
			activeProject, projectStorePath(), len(recs),
		))

	case "list":
		projects, err := listProjects()
		if err != nil {
			return errResult("could not list projects: " + err.Error())
		}
		if len(projects) == 0 {
			return okResult("No projects found. Use action=set to create one.\n🔒 All data is local — nothing leaves this machine.")
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d project(s):\n\n", len(projects)))
		for _, name := range projects {
			marker := "  "
			if name == activeProject {
				marker = "▶ "
			}
			tsv := filepath.Join(storeDir(), name, "store.tsv")
			count := 0
			if f, err := os.Open(tsv); err == nil {
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					if strings.TrimSpace(sc.Text()) != "" {
						count++
					}
				}
				f.Close()
			}
			sb.WriteString(fmt.Sprintf("%s%s  (%d lessons)\n", marker, name, count))
		}
		if activeProject == "" {
			sb.WriteString("\nNo project active. Use action=set to activate one.")
		} else {
			sb.WriteString(fmt.Sprintf("\nActive: %s", activeProject))
		}
		sb.WriteString("\n🔒 All data is local — nothing leaves this machine.")
		return okResult(sb.String())

	default:
		return errResult("action must be one of: set, current, list")
	}
}

func handleRecall(params json.RawMessage) mcpToolResult {
	var p struct {
		Situation string `json:"situation"`
		Top       int    `json:"top"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return errResult("invalid params: " + err.Error())
	}
	if p.Situation == "" {
		return errResult("situation is required")
	}
	// Sanitise and truncate input
	p.Situation = truncate(sanitiseText(p.Situation), maxSituationLen)
	if p.Top <= 0 || p.Top > 20 {
		p.Top = 3
	}

	recs, err := mcpLoadRecords()
	if err != nil {
		return errResult("could not load store: " + err.Error())
	}

	situation := tokenize(p.Situation)
	fired := scoreRecords(recs, situation, time.Now().Unix(), halfLifeDays())

	if len(fired) == 0 {
		return okResult("No matching lessons found for this situation.")
	}

	n := p.Top
	if n > len(fired) {
		n = len(fired)
	}

	var sb strings.Builder
	proj := activeProject
	if proj == "" {
		proj = "default"
	}
	sb.WriteString(fmt.Sprintf("Found %d matching lesson(s) [project: %s]:\n\n", n, proj))
	for k := 0; k < n; k++ {
		f := fired[k]
		r := recs[f.idx]
		sb.WriteString(fmt.Sprintf("**Lesson %d** [%s | weight=%.1f | hits=%d]\n",
			k+1, r.Trigger, r.Weight, r.Hits))
		sb.WriteString(fmt.Sprintf("%s\n", r.Gist))
		sb.WriteString(fmt.Sprintf("*Matched on: %s*\n\n", strings.Join(f.matched, ", ")))
	}

	mcpReinforce(recs, fired[:n])
	return okResult(sb.String())
}

func handleEncode(params json.RawMessage) mcpToolResult {
	var p struct {
		Gist     string `json:"gist"`
		Cue      string `json:"cue"`
		Trigger  string `json:"trigger"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return errResult("invalid params: " + err.Error())
	}
	if p.Gist == "" || p.Cue == "" {
		return errResult("gist and cue are required")
	}

	// Sanitise and truncate all user input before storing
	p.Gist = truncate(sanitiseText(p.Gist), maxGistLen)
	p.Cue = truncate(sanitiseText(p.Cue), maxCueLen)

	// Validate trigger
	validTriggers := map[string]bool{
		"correction": true, "revert": true,
		"test-fail": true, "error": true, "manual": true,
	}
	if p.Trigger == "" {
		p.Trigger = "manual"
	}
	if !validTriggers[p.Trigger] {
		return errResult("trigger must be one of: correction, revert, test-fail, error, manual")
	}

	// Validate severity
	sev := severityFor(p.Severity)

	// Load existing records for pattern detection
	recs, err := mcpLoadRecords()
	if err != nil {
		return errResult("could not load store: " + err.Error())
	}

	cueTokens := tokenize(p.Cue)

	// Auto-detect pattern: if incoming cue overlaps significantly with an
	// existing record — this is not a new mistake, it's a recurring one.
	patternNote := ""
	if p.Severity != severityPattern && p.Severity != severityFatal {
		if idx, isPattern := detectPattern(recs, cueTokens); isPattern {
			sev = severityPattern
			existing := recs[idx]
			patternNote = fmt.Sprintf(
				"\n⚠️  PATTERN DETECTED — this overlaps with an existing lesson (hits=%d):\n   \"%s\"\n   Severity upgraded to PATTERN automatically.",
				existing.Hits, existing.Gist,
			)
			// Upgrade weight of the existing record too
			recs[idx].Weight = severityWeights[severityPattern]
			recs[idx].Hits++
			recs[idx].Last = time.Now().Unix()
			if saveErr := mcpSaveRecords(recs); saveErr != nil {
				fmt.Fprintln(os.Stderr, "recoil mcp: pattern upgrade save:", saveErr)
			}
		}
	}

	now := time.Now().Unix()
	r := record{
		ID:      fmt.Sprintf("r%d", time.Now().UnixNano()),
		Created: now,
		Trigger: p.Trigger,
		Weight:  weightForSeverity(sev, p.Trigger),
		Hits:    0,
		Last:    now,
		Cue:     strings.Join(sortedTokens(cueTokens), " "),
		Gist:    fmt.Sprintf("[%s] %s", sev, p.Gist),
	}

	if err := mcpAppendRecord(r); err != nil {
		return errResult("could not save lesson: " + err.Error())
	}

	proj := activeProject
	if proj == "" {
		proj = "default"
	}

	severityEmoji := map[string]string{
		severityFatal:   "🔴",
		severityBlocker: "🟠",
		severityLesson:  "🟡",
		severityPattern: "🔁",
	}
	emoji := severityEmoji[sev]

	noDecayNote := ""
	if noDecaySeverities[sev] {
		noDecayNote = " [permanent — will not decay]"
	}

	return okResult(fmt.Sprintf(
		"%s Lesson recorded [project: %s | severity: %s%s | weight: %.1f]\n%s%s\n🔒 Stored locally only.",
		emoji, proj, sev, noDecayNote, r.Weight, p.Gist, patternNote,
	))
}

func handleGuard(params json.RawMessage) mcpToolResult {
	var p struct {
		Situation  string `json:"situation"`
		MinOverlap int    `json:"min_overlap"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return errResult("invalid params: " + err.Error())
	}
	if p.Situation == "" {
		return errResult("situation is required")
	}

	// Sanitise and truncate input
	p.Situation = truncate(sanitiseText(p.Situation), maxSituationLen)
	if p.MinOverlap <= 0 || p.MinOverlap > 10 {
		p.MinOverlap = defaultGuardOverlap
	}

	recs, err := mcpLoadRecords()
	if err != nil {
		return errResult("could not load store: " + err.Error())
	}

	situation := tokenize(p.Situation)
	warnings := guardMatches(recs, situation, time.Now().Unix(), halfLifeDays(), defaultGuardMin, p.MinOverlap)

	if len(warnings) == 0 {
		return okResult("No known issues found for this situation. Looks safe to proceed.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⚠️  %d warning(s) — you've been burned here before:\n\n", len(warnings)))
	for i, r := range warnings {
		sb.WriteString(fmt.Sprintf("%d. [%s | weight=%.1f | hits=%d]\n   %s\n\n",
			i+1, r.Trigger, r.Weight, r.Hits, r.Gist))
	}

	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: sb.String()}},
		IsError: false,
	}
}

func handleAnalyse(params json.RawMessage) mcpToolResult {
	var p struct {
		Focus string `json:"focus"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		// params can be empty — that's fine, default to full
		p.Focus = "full"
	}
	if p.Focus == "" {
		p.Focus = "full"
	}
	validFocus := map[string]bool{
		"full": true, "patterns": true, "mastered": true, "blind-spots": true,
	}
	if !validFocus[p.Focus] {
		p.Focus = "full"
	}

	recs, err := mcpLoadRecords()
	if err != nil {
		return errResult("could not load store: " + err.Error())
	}
	if len(recs) == 0 {
		return okResult("No lessons recorded yet. Start working and recording errors — the analysis will grow with your experience.")
	}

	now := time.Now().Unix()
	hl := halfLifeDays()

	// --- Classify records ---
	type recInfo struct {
		r        record
		sev      string     // extracted from gist prefix [fatal] etc.
		str      float64    // current strength
		ageDays  float64
		mastered bool       // strong enough to recall but hits>0 and strength still high = active
		// mastered = hits > 0 AND no recent recall (last > 14 days ago) — lesson absorbed
	}

	var infos []recInfo
	sevCount := map[string]int{}
	trigCount := map[string]int{}

	for _, r := range recs {
		sev := severityLesson
		gist := r.Gist
		// Extract severity prefix if present: "[pattern] ..."
		if strings.HasPrefix(gist, "[") {
			if end := strings.Index(gist, "] "); end > 0 {
				candidate := gist[1:end]
				if _, ok := severityWeights[candidate]; ok {
					sev = candidate
				}
			}
		}
		str := strength(r, now, hl)
		ageDays := float64(now-r.Last) / 86400.0

		// Mastered: recorded at least once (hits>0), not recalled recently (>14 days),
		// but still above the floor — meaning it hasn't decayed to irrelevance yet.
		mastered := r.Hits > 0 && ageDays > 14 && str > defaultFloor*2

		infos = append(infos, recInfo{r, sev, str, ageDays, mastered})
		sevCount[sev]++
		trigCount[r.Trigger]++
	}

	// --- Sort by hits descending for blind-spots ---
	type hitsRank struct {
		idx  int
		hits int
	}
	ranked := make([]hitsRank, len(infos))
	for i, inf := range infos {
		ranked[i] = hitsRank{i, inf.r.Hits}
	}
	// Simple insertion sort (small N)
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].hits > ranked[j-1].hits; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}

	proj := activeProject
	if proj == "" {
		proj = "default"
	}

	var sb strings.Builder

	// Helper to write a section header
	section := func(title string) {
		sb.WriteString(fmt.Sprintf("\n## %s\n\n", title))
	}

	// --- Header ---
	sb.WriteString(fmt.Sprintf("# 📊 Recoil Analysis — project: %s\n", proj))
	sb.WriteString(fmt.Sprintf("Total lessons: %d | Analysis: %s\n", len(recs), p.Focus))

	// --- Severity distribution (always shown) ---
	if p.Focus == "full" || p.Focus == "blind-spots" || p.Focus == "patterns" {
		section("Severity Distribution")
		order := []string{severityFatal, severityBlocker, severityPattern, severityLesson}
		emojis := map[string]string{
			severityFatal: "🔴", severityBlocker: "🟠",
			severityPattern: "🔁", severityLesson: "🟡",
		}
		for _, s := range order {
			if n := sevCount[s]; n > 0 {
				sb.WriteString(fmt.Sprintf("  %s %-8s %d lesson(s)\n", emojis[s], s, n))
			}
		}
	}

	// --- Patterns (systemic issues) ---
	if p.Focus == "full" || p.Focus == "patterns" {
		section("🔁 Systemic Patterns (same mistake recurring)")
		found := false
		for _, inf := range infos {
			if inf.sev == severityPattern || inf.r.Hits >= 2 {
				sb.WriteString(fmt.Sprintf("  hits=%-3d  str=%.2f  %s\n", inf.r.Hits, inf.str, inf.r.Gist))
				found = true
			}
		}
		if !found {
			sb.WriteString("  None detected yet — good sign, or not enough data.\n")
		}
	}

	// --- Blind spots (most hit errors) ---
	if p.Focus == "full" || p.Focus == "blind-spots" {
		section("🎯 Blind Spots (most frequent errors)")
		limit := 5
		shown := 0
		for _, rank := range ranked {
			if shown >= limit {
				break
			}
			inf := infos[rank.idx]
			if inf.r.Hits == 0 {
				break // no repeats — no blind spots yet
			}
			sb.WriteString(fmt.Sprintf("  %dx  [%s]  %s\n", inf.r.Hits, inf.sev, inf.r.Gist))
			shown++
		}
		if shown == 0 {
			sb.WriteString("  No repeated errors yet. Keep working — patterns will emerge.\n")
		}
	}

	// --- Mastered lessons ---
	if p.Focus == "full" || p.Focus == "mastered" {
		section("✅ Mastered (absorbed, not recurring)")
		masteredCount := 0
		for _, inf := range infos {
			if inf.mastered {
				sb.WriteString(fmt.Sprintf("  [%s] %s  (last seen %.0f days ago)\n",
					inf.sev, inf.r.Gist, inf.ageDays))
				masteredCount++
			}
		}
		if masteredCount == 0 {
			sb.WriteString("  None yet — lessons become 'mastered' after 14+ days without recurrence.\n")
		}
	}

	// --- Fatal / Blocker alerts (always shown if any exist) ---
	fatalCount := sevCount[severityFatal] + sevCount[severityBlocker]
	if fatalCount > 0 && (p.Focus == "full" || p.Focus == "blind-spots") {
		section("🔴 Critical — Never Ignore These")
		for _, inf := range infos {
			if inf.sev == severityFatal || inf.sev == severityBlocker {
				sb.WriteString(fmt.Sprintf("  [%s] str=%.2f  %s\n", inf.sev, inf.str, inf.r.Gist))
			}
		}
	}

	// --- Growth summary (full only) ---
	if p.Focus == "full" {
		section("📈 Growth Summary")
		mastered := 0
		active := 0
		for _, inf := range infos {
			if inf.mastered {
				mastered++
			} else {
				active++
			}
		}
		total := len(recs)
		pct := 0
		if total > 0 {
			pct = mastered * 100 / total
		}
		sb.WriteString(fmt.Sprintf("  Total recorded:  %d\n", total))
		sb.WriteString(fmt.Sprintf("  Still active:    %d (watch these)\n", active))
		sb.WriteString(fmt.Sprintf("  Mastered:        %d (%d%% of total)\n", mastered, pct))
		sb.WriteString(fmt.Sprintf("  Patterns found:  %d (systemic issues — prioritise)\n", sevCount[severityPattern]))
		if total >= 10 {
			sb.WriteString("\n  💡 You have enough data to identify systemic issues.\n")
			sb.WriteString("     Focus on 'patterns' — fixing one systemic issue beats fixing ten individual ones.\n")
		} else {
			remaining := 10 - total
			sb.WriteString(fmt.Sprintf("\n  Keep recording. %d more lessons until pattern analysis becomes meaningful.\n", remaining))
		}
	}

	sb.WriteString("\n🔒 Analysis performed locally — no data left this machine.")
	return okResult(sb.String())
}

// --- Result helpers ---

func okResult(text string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: text}}}
}

func errResult(text string) mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: "Error: " + text}},
		IsError: true,
	}
}

// --- MCP server loop ---

func cmdServeMCP() {
	// LimitedReader prevents a single malformed request from exhausting memory
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, maxRequestBytes), maxRequestBytes)
	encoder := json.NewEncoder(os.Stdout)

	fmt.Fprintln(os.Stderr, "recoil MCP server started (stdio transport)")
	fmt.Fprintf(os.Stderr, "recoil: base store dir: %s\n", storeDir())
	fmt.Fprintln(os.Stderr, "recoil: 🔒 privacy mode — no network, no telemetry, local data only")

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = encoder.Encode(rpcResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error"},
				// Note: we do NOT echo back the malformed input in the error message
				// to avoid reflecting potentially malicious content
			})
			continue
		}

		resp := dispatch(req)
		// Skip empty responses (e.g. "initialized" notification)
		if resp.JSONRPC == "" {
			continue
		}
		if err := encoder.Encode(resp); err != nil {
			fmt.Fprintln(os.Stderr, "recoil mcp: encode error:", err)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "recoil mcp: stdin error:", err)
		os.Exit(1)
	}
}

func dispatch(req rpcRequest) rpcResponse {
	base := rpcResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {

	case "initialize":
		base.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "recoil",
				"version": version,
			},
		}

	case "initialized":
		return rpcResponse{} // notification — no response

	case "tools/list":
		base.Result = map[string]any{"tools": mcpTools}

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			base.Error = &rpcError{Code: -32602, Message: "invalid params"}
			return base
		}
		// Whitelist — only known tool names accepted
		var result mcpToolResult
		switch p.Name {
		case "recoil_project":
			result = handleProject(p.Arguments)
		case "recoil_recall":
			result = handleRecall(p.Arguments)
		case "recoil_encode":
			result = handleEncode(p.Arguments)
		case "recoil_guard":
			result = handleGuard(p.Arguments)
		case "recoil_analyse":
			result = handleAnalyse(p.Arguments)
		default:
			base.Error = &rpcError{Code: -32601, Message: "unknown tool"}
			return base
		}
		base.Result = result

	case "shutdown":
		base.Result = nil
		fmt.Fprintln(os.Stderr, "recoil MCP server: shutdown")

	default:
		base.Error = &rpcError{Code: -32601, Message: "method not found"}
	}

	return base
}

//
// Run with: recoil serve --mcp
//
