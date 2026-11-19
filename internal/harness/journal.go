package harness

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/samhvw8/termctl/internal/term"
)

// The journal is harness-ops' long-term log: one JSON line per action (msg, spawn, slash, interrupt,
// close, note, task new/done/close/accept/exited), appended under a lock and never rewritten.
// Long messages, prompts, briefs and notes are files under projects/<project>/; their entries
// point at them. log and resume read it back, so a session can pick up where another stopped.

const (
	journalFile = "journal.jsonl"
	journalLock = "journal.lock"
	projectsDir = "projects"
	previewLen  = 200 // the text kept in an entry whose full text is in a file
)

// Entry is one journal line.
type Entry struct {
	T        int64  `json:"t"`    // Unix seconds
	Time     string `json:"time"` // local time, RFC 3339
	Zone     string `json:"zone"` // IANA name of the local zone, e.g. Asia/Ho_Chi_Minh
	Event    string `json:"event"`
	Project  string `json:"project"` // absolute path of the folder the action was taken from
	From     string `json:"from,omitempty"`
	FromName string `json:"from_name,omitempty"`
	To       string `json:"to,omitempty"`
	ToName   string `json:"to_name,omitempty"`
	Task     string `json:"task,omitempty"`
	Harness  string `json:"harness,omitempty"`
	Status   string `json:"status,omitempty"`
	Text     string `json:"text,omitempty"` // the full text, or its start when File holds it
	File     string `json:"file,omitempty"`
	Error    string `json:"error,omitempty"` // the action failed; the entry records the attempt
	ID       string `json:"id,omitempty"`    // a deferred message's ID (send --defer), and the ID an ack entry acks
	Key      string `json:"key,omitempty"`   // a msg's idempotency key: sender, target pane and text; set once typed
}

// Journal writes and reads the journal and the project files under Root.
type Journal struct {
	Root  string
	Clock Clock
}

func (j *Journal) now() time.Time {
	if j.Clock == nil {
		return time.Now()
	}
	return time.Unix(int64(j.Clock.Now()), 0)
}

// ZoneName is the IANA name of the local zone: TZ, else the /etc/localtime link, else the abbreviation.
func ZoneName(t time.Time) string {
	if tz := os.Getenv("TZ"); tz != "" && !strings.HasPrefix(tz, ":") {
		return tz
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(link, "zoneinfo/"); i >= 0 {
			return link[i+len("zoneinfo/"):]
		}
	}
	name, _ := t.Zone()
	return name
}

// Header is the first line of every file the journal writes: the project, Unix time, local time and zone.
//
//	/Users/samhv/workspace/tools/termctl · 1794845342 · 2026-11-16 23:09:02 +07 (Asia/Ho_Chi_Minh)
func Header(project string, t time.Time) string {
	return fmt.Sprintf("%s · %d · %s (%s)", project, t.Unix(), t.Format("2006-01-02 15:04:05 -07"), ZoneName(t))
}

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ProjectSlug names a project folder as omp does, without the leading dash: the path below home with
// "/" turned into "-" (~/workspace/tools/termctl is workspace-tools-termctl), "home" for home itself,
// and the whole path for a folder outside home (/private/tmp is private-tmp).
func ProjectSlug(project string) string {
	p := filepath.Clean(project)
	if home, err := os.UserHomeDir(); err == nil {
		if p == home {
			return "home"
		}
		if strings.HasPrefix(p, home+"/") {
			p = strings.TrimPrefix(p, home+"/")
		}
	}
	s := strings.Trim(slugUnsafe.ReplaceAllString(strings.ReplaceAll(strings.Trim(p, "/"), "/", "-"), "-"), "-")
	if s == "" {
		return "root"
	}
	return s
}

// ProjectOf is the project of an action: the pane's folder, else the working directory.
func ProjectOf(p *term.Pane) string {
	if p != nil && p.Folder != "" && p.Folder != "-" {
		return filepath.Clean(ExpandHome(p.Folder))
	}
	wd, _ := os.Getwd()
	return wd
}

// WriteFile writes body under projects/<slug>/<unix>-<kind>-<who>.md, after the header line, and
// returns its path. It never overwrites: a name taken in the same second gets -2, -3, ...
func (j *Journal) WriteFile(project, kind, who, body string) (string, error) {
	t := j.now()
	dir := filepath.Join(j.Root, projectsDir, ProjectSlug(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", term.BackendErr("journal: %v", err)
	}
	base := fmt.Sprintf("%d-%s", t.Unix(), kind)
	if who = unsafeName.ReplaceAllString(who, "_"); who != "" {
		base += "-" + who
	}
	content := []byte(Header(project, t) + "\n\n" + strings.TrimRight(body, " \n\t") + "\n")
	for n := 1; n < 100; n++ {
		name := base + ".md"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.md", base, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", term.BackendErr("journal: %v", err)
		}
		_, werr := f.Write(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", term.BackendErr("journal: %v", werr)
		}
		return path, nil
	}
	return "", term.BackendErr("journal: no free file name for %s in %s", base, dir)
}

// Append adds one entry under the lock. Time fields and an empty project are filled in.
func (j *Journal) Append(e Entry) error {
	j.stamp(&e)
	return j.locked(func() error { return j.appendLocked(e) })
}

// stamp fills in an entry's time fields and an empty project.
func (j *Journal) stamp(e *Entry) {
	if e.T == 0 {
		e.T = j.now().Unix()
	}
	ts := time.Unix(e.T, 0)
	e.Time, e.Zone = ts.Format(time.RFC3339), ZoneName(ts)
	if e.Project == "" {
		e.Project = ProjectOf(nil)
	}
}

