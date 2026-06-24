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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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
	port := flag.Int("port", 5000, "starting HTTP port (increments until a free port is found)")
	claudeDir := flag.String("dir", filepath.Join(os.Getenv("HOME"), ".claude"), "Claude data directory")
	flag.Parse()

	actualPort := findFreePort(*port)
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
		"formatCost":     formatCost,
	}

	pages := []string{"dashboard.html", "projects.html", "project_detail.html", "session.html", "search.html", "stats.html"}
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
	mux.HandleFunc("GET /sessions/{project}/{session}/md", func(w http.ResponseWriter, r *http.Request) {
		handleSessionMD(w, r, store)
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		handleSearch(w, r, store)
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		handleStats(w, r, store)
	})

	addr := fmt.Sprintf(":%d", actualPort)
	url := fmt.Sprintf("http://localhost:%d", actualPort)
	fmt.Printf("Claude Reader running at %s\n", url)
	go func() {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		case "windows":
			cmd = exec.Command("cmd", "/c", "start", url)
		default:
			cmd = exec.Command("xdg-open", url)
		}
		cmd.Run()
	}()
	log.Fatal(http.ListenAndServe(addr, mux))
}

func findFreePort(start int) int {
	for p := start; p < 65536; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	log.Fatal("no free port found starting from", start)
	return 0
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

type ModelStat struct {
	Model  string
	Tokens int
	Cost   float64
}

type Session struct {
	ID           string
	ProjectName  string
	StartTime    time.Time
	EndTime      time.Time
	Messages     []Message
	TurnCount    int
	TotalTokens  int
	InputTokens  int
	OutputTokens int
	TotalCost    float64
	ModelStats   []ModelStat
	FirstText    string // first meaningful user text (pre-computed; nil Messages ok)
}

type Project struct {
	Name     string
	DirName  string
	Sessions []Session
}

// ProjectSummary is a lightweight project descriptor built without parsing session content.
type ProjectSummary struct {
	Name          string
	DirName       string
	SessionCount  int
	LastActive    time.Time // mtime of the most recently modified session file
	LatestSession *Session  // meta-parsed; populated for projects list page
}

// RecentSessionItem is returned by Store.RecentSessions.
type RecentSessionItem struct {
	Project    string
	ProjectDir string
	Session    Session
}

// fileInfo holds per-session-file metadata from a directory scan (no JSONL parsing).
type fileInfo struct {
	path    string
	id      string
	modTime time.Time
	size    int64
}

// projDir is one project's directory entry from a scan.
type projDir struct {
	name    string
	dirName string
	files   []fileInfo
}

// cachedMeta is a cache entry for a meta-parsed session keyed by path.
type cachedMeta struct {
	modTime time.Time
	size    int64
	sess    Session // Messages == nil
}

// ─── Store ─────────────────────────────────────────────────────────────────────

// Store is a stateless-scan + mtime-keyed meta-cache store.
// Every public method re-scans the directory listing on each call so that new,
// deleted, or updated sessions are always reflected without a restart.
type Store struct {
	claudeDir string
	mu        sync.Mutex
	metaCache map[string]*cachedMeta // key: absolute file path
	nameCache map[string]string      // dirName → decoded human-readable project name
}

func NewStore(dir string) *Store {
	return &Store{
		claudeDir: dir,
		metaCache: make(map[string]*cachedMeta),
		nameCache: make(map[string]string),
	}
}

// cachedName returns the decoded project name for dirName, computing and caching it
// on first access. decodeProjectName does filesystem I/O, so we hold the lock only
// around the cache read/write, not the computation.
func (s *Store) cachedName(dirName string) string {
	s.mu.Lock()
	if name, ok := s.nameCache[dirName]; ok {
		s.mu.Unlock()
		return name
	}
	s.mu.Unlock()
	// Compute outside lock — decodeProjectName does os.Stat per segment.
	name := decodeProjectName(dirName)
	s.mu.Lock()
	s.nameCache[dirName] = name
	s.mu.Unlock()
	return name
}

// scan performs a Tier-1 directory scan: no JSONL parsing. Returns one projDir
// per project directory, each with fileInfo entries for every .jsonl session file.
// The directory listing is never cached so new/deleted files are always visible.
func (s *Store) scan() []projDir {
	projectsDir := filepath.Join(s.claudeDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var result []projDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirName := e.Name()
		projPath := filepath.Join(projectsDir, dirName)
		files, _ := os.ReadDir(projPath)
		var fis []fileInfo
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(f.Name(), ".jsonl")
			fis = append(fis, fileInfo{
				path:    filepath.Join(projPath, f.Name()),
				id:      id,
				modTime: fi.ModTime(),
				size:    fi.Size(),
			})
		}
		if len(fis) == 0 {
			continue
		}
		name := s.cachedName(dirName)
		result = append(result, projDir{name: name, dirName: dirName, files: fis})
	}
	return result
}

