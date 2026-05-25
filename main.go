package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

//go:embed web/templates/*.html web/static
var webFS embed.FS

var pageTemplates map[string]*template.Template

var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM, // tables, strikethrough, task lists, linkify
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
	),
	goldmark.WithRendererOptions(
		gmhtml.WithUnsafe(), // pass through raw HTML in source
	),
)

func main() {
	port := flag.Int("port", 8080, "HTTP port")
	claudeDir := flag.String("dir", filepath.Join(os.Getenv("HOME"), ".claude"), "Claude data directory")
	flag.Parse()

	store := NewStore(*claudeDir)

	funcMap := template.FuncMap{
		"formatTime":     formatTime,
		"formatDate":     formatDate,
		"decodeProject":  decodeProjectName,
		"add":            func(a, b int) int { return a + b },
		"truncate":       truncate,
		"humanizeTokens": humanizeTokens,
		"renderMarkdown": renderMarkdown,
		"safeHTML":       func(s string) template.HTML { return template.HTML(s) },
		"formatDuration": formatDuration,
		"firstUserText":  firstUserText,
		"msgHasContent":  msgHasContent,
	}

	pages := []string{"dashboard.html", "projects.html", "project_detail.html", "session.html", "search.html"}
	pageTemplates = make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New("").Funcs(funcMap).ParseFS(webFS,
			"web/templates/base.html",
			"web/templates/"+page,
		)
		if err != nil {
			log.Fatalf("template parse %s: %v", page, err)
		}
		pageTemplates[page] = t
	}

	staticFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal("fs.Sub:", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		handleDashboard(w, r, store)
	})
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		handleProjects(w, r, store)
	})
	mux.HandleFunc("GET /projects/{name}", func(w http.ResponseWriter, r *http.Request) {
		handleProjectDetail(w, r, store)
	})
	mux.HandleFunc("GET /sessions/{project}/{session}", func(w http.ResponseWriter, r *http.Request) {
		handleSession(w, r, store)
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		handleSearch(w, r, store)
	})

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Claude Reader running at http://localhost%s\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// ─── Data Types ────────────────────────────────────────────────────────────────

type RawEntry struct {
	Type       string          `json:"type"`
	UUID       string          `json:"uuid"`
	ParentUUID *string         `json:"parentUuid"`
	IsSidechain bool           `json:"isSidechain"`
	Timestamp  string          `json:"timestamp"`
	Message    json.RawMessage `json:"message"`
	SessionID  string          `json:"sessionId"`
}

type RawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
	Usage   *Usage          `json:"usage"`
}

type Usage struct {
	InputTokens            int `json:"input_tokens"`
	OutputTokens           int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens   int `json:"cache_read_input_tokens"`
}

type ContentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

type Message struct {
	UUID      string
	Role      string
	Timestamp time.Time
	Blocks    []ContentBlock
	Model     string
	Usage     *Usage
	Sidechain bool
}

type Session struct {
	ID          string
	ProjectName string
	StartTime   time.Time
	EndTime     time.Time
	Messages    []Message
	TurnCount   int
	TotalTokens int
	InputTokens int
	OutputTokens int
}

type Project struct {
	Name     string
	DirName  string
	Sessions []Session
}

// ─── Store ─────────────────────────────────────────────────────────────────────

type Store struct {
	claudeDir string
	projects  []Project
	loaded    bool
}

func NewStore(dir string) *Store {
	return &Store{claudeDir: dir}
}

func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	projectsDir := filepath.Join(s.claudeDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		proj := Project{
			Name:    decodeProjectName(e.Name()),
			DirName: e.Name(),
		}
		projPath := filepath.Join(projectsDir, e.Name())
		files, _ := os.ReadDir(projPath)
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			sessionID := strings.TrimSuffix(f.Name(), ".jsonl")
			sess := parseSession(filepath.Join(projPath, f.Name()), sessionID, e.Name())
			proj.Sessions = append(proj.Sessions, sess)
		}
		sort.Slice(proj.Sessions, func(i, j int) bool {
			return proj.Sessions[i].StartTime.After(proj.Sessions[j].StartTime)
		})
		if len(proj.Sessions) > 0 {
			s.projects = append(s.projects, proj)
		}
	}
	sort.Slice(s.projects, func(i, j int) bool {
		var ti, tj time.Time
		if len(s.projects[i].Sessions) > 0 {
			ti = s.projects[i].Sessions[0].StartTime
		}
		if len(s.projects[j].Sessions) > 0 {
			tj = s.projects[j].Sessions[0].StartTime
		}
		return ti.After(tj)
	})
}

func (s *Store) Projects() []Project {
	s.load()
	return s.projects
}

func (s *Store) GetProject(dirName string) *Project {
	s.load()
	for i := range s.projects {
		if s.projects[i].DirName == dirName {
			return &s.projects[i]
		}
	}
	return nil
}