// locked runs fn holding the journal's flock: an append, or a look and an append that must not race.
func (j *Journal) locked(fn func() error) error {
	if err := os.MkdirAll(j.Root, 0o755); err != nil {
		return term.BackendErr("journal: %v", err)
	}
	lock, err := os.OpenFile(filepath.Join(j.Root, journalLock), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return term.BackendErr("journal lock: %v", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return term.BackendErr("journal lock: %v", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// appendLocked writes one entry; the caller holds the lock. Time fields are filled in by Append.
func (j *Journal) appendLocked(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return term.BackendErr("journal: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(j.Root, journalFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return term.BackendErr("journal: %v", err)
	}
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return term.BackendErr("journal: %v", werr)
	}
	return nil
}

// Record appends and turns a failure into a loud warning: the action itself already happened, so the
// journal must not fail it, and must not lose it quietly either.
func (j *Journal) Record(e Entry) {
	if j == nil {
		return
	}
	if err := j.Append(e); err != nil {
		Warn(fmt.Sprintf("not recorded in the journal (%s %s): %v", e.Event, e.Task, err))
	}
}

// Filter selects entries; empty fields match everything.
type Filter struct {
	Project string // absolute path
	Task    string
	Pane    string // a full pane address or its start
	Since   int64  // Unix seconds
}

func (f Filter) match(e Entry) bool {
	if f.Project != "" && filepath.Clean(e.Project) != filepath.Clean(f.Project) {
		return false
	}
	if f.Task != "" && e.Task != f.Task {
		return false
	}
	if f.Pane != "" && !strings.HasPrefix(e.From, f.Pane) && !strings.HasPrefix(e.To, f.Pane) &&
		!strings.HasPrefix(trimBackend(e.From), f.Pane) && !strings.HasPrefix(trimBackend(e.To), f.Pane) {
		return false
	}
	return e.T >= f.Since
}

func trimBackend(id string) string {
	if i := strings.Index(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// Read returns the matching entries, oldest first, and how many lines could not be read.
func (j *Journal) Read(f Filter) ([]Entry, int, error) {
	file, err := os.Open(filepath.Join(j.Root, journalFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, term.BackendErr("journal: %v", err)
	}
	defer file.Close()
	var out []Entry
	bad := 0
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			bad++
			continue
		}
		if f.match(e) {
			out = append(out, e)
		}
	}
	if err := sc.Err(); err != nil {
		return out, bad, term.BackendErr("journal: %v", err)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].T < out[b].T })
	return out, bad, nil
}

// OneLine puts a text on one line: newlines and runs of spaces become one space, escapes go.
func OneLine(text string) string {
	return strings.Join(strings.Fields(Flat(text)), " ")
}

// preview is the start of a text whose full form is in a file.
func preview(text string) string {
	text = OneLine(text)
	if r := []rune(text); len(r) > previewLen {
		return string(r[:previewLen]) + "…"
	}
	return text
}

// paneName is the label, else the title, of a pane, for the journal.
func entryName(p *term.Pane) string {
	if p == nil {
		return ""
	}
	if p.Label != "" {
		return p.Label
	}
	return p.Title
}

func address(p *term.Pane) string {
	if p == nil {
		return ""
	}
	return p.Address()
}

// scanBack calls fn with the journal's entries newest first, until fn returns false. It reads the file from
// its end in blocks, so a look at the last minutes costs the same however long the journal is. Lines that
// cannot be read are skipped.
func (j *Journal) scanBack(fn func(Entry) bool) error {
	f, err := os.Open(filepath.Join(j.Root, journalFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return term.BackendErr("journal: %v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return term.BackendErr("journal: %v", err)
	}
	const block = 64 << 10
	var carry []byte // the start of a line whose beginning is in an earlier block
	for pos := st.Size(); pos > 0; {
		n := min(int64(block), pos)
		pos -= n
		buf := make([]byte, n, int(n)+len(carry))
		if _, err := f.ReadAt(buf, pos); err != nil && !errors.Is(err, io.EOF) {
			return term.BackendErr("journal: %v", err)
		}
		lines := bytes.Split(append(buf, carry...), []byte("\n"))
		first := 0
		carry = nil
		if pos > 0 {
			carry, first = append([]byte{}, lines[0]...), 1
		}
		for i := len(lines) - 1; i >= first; i-- {
			if len(bytes.TrimSpace(lines[i])) == 0 {
				continue
			}
			var e Entry
			if json.Unmarshal(lines[i], &e) != nil {
				continue
			}
			if !fn(e) {
				return nil
			}
		}
	}
	return nil
}

// keySlack is how far out of time order two appends can land (each stamps its time before taking the lock).
const keySlack = 60

// FindKey returns the oldest msg entry with idempotency key key written at or after since, or nil.
func (j *Journal) FindKey(key string, since int64) (*Entry, error) {
	var found *Entry
	err := j.scanBack(func(e Entry) bool {
		if e.T < since-keySlack {
			return false
		}
		if e.Event == "msg" && e.Key == key && e.T >= since {
			c := e
			found = &c
		}
		return true
	})
	return found, err
}

// MsgKey is a message's idempotency key: the sender's address, the target pane's ID and the text.
func MsgKey(from, pane, text string) string {
	sum := sha256.Sum256([]byte(from + "\x00" + pane + "\x00" + strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:8])
}

// NewMsgID is a deferred message's ID: m and 6 hex digits.
func NewMsgID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return "m" + hex.EncodeToString(b)
}