// metaSession returns a Tier-2 meta-parsed Session for the given file, using the
// mtime+size keyed cache. Parsing (without Message/Block allocation) is done
// outside the lock; only cache reads/writes are locked.
func (s *Store) metaSession(fi fileInfo, projectDirName string) Session {
	s.mu.Lock()
	if c, ok := s.metaCache[fi.path]; ok && c.modTime.Equal(fi.modTime) && c.size == fi.size {
		sess := c.sess
		s.mu.Unlock()
		return sess
	}
	s.mu.Unlock()

	projName := s.cachedName(projectDirName)
	sess := parseSessionMeta(fi.path, fi.id, projName)

	s.mu.Lock()
	s.metaCache[fi.path] = &cachedMeta{modTime: fi.modTime, size: fi.size, sess: sess}
	s.mu.Unlock()

	return sess
}

// Counts returns project and session counts via directory scan only (zero JSONL parsing).
func (s *Store) Counts() (projects, sessions int) {
	dirs := s.scan()
	for _, d := range dirs {
		projects++
		sessions += len(d.files)
	}
	return
}

// ProjectSummaries returns lightweight project descriptors sorted by most recent activity.
// Each entry includes the mtime-based LastActive time and the meta-parsed LatestSession
// (the single most recently modified session per project).
func (s *Store) ProjectSummaries() []ProjectSummary {
	dirs := s.scan()
	summaries := make([]ProjectSummary, 0, len(dirs))
	for _, d := range dirs {
		var latestFi fileInfo
		for _, f := range d.files {
			if f.modTime.After(latestFi.modTime) {
				latestFi = f
			}
		}
		var latestSess *Session
		if latestFi.path != "" {
			sess := s.metaSession(latestFi, d.dirName)
			latestSess = &sess
		}
		summaries = append(summaries, ProjectSummary{
			Name:          d.name,
			DirName:       d.dirName,
			SessionCount:  len(d.files),
			LastActive:    latestFi.modTime,
			LatestSession: latestSess,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].LastActive.After(summaries[j].LastActive)
	})
	return summaries
}

// RecentSessions returns the n most recently modified sessions (by file mtime) across
// all projects, each meta-parsed. Used by the dashboard.
func (s *Store) RecentSessions(n int) []RecentSessionItem {
	dirs := s.scan()
	type fileWithProj struct {
		fi       fileInfo
		projName string
		dirName  string
	}
	var allFiles []fileWithProj
	for _, d := range dirs {
		for _, f := range d.files {
			allFiles = append(allFiles, fileWithProj{f, d.name, d.dirName})
		}
	}
	sort.Slice(allFiles, func(i, j int) bool {
		return allFiles[i].fi.modTime.After(allFiles[j].fi.modTime)
	})
	if len(allFiles) > n {
		allFiles = allFiles[:n]
	}
	result := make([]RecentSessionItem, 0, len(allFiles))
	for _, f := range allFiles {
		sess := s.metaSession(f.fi, f.dirName)
		result = append(result, RecentSessionItem{
			Project:    f.projName,
			ProjectDir: f.dirName,
			Session:    sess,
		})
	}
	return result
}