func (s *Store) GetSession(projectDirName, sessionID string) *Session {
	proj := s.GetProject(projectDirName)
	if proj == nil {
		return nil
	}
	for i := range proj.Sessions {
		if proj.Sessions[i].ID == sessionID {
			return &proj.Sessions[i]
		}
	}
	return nil
}

type Stats struct {
	TotalProjects  int
	TotalSessions  int
	TotalTurns     int
	TotalTokens    int
	InputTokens    int
	OutputTokens   int
}

func (s *Store) Stats() Stats {
	s.load()
	var st Stats
	st.TotalProjects = len(s.projects)
	for _, p := range s.projects {
		st.TotalSessions += len(p.Sessions)
		for _, sess := range p.Sessions {
			st.TotalTurns += sess.TurnCount
			st.TotalTokens += sess.TotalTokens
			st.InputTokens += sess.InputTokens
			st.OutputTokens += sess.OutputTokens
		}
	}
	return st
}

type SearchResult struct {
	Project     string
	ProjectDir  string
	SessionID   string
	SessionTime time.Time
	Snippet     string
}

type SearchFilter struct {
	Query      string
	ProjectDir string
	DateFrom   string
	DateTo     string
}

func (s *Store) Search(f SearchFilter) []SearchResult {
	s.load()
	query := strings.ToLower(f.Query)

	var fromTime, toTime time.Time
	if f.DateFrom != "" {
		fromTime, _ = time.Parse("2006-01-02", f.DateFrom)
	}
	if f.DateTo != "" {
		toTime, _ = time.Parse("2006-01-02", f.DateTo)
		toTime = toTime.Add(24*time.Hour - time.Second)
	}

	var results []SearchResult
	for _, p := range s.projects {
		if f.ProjectDir != "" && p.DirName != f.ProjectDir {
			continue
		}
		for _, sess := range p.Sessions {
			if !fromTime.IsZero() && sess.StartTime.Before(fromTime) {
				continue
			}
			if !toTime.IsZero() && sess.StartTime.After(toTime) {
				continue
			}
			if query == "" {
				// Date/project filter only — include session with first user text as snippet
				snippet := firstUserText(sess)
				results = append(results, SearchResult{
					Project:     p.Name,
					ProjectDir:  p.DirName,
					SessionID:   sess.ID,
					SessionTime: sess.StartTime,
					Snippet:     snippet,
				})
				continue
			}
			for _, msg := range sess.Messages {
				for _, b := range msg.Blocks {
					if b.Type == "text" && strings.Contains(strings.ToLower(b.Text), query) {
						snippet := extractSnippet(b.Text, query, 150)
						results = append(results, SearchResult{
							Project:     p.Name,
							ProjectDir:  p.DirName,
							SessionID:   sess.ID,
							SessionTime: sess.StartTime,
							Snippet:     snippet,
						})
						goto nextSession
					}
				}
			}
		nextSession:
		}
	}
	return results
}

// ─── JSONL Parsing ──────────────────────────────────────────────────────────────

func parseSession(path, sessionID, projectDirName string) Session {
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{ID: sessionID}
	}

	sess := Session{
		ID:          sessionID,
		ProjectName: decodeProjectName(projectDirName),
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry RawEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}
		if entry.IsSidechain {
			continue
		}

		var raw RawMessage
		if err := json.Unmarshal(entry.Message, &raw); err != nil {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)

		blocks := parseContent(raw.Content)
		// Skip entries with no displayable content
		hasText := false
		for _, b := range blocks {
			if b.Type == "text" || b.Type == "tool_use" || b.Type == "tool_result" {
				hasText = true
				break
			}
		}
		if !hasText {
			continue
		}

		msg := Message{
			UUID:      entry.UUID,
			Role:      raw.Role,
			Timestamp: ts,
			Blocks:    blocks,
			Model:     raw.Model,
			Usage:     raw.Usage,
			Sidechain: entry.IsSidechain,
		}
		sess.Messages = append(sess.Messages, msg)

		if sess.StartTime.IsZero() || ts.Before(sess.StartTime) {
			sess.StartTime = ts
		}
		if ts.After(sess.EndTime) {
			sess.EndTime = ts
		}

		if raw.Role == "user" {
			sess.TurnCount++
		}
		if raw.Usage != nil {
			sess.TotalTokens += raw.Usage.InputTokens + raw.Usage.OutputTokens
			sess.InputTokens += raw.Usage.InputTokens + raw.Usage.CacheCreationInputTokens + raw.Usage.CacheReadInputTokens
			sess.OutputTokens += raw.Usage.OutputTokens
		}
	}
	return sess
}