// ProjectMeta returns a Project whose sessions are all meta-parsed (no Messages/Blocks).
// Used by the project detail page which shows per-session stat badges and preview text.
func (s *Store) ProjectMeta(dirName string) *Project {
	dirs := s.scan()
	for _, d := range dirs {
		if d.dirName != dirName {
			continue
		}
		proj := &Project{Name: d.name, DirName: d.dirName}
		for _, f := range d.files {
			sess := s.metaSession(f, dirName)
			proj.Sessions = append(proj.Sessions, sess)
		}
		sort.Slice(proj.Sessions, func(i, j int) bool {
			return proj.Sessions[i].StartTime.After(proj.Sessions[j].StartTime)
		})
		return proj
	}
	return nil
}

// GetSession does a Tier-3 full parse of a single session file and returns it fresh.
// Always reads the file directly so the result is guaranteed up-to-date.
func (s *Store) GetSession(projectDirName, sessionID string) *Session {
	projectsDir := filepath.Join(s.claudeDir, "projects")
	path := filepath.Join(projectsDir, projectDirName, sessionID+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	projName := s.cachedName(projectDirName)
	sess := parseSession(path, sessionID, projName)
	return &sess
}

type StatsFilter struct {
	From        time.Time
	To          time.Time
	ProjectDirs []string
}

type StatPoint struct {
	Label  string  `json:"label"`
	Count  int     `json:"count"`
	Tokens int     `json:"tokens"`
	Cost   float64 `json:"cost"`
}

type ChartStats struct {
	TotalProjects int         `json:"totalProjects"`
	TotalSessions int         `json:"totalSessions"`
	TotalTurns    int         `json:"totalTurns"`
	TotalTokens   int         `json:"totalTokens"`
	TotalCost     float64     `json:"totalCost"`
	Daily         []StatPoint `json:"daily"`
	Monthly       []StatPoint `json:"monthly"`
	Weekday       []StatPoint `json:"weekday"`
	Hourly        []StatPoint `json:"hourly"`
	Models        []StatPoint `json:"models"`
	Projects      []StatPoint `json:"projects"`
}

func (s *Store) ChartStats(f StatsFilter) ChartStats {
	loc := f.From.Location()

	// Daily buckets: filter range capped at 90 days
	nDays := int(f.To.Sub(f.From).Hours()/24) + 1
	if nDays > 90 {
		nDays = 90
	}
	if nDays < 1 {
		nDays = 1
	}
	dailyStart := time.Date(f.To.Year(), f.To.Month(), f.To.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(nDays - 1))
	dailyIdx := make(map[string]int, nDays)
	daily := make([]StatPoint, nDays)
	for i := 0; i < nDays; i++ {
		d := dailyStart.AddDate(0, 0, i)
		daily[i].Label = d.Format("01/02")
		dailyIdx[d.Format("2006-01-02")] = i
	}

	// Monthly buckets: full filter range (capped at 24 months)
	fromMonth := time.Date(f.From.Year(), f.From.Month(), 1, 0, 0, 0, 0, loc)
	toMonth := time.Date(f.To.Year(), f.To.Month(), 1, 0, 0, 0, 0, loc)
	nMonths := 0
	for m := fromMonth; !m.After(toMonth); m = m.AddDate(0, 1, 0) {
		nMonths++
	}
	if nMonths > 24 {
		nMonths = 24
	}
	if nMonths < 1 {
		nMonths = 1
	}
	showYear := nMonths > 12
	monthlyIdx := make(map[string]int, nMonths)
	monthly := make([]StatPoint, nMonths)
	for i := 0; i < nMonths; i++ {
		d := fromMonth.AddDate(0, i, 0)
		var label string
		if showYear {
			label = fmt.Sprintf("%02d/%02d", d.Year()%100, int(d.Month()))
		} else {
			label = fmt.Sprintf("%d월", int(d.Month()))
		}
		monthly[i].Label = label
		monthlyIdx[d.Format("2006-01")] = i
	}

	weekdayLabels := []string{"월", "화", "수", "목", "금", "토", "일"}
	weekday := make([]StatPoint, 7)
	for i, lbl := range weekdayLabels {
		weekday[i].Label = lbl
	}

	hourly := make([]StatPoint, 24)
	for i := range hourly {
		hourly[i].Label = fmt.Sprintf("%02d", i)
	}

	type acc struct {
		count, tokens int
		cost          float64
	}
	modelAcc := make(map[string]*acc)
	projAcc := make(map[string]*acc)

	var totalProjects, totalSessions, totalTurns, totalTokens int
	var totalCost float64

	fromDay := time.Date(f.From.Year(), f.From.Month(), f.From.Day(), 0, 0, 0, 0, loc)

	projFilter := make(map[string]bool, len(f.ProjectDirs))
	for _, d := range f.ProjectDirs {
		projFilter[d] = true
	}

	dirs := s.scan()
	for _, d := range dirs {
		if len(projFilter) > 0 && !projFilter[d.dirName] {
			continue
		}
		pa := &acc{}
		for _, fi := range d.files {
			sess := s.metaSession(fi, d.dirName)
			if sess.StartTime.Before(fromDay) || sess.StartTime.After(f.To) {
				continue
			}
			pa.count++
			pa.tokens += sess.TotalTokens
			pa.cost += sess.TotalCost
			totalSessions++
			totalTurns += sess.TurnCount
			totalTokens += sess.TotalTokens
			totalCost += sess.TotalCost

			if idx, ok := dailyIdx[sess.StartTime.Format("2006-01-02")]; ok {
				daily[idx].Count++
				daily[idx].Tokens += sess.TotalTokens
				daily[idx].Cost += sess.TotalCost
			}
			if idx, ok := monthlyIdx[sess.StartTime.Format("2006-01")]; ok {
				monthly[idx].Count++
				monthly[idx].Tokens += sess.TotalTokens
				monthly[idx].Cost += sess.TotalCost
			}

			wd := (int(sess.StartTime.Weekday()) + 6) % 7
			weekday[wd].Count++
			weekday[wd].Tokens += sess.TotalTokens
			weekday[wd].Cost += sess.TotalCost

			h := sess.StartTime.Hour()
			hourly[h].Count++
			hourly[h].Tokens += sess.TotalTokens
			hourly[h].Cost += sess.TotalCost

			for _, ms := range sess.ModelStats {
				if _, ok := modelAcc[ms.Model]; !ok {
					modelAcc[ms.Model] = &acc{}
				}
				modelAcc[ms.Model].count++
				modelAcc[ms.Model].tokens += ms.Tokens
				modelAcc[ms.Model].cost += ms.Cost
			}
		}
		if pa.count > 0 {
			totalProjects++
			projAcc[d.name] = pa
		}
	}

	type kv struct {
		key    string
		tokens int
		count  int
		cost   float64
	}

	var modelSlice []kv
	for k, v := range modelAcc {
		modelSlice = append(modelSlice, kv{k, v.tokens, v.count, v.cost})
	}
	sort.Slice(modelSlice, func(i, j int) bool { return modelSlice[i].cost > modelSlice[j].cost })
	if len(modelSlice) > 10 {
		modelSlice = modelSlice[:10]
	}
	models := make([]StatPoint, len(modelSlice))
	for i, m := range modelSlice {
		models[i] = StatPoint{Label: m.key, Count: m.count, Tokens: m.tokens, Cost: m.cost}
	}

	var projSlice []kv
	for k, v := range projAcc {
		projSlice = append(projSlice, kv{k, v.tokens, v.count, v.cost})
	}
	sort.Slice(projSlice, func(i, j int) bool { return projSlice[i].cost > projSlice[j].cost })
	if len(projSlice) > 15 {
		projSlice = projSlice[:15]
	}
	projects := make([]StatPoint, len(projSlice))
	for i, p := range projSlice {
		name := p.key
		if len(name) > 40 {
			name = name[:38] + "…"
		}
		projects[i] = StatPoint{Label: name, Count: p.count, Tokens: p.tokens, Cost: p.cost}
	}

	return ChartStats{
		TotalProjects: totalProjects,
		TotalSessions: totalSessions,
		TotalTurns:    totalTurns,
		TotalTokens:   totalTokens,
		TotalCost:     totalCost,
		Daily:         daily,
		Monthly:       monthly,
		Weekday:       weekday,
		Hourly:        hourly,
		Models:        models,
		Projects:      projects,
	}
}

// pricingFor returns $/MTok rates for input, output, cache-write, cache-read.
// model is the shortened name without the "claude-" prefix.
// Prices from https://docs.anthropic.com/en/docs/about-claude/pricing
func pricingFor(model string) (inP, outP, cwP, crP float64) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "fable") || strings.Contains(m, "mythos"):
		inP, outP = 10.0, 50.0
	case strings.Contains(m, "3-opus"): // Claude 3 Opus (deprecated)
		inP, outP = 15.0, 75.0
	case strings.Contains(m, "opus"): // Opus 4.5+
		inP, outP = 5.0, 25.0
	case strings.Contains(m, "sonnet"):
		inP, outP = 3.0, 15.0
	case strings.Contains(m, "haiku-4"):
		inP, outP = 1.0, 5.0
	case strings.Contains(m, "haiku-3-5") || strings.Contains(m, "3-5-haiku"):
		inP, outP = 0.80, 4.0
	case strings.Contains(m, "haiku"): // Claude 3 Haiku
		inP, outP = 0.25, 1.25
	default:
		return
	}
	cwP = inP * 1.25
	crP = inP * 0.1
	return
}