func parseContent(raw json.RawMessage) []ContentBlock {
	if len(raw) == 0 {
		return nil
	}
	// Try string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) != "" {
			return []ContentBlock{{Type: "text", Text: s}}
		}
		return nil
	}
	// Try array
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	return nil
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func handleDashboard(w http.ResponseWriter, r *http.Request, s *Store) {
	stats := s.Stats()
	projects := s.Projects()

	// recent sessions across all projects
	type RecentSession struct {
		Project    string
		ProjectDir string
		Session    Session
	}
	var recents []RecentSession
	for _, p := range projects {
		for _, sess := range p.Sessions {
			recents = append(recents, RecentSession{
				Project:    p.Name,
				ProjectDir: p.DirName,
				Session:    sess,
			})
		}
	}
	sort.Slice(recents, func(i, j int) bool {
		return recents[i].Session.StartTime.After(recents[j].Session.StartTime)
	})
	if len(recents) > 10 {
		recents = recents[:10]
	}

	renderTemplate(w, "dashboard.html", map[string]any{
		"Stats":    stats,
		"Recents":  recents,
		"Projects": projects[:min(5, len(projects))],
	})
}

func handleProjects(w http.ResponseWriter, r *http.Request, s *Store) {
	projects := s.Projects()
	renderTemplate(w, "projects.html", map[string]any{
		"Projects": projects,
	})
}

func handleProjectDetail(w http.ResponseWriter, r *http.Request, s *Store) {
	name := r.PathValue("name")
	proj := s.GetProject(name)
	if proj == nil {
		http.NotFound(w, r)
		return
	}
	renderTemplate(w, "project_detail.html", map[string]any{
		"Project": proj,
	})
}

func handleSession(w http.ResponseWriter, r *http.Request, s *Store) {
	projectName := r.PathValue("project")
	sessionID := r.PathValue("session")
	sess := s.GetSession(projectName, sessionID)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	proj := s.GetProject(projectName)
	renderTemplate(w, "session.html", map[string]any{
		"Session": sess,
		"Project": proj,
	})
}

func handleSearch(w http.ResponseWriter, r *http.Request, s *Store) {
	filter := SearchFilter{
		Query:      r.URL.Query().Get("q"),
		ProjectDir: r.URL.Query().Get("project"),
		DateFrom:   r.URL.Query().Get("from"),
		DateTo:     r.URL.Query().Get("to"),
	}
	var results []SearchResult
	if filter.Query != "" || filter.ProjectDir != "" || filter.DateFrom != "" || filter.DateTo != "" {
		results = s.Search(filter)
	}
	renderTemplate(w, "search.html", map[string]any{
		"Query":    filter.Query,
		"Filter":   filter,
		"Projects": s.Projects(),
		"Results":  results,
	})
}

func renderTemplate(w http.ResponseWriter, name string, data any) {
	t, ok := pageTemplates[name]
	if !ok {
		http.Error(w, "unknown template: "+name, 500)
		return
	}
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "template error: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, buf.String())
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func decodeProjectName(dirName string) string {
	// dirName encodes an absolute path with '/' replaced by '-'.
	// e.g., "-Users-user-projects-my-cool-app" → "/Users/user/projects/my-cool-app"
	// We resolve ambiguity (dash-in-name vs path separator) by walking the filesystem.
	segs := strings.Split(strings.TrimPrefix(dirName, "-"), "-")
	var parts []string
	for _, s := range segs {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return dirName
	}

	// Greedy longest-match: at each position find the longest run of segments
	// whose joined name (with dashes) exists as a directory on disk.
	current := "/"
	for i := 0; i < len(parts); {
		found := false
		for j := len(parts); j > i; j-- {
			name := strings.Join(parts[i:j], "-")
			if info, err := os.Stat(filepath.Join(current, name)); err == nil && info.IsDir() {
				current = filepath.Join(current, name)
				i = j
				found = true
				break
			}
		}
		if !found {
			// Directory no longer exists; join remaining segments as the name.
			return strings.Join(parts[i:], "-")
		}
	}
	return filepath.Base(current)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func humanizeTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func formatDuration(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return ""
	}
	d := end.Sub(start)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

func extractSnippet(text, query string, maxLen int) string {
	lower := strings.ToLower(text)
	idx := strings.Index(lower, query)
	if idx < 0 {
		return truncate(text, maxLen)
	}
	start := idx - 60
	if start < 0 {
		start = 0
	}
	end := idx + len(query) + 60
	if end > len(text) {
		end = len(text)
	}
	snippet := text[start:end]
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(text) {
		snippet += "…"
	}
	return snippet
}

func renderMarkdown(s string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(s), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(s))
	}
	return template.HTML(buf.String())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// msgHasContent returns true if a message contains at least one text or thinking block.
// Messages with only tool_use/tool_result blocks are considered "tools-only".
func msgHasContent(msg Message) bool {
	for _, b := range msg.Blocks {
		if b.Type == "text" || b.Type == "thinking" {
			return true
		}
	}
	return false
}

// firstUserText returns the first meaningful user text (skips command/system lines).
func firstUserText(sess Session) string {
	for _, msg := range sess.Messages {
		if msg.Role != "user" {
			continue
		}
		for _, b := range msg.Blocks {
			if b.Type != "text" {
				continue
			}
			t := strings.TrimSpace(b.Text)
			// Skip lines that look like command injections or system content
			if strings.HasPrefix(t, "<") || strings.HasPrefix(t, "Base directory") || t == "" {
				continue
			}
			return t
		}
	}
	return ""
}