func formatCost(usd float64) string {
	if usd < 0.0001 {
		return ""
	}
	if usd < 0.01 {
		return "<$0.01"
	}
	if usd >= 1000 {
		return fmt.Sprintf("$%.1fK", usd/1000)
	}
	return fmt.Sprintf("$%.2f", usd)
}

func shortenModel(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if len(last) == 8 {
			allDigits := true
			for _, c := range last {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return strings.Join(parts[:len(parts)-1], "-")
			}
		}
	}
	return name
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
	query := strings.ToLower(f.Query)

	var fromTime, toTime time.Time
	if f.DateFrom != "" {
		fromTime, _ = time.Parse("2006-01-02", f.DateFrom)
	}
	if f.DateTo != "" {
		toTime, _ = time.Parse("2006-01-02", f.DateTo)
		toTime = toTime.Add(24*time.Hour - time.Second)
	}

	dirs := s.scan()
	var results []SearchResult

	for _, d := range dirs {
		if f.ProjectDir != "" && d.dirName != f.ProjectDir {
			continue
		}
		// Sort files by mtime desc for consistent ordering
		files := make([]fileInfo, len(d.files))
		copy(files, d.files)
		sort.Slice(files, func(i, j int) bool {
			return files[i].modTime.After(files[j].modTime)
		})

		for _, fi := range files {
			if query == "" {
				// Filter-only mode: use cached meta session (no full parse)
				sess := s.metaSession(fi, d.dirName)
				if !fromTime.IsZero() && sess.StartTime.Before(fromTime) {
					continue
				}
				if !toTime.IsZero() && sess.StartTime.After(toTime) {
					continue
				}
				results = append(results, SearchResult{
					Project:     d.name,
					ProjectDir:  d.dirName,
					SessionID:   fi.id,
					SessionTime: sess.StartTime,
					Snippet:     sess.FirstText,
				})
				continue
			}
			// Query-based search: full parse (Tier 3), discard after scanning
			sess := parseSession(fi.path, fi.id, d.name)
			if !fromTime.IsZero() && sess.StartTime.Before(fromTime) {
				continue
			}
			if !toTime.IsZero() && sess.StartTime.After(toTime) {
				continue
			}
			found := false
			for _, msg := range sess.Messages {
				if found {
					break
				}
				for _, b := range msg.Blocks {
					if b.Type == "text" && strings.Contains(strings.ToLower(b.Text), query) {
						snippet := extractSnippet(b.Text, query, 150)
						results = append(results, SearchResult{
							Project:     d.name,
							ProjectDir:  d.dirName,
							SessionID:   fi.id,
							SessionTime: sess.StartTime,
							Snippet:     snippet,
						})
						found = true
						break
					}
				}
			}
		}
	}
	return results
}

// ─── JSONL Parsing ──────────────────────────────────────────────────────────────

// parseSession does a full (Tier-3) parse: builds Message/ContentBlock slices.
// projectName is the already-decoded human-readable name (not the dir-encoded form).
func parseSession(path, sessionID, projectName string) Session {
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{ID: sessionID}
	}

	sess := Session{
		ID:          sessionID,
		ProjectName: projectName,
	}
	type mUsage struct{ in, out, cw, cr int }
	modelUsageMap := make(map[string]*mUsage)

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
		ts = ts.Local()

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
			if sess.FirstText == "" {
				for _, b := range blocks {
					if b.Type == "text" {
						t := strings.TrimSpace(b.Text)
						if !strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "Base directory") && t != "" {
							sess.FirstText = t
							break
						}
					}
				}
			}
			sess.TurnCount++
		}
		if raw.Usage != nil {
			sess.TotalTokens += raw.Usage.InputTokens + raw.Usage.OutputTokens
			sess.InputTokens += raw.Usage.InputTokens + raw.Usage.CacheCreationInputTokens + raw.Usage.CacheReadInputTokens
			sess.OutputTokens += raw.Usage.OutputTokens
			if raw.Model != "" {
				name := strings.TrimPrefix(shortenModel(raw.Model), "claude-")
				u := modelUsageMap[name]
				if u == nil {
					u = &mUsage{}
					modelUsageMap[name] = u
				}
				u.in += raw.Usage.InputTokens
				u.out += raw.Usage.OutputTokens
				u.cw += raw.Usage.CacheCreationInputTokens
				u.cr += raw.Usage.CacheReadInputTokens
			}
		}
	}

	for name, u := range modelUsageMap {
		inP, outP, cwP, crP := pricingFor(name)
		cost := float64(u.in)*inP/1e6 +
			float64(u.out)*outP/1e6 +
			float64(u.cw)*cwP/1e6 +
			float64(u.cr)*crP/1e6
		sess.ModelStats = append(sess.ModelStats, ModelStat{
			Model:  name,
			Tokens: u.in + u.out,
			Cost:   cost,
		})
		sess.TotalCost += cost
	}
	sort.Slice(sess.ModelStats, func(i, j int) bool {
		return sess.ModelStats[i].Tokens > sess.ModelStats[j].Tokens
	})

	return sess
}

// parseSessionMeta does a Tier-2 parse: computes all aggregate fields (times, token
// counts, cost, ModelStats, FirstText) but does NOT build Message or ContentBlock
// slices, so the resulting Session.Messages is always nil.  Memory use per cached
// entry is therefore O(1) regardless of session length.
// projectName is the already-decoded human-readable name.
func parseSessionMeta(path, sessionID, projectName string) Session {
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{ID: sessionID}
	}

	sess := Session{
		ID:          sessionID,
		ProjectName: projectName,
	}
	type mUsage struct{ in, out, cw, cr int }
	modelUsageMap := make(map[string]*mUsage)

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
		ts = ts.Local()

		// Parse content to apply the same hasText filter used by parseSession,
		// ensuring TurnCount values are identical between meta and full parse.
		blocks := parseContent(raw.Content)
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

		if sess.StartTime.IsZero() || ts.Before(sess.StartTime) {
			sess.StartTime = ts
		}
		if ts.After(sess.EndTime) {
			sess.EndTime = ts
		}

		if raw.Role == "user" {
			if sess.FirstText == "" {
				for _, b := range blocks {
					if b.Type == "text" {
						t := strings.TrimSpace(b.Text)
						if !strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "Base directory") && t != "" {
							sess.FirstText = t
							break
						}
					}
				}
			}
			sess.TurnCount++
		}
		if raw.Usage != nil {
			sess.TotalTokens += raw.Usage.InputTokens + raw.Usage.OutputTokens
			sess.InputTokens += raw.Usage.InputTokens + raw.Usage.CacheCreationInputTokens + raw.Usage.CacheReadInputTokens
			sess.OutputTokens += raw.Usage.OutputTokens
			if raw.Model != "" {
				name := strings.TrimPrefix(shortenModel(raw.Model), "claude-")
				u := modelUsageMap[name]
				if u == nil {
					u = &mUsage{}
					modelUsageMap[name] = u
				}
				u.in += raw.Usage.InputTokens
				u.out += raw.Usage.OutputTokens
				u.cw += raw.Usage.CacheCreationInputTokens
				u.cr += raw.Usage.CacheReadInputTokens
			}
		}
		// NOTE: blocks are NOT stored; no Message struct built; sess.Messages stays nil.
	}

	for name, u := range modelUsageMap {
		inP, outP, cwP, crP := pricingFor(name)
		cost := float64(u.in)*inP/1e6 +
			float64(u.out)*outP/1e6 +
			float64(u.cw)*cwP/1e6 +
			float64(u.cr)*crP/1e6
		sess.ModelStats = append(sess.ModelStats, ModelStat{
			Model:  name,
			Tokens: u.in + u.out,
			Cost:   cost,
		})
		sess.TotalCost += cost
	}
	sort.Slice(sess.ModelStats, func(i, j int) bool {
		return sess.ModelStats[i].Tokens > sess.ModelStats[j].Tokens
	})

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
	// Tier 1 only: zero JSONL parsing for the dashboard.
	totalProjects, totalSessions := s.Counts()
	// Top 10 recent sessions: meta-parse of 10 files only.
	recents := s.RecentSessions(10)
	// Project list: meta-parse of 1 file per project (latest session only).
	projects := s.ProjectSummaries()
	if len(projects) > 5 {
		projects = projects[:5]
	}
	renderTemplate(w, "dashboard.html", map[string]any{
		"TotalProjects": totalProjects,
		"TotalSessions": totalSessions,
		"Recents":       recents,
		"Projects":      projects,
	})
}

func handleProjects(w http.ResponseWriter, r *http.Request, s *Store) {
	renderTemplate(w, "projects.html", map[string]any{
		"Projects": s.ProjectSummaries(),
	})
}

func handleProjectDetail(w http.ResponseWriter, r *http.Request, s *Store) {
	name := r.PathValue("name")
	proj := s.ProjectMeta(name)
	if proj == nil {
		http.NotFound(w, r)
		return
	}
	renderTemplate(w, "project_detail.html", map[string]any{
		"Project": proj,
	})
}

func handleSession(w http.ResponseWriter, r *http.Request, s *Store) {
	projectDirName := r.PathValue("project")
	sessionID := r.PathValue("session")
	sess := s.GetSession(projectDirName, sessionID)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	renderTemplate(w, "session.html", map[string]any{
		"Session": sess,
		"Project": &Project{
			Name:    s.cachedName(projectDirName),
			DirName: projectDirName,
		},
	})
}

func handleSessionMD(w http.ResponseWriter, r *http.Request, s *Store) {
	projectDirName := r.PathValue("project")
	sessionID := r.PathValue("session")
	sess := s.GetSession(projectDirName, sessionID)
	if sess == nil {
		http.NotFound(w, r)
		return
	}

	var sb strings.Builder
	sb.WriteString("# 세션 대화\n\n")
	sb.WriteString("| 항목 | 값 |\n|---|---|\n")
	sb.WriteString(fmt.Sprintf("| 프로젝트 | %s |\n", sess.ProjectName))
	sb.WriteString(fmt.Sprintf("| 세션 ID | %s |\n", sess.ID))
	sb.WriteString(fmt.Sprintf("| 시작 | %s |\n", formatTime(sess.StartTime)))
	if !sess.EndTime.IsZero() {
		sb.WriteString(fmt.Sprintf("| 종료 | %s (%s) |\n", formatTime(sess.EndTime), formatDuration(sess.StartTime, sess.EndTime)))
	}
	sb.WriteString(fmt.Sprintf("| 턴 수 | %d |\n", sess.TurnCount))
	if sess.TotalTokens > 0 {
		sb.WriteString(fmt.Sprintf("| 총 토큰 | %s |\n", humanizeTokens(sess.TotalTokens)))
	}
	sb.WriteString("\n---\n\n")

	for _, msg := range sess.Messages {
		switch msg.Role {
		case "user":
			sb.WriteString("## 사용자\n\n")
		case "assistant":
			model := "Claude"
			if msg.Model != "" {
				model = fmt.Sprintf("Claude (%s)", truncate(msg.Model, 20))
			}
			sb.WriteString(fmt.Sprintf("## %s\n\n", model))
		}
		sb.WriteString(fmt.Sprintf("*%s*\n\n", formatTime(msg.Timestamp)))

		for _, b := range msg.Blocks {
			switch b.Type {
			case "text":
				sb.WriteString(b.Text)
				sb.WriteString("\n\n")
			case "thinking":
				sb.WriteString("<details>\n<summary>💭 내부 추론</summary>\n\n")
				sb.WriteString(b.Text)
				sb.WriteString("\n\n</details>\n\n")
			case "tool_use":
				sb.WriteString(fmt.Sprintf("**🔧 도구 호출: %s**\n\n", b.Name))
				if len(b.Input) > 0 && string(b.Input) != "null" {
					sb.WriteString("```json\n")
					sb.WriteString(truncate(string(b.Input), 2000))
					sb.WriteString("\n```\n\n")
				}
			case "tool_result":
				sb.WriteString("**📤 도구 결과**\n\n")
				if len(b.Content) > 0 && string(b.Content) != "null" {
					sb.WriteString("```\n")
					sb.WriteString(truncate(string(b.Content), 2000))
					sb.WriteString("\n```\n\n")
				}
			}
		}
		sb.WriteString("---\n\n")
	}

	filename := fmt.Sprintf("session-%s.md", truncate(sess.ID, 8))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	fmt.Fprint(w, sb.String())
}

func handleStats(w http.ResponseWriter, r *http.Request, s *Store) {
	q := r.URL.Query()
	now := time.Now()
	loc := now.Location()

	fromStr := q.Get("from")
	toStr := q.Get("to")
	projectDirs := q["project"]

	var from, to time.Time
	if fromStr != "" {
		from, _ = time.ParseInLocation("2006-01-02", fromStr, loc)
	}
	if toStr != "" {
		t, _ := time.ParseInLocation("2006-01-02", toStr, loc)
		if !t.IsZero() {
			to = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, loc)
		}
	}
	if from.IsZero() {
		d := now.AddDate(0, -1, 0)
		from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	}
	if to.IsZero() {
		to = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc)
	}

	selectedProjects := make(map[string]bool, len(projectDirs))
	for _, d := range projectDirs {
		selectedProjects[d] = true
	}

	f := StatsFilter{From: from, To: to, ProjectDirs: projectDirs}
	cs := s.ChartStats(f)
	jsonBytes, err := json.Marshal(cs)
	if err != nil {
		http.Error(w, "json error", 500)
		return
	}
	renderTemplate(w, "stats.html", map[string]any{
		"Stats":            cs,
		"StatsJSON":        template.JS(jsonBytes),
		"FromStr":          from.Format("2006-01-02"),
		"ToStr":            to.Format("2006-01-02"),
		"SelectedProjects": selectedProjects,
		"Projects":         s.ProjectSummaries(),
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
		"Projects": s.ProjectSummaries(),
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

// firstUserText returns the first meaningful user text for a session.
// For meta-parsed sessions (Messages == nil), uses the pre-computed FirstText field.
// For full-parsed sessions (session detail view), falls back to iterating Messages.
func firstUserText(sess Session) string {
	if sess.FirstText != "" {
		return sess.FirstText
	}
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
