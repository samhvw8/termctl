package harness

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samhvw8/termctl/internal/term"
)

var scopeNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
var scopeSlugUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ParseStallDuration parses a Go duration ("30m", "1h", "90s"), Nd ("2d"), or "0"/"off" to disable.
func ParseStallDuration(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, term.UsageErr("--stall requires a duration (e.g. 30m, 1h, 90s, 1d, or 0/off)")
	}
	if s == "0" || strings.EqualFold(s, "off") {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") || strings.HasSuffix(s, "D") {
		days, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil || days < 0 {
			return 0, term.UsageErr("invalid --stall duration %q", s)
		}
		return days * 86400, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, term.UsageErr("invalid --stall duration %q (use e.g. 30m, 1h, 90s, 1d, or off)", s)
	}
	return d.Seconds(), nil
}

// AttachSuffix is appended to the lead's first non-deferred send to a silently attached scope pane.
func AttachSuffix(scopeName, owner string) string {
	return fmt.Sprintf(" [attached: scope %s, lead %s; report with harness-ops report]", scopeName, owner)
}

// effectiveStallAfter returns the idle threshold in seconds for lease l, or 0 when stall detection is off.
func effectiveStallAfter(d *Data, l *Lease) float64 {
	if l == nil || l.State != "active" {
		return 0
	}
	if l.Job != "" {
		if j := d.Jobs[l.Job]; j != nil && j.Status == "running" && !j.Closed {
			return stallAfter
		}
		return 0
	}
	if l.Scope != "" {
		if s := d.Scopes[l.Scope]; s != nil && s.StallAfter > 0 {
			return s.StallAfter
		}
	}
	return 0
}

func scopeTag(s *Scope, l *Lease) string {
	name := l.ID
	if s != nil && s.Name != "" {
		name = s.Name
	}
	return fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", name, l.Harness, l.Pane, l.Cwd)
}

func scopeDialogNotice(s *Scope, l *Lease, sig Signal) string {
	name, sid := l.ID, l.Scope
	if s != nil {
		name, sid = s.Name, s.ID
	}
	return fmt.Sprintf("scope %s (%s) %s", name, sid, dialogLine(name, sig.Dialog, sig.Status))
}

func leaseTag(d *Data, l *Lease) string {
	if j := d.jobOf(l); j != nil {
		return dialogTag(j, l)
	}
	if l.Scope != "" {
		return scopeTag(d.Scopes[l.Scope], l)
	}
	return paneTag(d, l)
}

func (t *Tasks) scopeOps(b term.Backend, sender *term.Pane, s *Scope, l *Lease) *Ops {
	ops := t.ops(b, sender)
	if s != nil {
		ops.Scope, ops.Project = s.ID, s.Project
	} else if l != nil {
		ops.Scope, ops.Project = l.Scope, l.Project
	}
	return ops
}

func (t *Tasks) leaseOps(b term.Backend, sender *term.Pane, l *Lease) *Ops {
	ops := t.ops(b, sender)
	if l != nil {
		ops.Task = l.Job
		ops.Scope = l.Scope
		if l.Project != "" {
			ops.Project = l.Project
		}
	}
	return ops
}

func (t *Tasks) logScope(event string, s *Scope, l *Lease, status, text string) {
	if s == nil {
		return
	}
	project := s.Project
	if project == "" && l != nil {
		project = l.Project
	}
	if project == "" {
		project = ProjectOf(t.Sender)
	}
	e := Entry{
		Event:    event,
		Project:  project,
		From:     address(t.Sender),
		FromName: entryName(t.Sender),
		Scope:    s.ID,
		Status:   status,
		Text:     text,
	}
	if l != nil {
		e.To = l.Pane
		e.Harness = l.Harness
		if t.Sender != nil && t.Sender.ID == l.Pane {
			e.From = l.Pane
			e.To = l.Owner
		}
	}
	t.journal().Record(e)
}

// ErrSeveralScopes marks a scope query that matches more than one scope.
var ErrSeveralScopes = errors.New("several scopes match")

var errSeveralScopes = ErrSeveralScopes

func pickUniqueScope(hits []*Scope) *Scope {
	if len(hits) == 1 {
		return hits[0]
	}
	if len(hits) <= 1 {
		return nil
	}
	var live []*Scope
	for _, s := range hits {
		if !s.Dropped {
			live = append(live, s)
		}
	}
	if len(live) == 1 {
		return live[0]
	}
	return nil
}

// FindScope resolves a scope by exact ID, unique 3+ ID prefix, or name (callerProj first, else unique).
func (d *Data) FindScope(target, callerProj string) (*Scope, error) {
	if s, ok := d.Scopes[target]; ok {
		return s, nil
	}
	if len(target) >= 3 && strings.HasPrefix(target, "s") {
		var hits []*Scope
		for _, s := range d.Scopes {
			if strings.HasPrefix(s.ID, target) {
				hits = append(hits, s)
			}
		}
		if len(hits) == 1 {
			return hits[0], nil
		}
		if len(hits) > 1 {
			return nil, &findError{err: term.TargetErr("several scopes match '%s'", target), several: true, sentinel: ErrSeveralScopes}
		}
	}
	if callerProj != "" {
		callerProj = filepath.Clean(callerProj)
		var projHits []*Scope
		for _, s := range d.Scopes {
			if s.Name == target && filepath.Clean(s.Project) == callerProj {
				projHits = append(projHits, s)
			}
		}
		if u := pickUniqueScope(projHits); u != nil {
			return u, nil
		}
		if len(projHits) > 1 {
			return nil, &findError{err: term.TargetErr("several scopes named '%s' in %s", target, callerProj), several: true, sentinel: ErrSeveralScopes}
		}
	}
	var hits []*Scope
	for _, s := range d.Scopes {
		if s.Name == target {
			hits = append(hits, s)
		}
	}
	if u := pickUniqueScope(hits); u != nil {
		return u, nil
	}
	if len(hits) > 1 {
		return nil, &findError{err: term.TargetErr("several scopes named '%s'", target), several: true, sentinel: ErrSeveralScopes}
	}
	return nil, term.TargetErr("no scope matches '%s'", target)
}

// findScope resolves a scope by exact ID, unique 3+ ID prefix, or name (caller's project first, else unique).
func (t *Tasks) findScope(d *Data, target string) (*Scope, error) {
	callerProj := ""
	if t.Sender != nil {
		callerProj = filepath.Clean(ProjectOf(t.Sender))
	}
	return d.FindScope(target, callerProj)
}

func (t *Tasks) findLease(d *Data, target string) (*Lease, error) {
	if l, ok := d.Leases[target]; ok {
		return l, nil
	}
	if len(target) >= 3 && strings.HasPrefix(target, "l") {
		var hits []*Lease
		for _, l := range d.Leases {
			if strings.HasPrefix(l.ID, target) {
				hits = append(hits, l)
			}
		}
		if len(hits) == 1 {
			return hits[0], nil
		}
		if len(hits) > 1 {
			return nil, fmt.Errorf("%w: %w", term.TargetErr("several leases match '%s'", target), errSeveral)
		}
	}
	return nil, term.TargetErr("no lease matches '%s'", target)
}

// sanitizeSlug turns a label or folder basename into a valid scope slug, falling back to fallback.
func sanitizeSlug(raw, fallback string) string {
	s := scopeSlugUnsafe.ReplaceAllString(strings.TrimSpace(raw), "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = fallback
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-.")
	}
	if !scopeNameRe.MatchString(s) {
		return "scope"
	}
	return s
}

func scopeByProjectName(d *Data, project, name string) *Scope {
	project = filepath.Clean(project)
	ids := make([]string, 0, len(d.Scopes))
	for id := range d.Scopes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var first *Scope
	for _, id := range ids {
		s := d.Scopes[id]
		if s.Name == name && filepath.Clean(s.Project) == project {
			if !s.Dropped {
				return s
			}
			if first == nil {
				first = s
			}
		}
	}
	return first
}

func defaultScopeName(d *Data, p *term.Pane, callerProject, curScopeID string) string {
	fallback := "pane-" + term.Cut(p.Native, 6)
	base := ""
	switch {
	case p.Label != "":
		base = sanitizeSlug(p.Label, fallback)
	case p.Folder != "" && p.Folder != "-":
		base = sanitizeSlug(filepath.Base(ExpandHome(p.Folder)), fallback)
	default:
		base = sanitizeSlug(fallback, "scope")
	}
	taken := map[string]bool{}
	for _, s := range d.Scopes {
		if s.ID == curScopeID || filepath.Clean(s.Project) != callerProject {
			continue
		}
		taken[s.Name] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		suf := fmt.Sprintf("-%d", n)
		prefix := base
		if len(prefix)+len(suf) > 40 {
			prefix = strings.TrimRight(prefix[:40-len(suf)], "-.")
		}
		cand := prefix + suf
		if !taken[cand] {
			return cand
		}
	}
}

// AttachOpts configures Tasks.Attach.
type AttachOpts struct {
	Target      string
	Name        string
	About       string
	HasAbout    bool
	Stall       float64
	HasStall    bool
	SubDepth    int
	HasSubDepth bool
	TreeMax     int
	HasTreeMax  bool
	Notify      bool
	Force       bool
}

// Attach binds an existing pane to a durable Scope under the caller's supervision.
// Returns the ScopeView, whether it updated an existing attached scope in place, and any error.
func (t *Tasks) Attach(o AttachOpts) (*ScopeView, bool, error) {
	me, err := t.owner()
	if err != nil {
		return nil, false, err
	}
	if o.Name != "" && !scopeNameRe.MatchString(o.Name) {
		return nil, false, term.UsageErr("invalid scope name %q (letters, digits, . _ -; max 40)", o.Name)
	}
	if o.SubDepth < 0 || o.TreeMax < 0 {
		return nil, false, term.UsageErr("--sub-depth and --tree-max must not be negative")
	}
	if o.Stall < 0 {
		return nil, false, term.UsageErr("--stall must not be negative")
	}
	maxDepth, err := envInt("TERMCTL_MAX_DEPTH", defaultMaxDepth)
	if err != nil {
		return nil, false, err
	}
	if err := t.Reconcile(); err != nil {
		return nil, false, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, false, err
	}
	b, p, err := t.paneTarget(d, o.Target)
	if err != nil {
		return nil, false, err
	}
	if p.IsSelf || t.isSelf(p.ID) {
		return nil, false, term.TargetErr("refusing to attach your own pane")
	}
	ref := b.ServerRef()
	callerProject := filepath.Clean(ProjectOf(me))

	// Check open Jobs on the target pane before anything else.
	for _, j := range d.Jobs {
		jl := d.leaseOf(j)
		if open(j.Status) && !j.Closed && jl.Pane == p.ID && sameServer(jl.Ref, ref) {
			return nil, false, term.Errf(term.CodeGuard, "%s holds open task %s (%s); use task adopt", p.Address(), j.ID, j.Status)
		}
	}

	cur := activeLeaseForPane(d, p.ID, ref)
	// Pre-flight validation before --notify draft check or registry mutation.
	if cur != nil && cur.Scope != "" {
		if curScope := d.Scopes[cur.Scope]; curScope != nil && o.Name != "" && o.Name != curScope.Name {
			return nil, false, term.UsageErr("%s is already attached to scope %s (%s); detach it before attaching as %s", p.Address(), curScope.Name, curScope.ID, o.Name)
		}
	}
	if cur != nil && cur.Job == "" && !t.owns(cur) {
		if !o.Force && t.ownerAlive(cur.Owner, cur.OwnerRef) {
			return nil, false, term.Errf(term.CodeGuard, "%s is already supervised by %s, which is still there; ask it, or use --force", p.Address(), cur.Owner)
		}
	}
	if cur == nil || cur.Scope == "" || !t.owns(cur) {
		pl := activeLeaseForPane(d, me.ID, t.callerRef())
		if cur != nil {
			seen := map[string]bool{}
			for y := pl; y != nil && !seen[y.ID]; y = d.Leases[y.Parent] {
				seen[y.ID] = true
				if y.ID == cur.ID {
					return nil, false, term.TargetErr("%s is above your own pane; it cannot report to you", p.Address())
				}
			}
		}
		if err := placeLease(d, &Lease{ID: "tmp"}, pl, o.SubDepth, o.TreeMax, maxDepth, false); err != nil {
			return nil, false, err
		}
		if cur == nil || cur.Scope == "" {
			if o.Name != "" {
				for _, s := range d.Scopes {
					if s.Name == o.Name && filepath.Clean(s.Project) == callerProject && s.Lease != "" {
						if sl := d.Leases[s.Lease]; sl != nil && sl.State == "active" && (sl.Pane != p.ID || !sameServer(sl.Ref, ref)) {
							return nil, false, term.TargetErr("scope %s (%s) is already bound to live pane %s", s.Name, s.ID, sl.Pane)
						}
					}
				}
			}
		}
	}

	if o.Notify {
		prof := ForProcess(p.Process)
		if err := t.ops(b, me).CheckDraft(p, prof, false); err != nil {
			return nil, false, err
		}
	}

	var outScope Scope
	var outLease Lease
	var view *ScopeView
	var renames []titleUpdate
	updated := false
	takenOver := false
	oldOwner := ""
	err = t.Reg.Update(func(d *Data) error {
		now := t.clock().Now()
		for _, j := range d.Jobs {
			jl := d.leaseOf(j)
			if open(j.Status) && !j.Closed && jl.Pane == p.ID && sameServer(jl.Ref, ref) {
				return term.Errf(term.CodeGuard, "%s holds open task %s (%s); use task adopt", p.Address(), j.ID, j.Status)
			}
		}
		cur := activeLeaseForPane(d, p.ID, ref)
		// Case 1: Caller already supervises this pane as a Scope -> idempotent update in place.
		if cur != nil && t.owns(cur) && cur.Scope != "" {
			s := d.Scopes[cur.Scope]
			if s == nil {
				return term.BackendErr("scope %s missing for lease %s", cur.Scope, cur.ID)
			}
			if o.Name != "" && o.Name != s.Name {
				return term.UsageErr("%s is already attached to scope %s (%s); detach it before attaching as %s", p.Address(), s.Name, s.ID, o.Name)
			}
			s.Dropped = false
			if o.HasAbout {
				s.About = OneLine(o.About)
			}
			if o.HasStall {
				s.StallAfter = o.Stall
			}
			updated = true
			outScope, outLease = *s, *cur
			view = d.ScopeView(s.ID)
			return nil
		}

		// Case 2: Another lead's active Scope or bare Lease -> reparentLease.
		if cur != nil && cur.Job == "" && !t.owns(cur) {
			alive := t.ownerAlive(cur.Owner, cur.OwnerRef)
			if alive && !o.Force {
				return term.Errf(term.CodeGuard, "%s is already supervised by %s, which is still there; ask it, or use --force", p.Address(), cur.Owner)
			}
			old, err := t.reparentLease(d, cur, me, t.callerRef(), maxDepth, alive, &renames)
			if err != nil {
				return err
			}
			oldOwner, takenOver = old, true
			if cur.Scope != "" {
				s := d.Scopes[cur.Scope]
				if s == nil {
					return term.BackendErr("scope %s missing for lease %s", cur.Scope, cur.ID)
				}
				if o.Name != "" && o.Name != s.Name {
					return term.UsageErr("%s is already attached to scope %s (%s); detach it before attaching as %s", p.Address(), s.Name, s.ID, o.Name)
				}
				if o.HasSubDepth || o.HasTreeMax {
					pl := activeLeaseForPane(d, me.ID, t.callerRef())
					reqSub, reqMax := o.SubDepth, o.TreeMax
					if !o.HasSubDepth {
						reqSub = cur.SubDepth
					}
					if !o.HasTreeMax {
						reqMax = cur.TreeMax
					}
					if err := placeLease(d, &Lease{ID: "tmp"}, pl, reqSub, reqMax, maxDepth, false); err != nil {
						return err
					}
					if o.HasSubDepth {
						cur.SubDepth = min(reqSub, maxDepth-cur.Depth)
					}
					if o.HasTreeMax && reqMax > 0 && reqMax < cur.TreeMax {
						cur.TreeMax = reqMax
					}
					for _, y := range leaseDescendants(d, cur.ID) {
						yj := d.jobOf(y)
						beforeTitle := ""
						if yj != nil {
							beforeTitle = paneTitle(yj, y)
						}
						if cur.TreeMax > 0 && (y.TreeMax == 0 || y.TreeMax > cur.TreeMax) {
							y.TreeMax = cur.TreeMax
						}
						maxAllowed := max(0, cur.SubDepth-(y.Depth-cur.Depth))
						if y.SubDepth > maxAllowed {
							y.SubDepth = maxAllowed
						}
						if yj != nil && y.Pane != "" && open(yj.Status) && !yj.Closed {
							if afterTitle := paneTitle(yj, y); afterTitle != beforeTitle {
								renames = append(renames, titleUpdate{y.Pane, y.Ref, afterTitle})
							}
						}
					}
				}
				s.Dropped = false
				if o.HasAbout {
					s.About = OneLine(o.About)
				}
				if o.HasStall {
					s.StallAfter = o.Stall
				}
				outScope, outLease = *s, *cur
				view = d.ScopeView(s.ID)
				return nil
			}
			// cur was another lead's bare spawn lease, now reparented to caller; fall through to attach Scope to cur.
		}

		// Case 3: Settled/closed Job lease still active on pane -> end old Lease first.
		if cur != nil && cur.Job != "" {
			endLease(cur, now)
			cur = nil
		}

		// Resolve or create Scope.
		var s *Scope
		if o.Name != "" {
			s = scopeByProjectName(d, callerProject, o.Name)
			if s != nil {
				if s.Lease != "" {
					if sl := d.Leases[s.Lease]; sl != nil && sl.State == "active" && (sl.Pane != p.ID || !sameServer(sl.Ref, ref)) {
						return term.TargetErr("scope %s (%s) is already bound to live pane %s", s.Name, s.ID, sl.Pane)
					}
				}
				s.Dropped = false
				if o.HasAbout {
					s.About = OneLine(o.About)
				}
				if o.HasStall {
					s.StallAfter = o.Stall
				}
			} else {
				s = &Scope{
					ID:         newScopeID(d),
					Name:       o.Name,
					About:      OneLine(o.About),
					Project:    callerProject,
					StallAfter: o.Stall,
					Created:    now,
				}
				d.Scopes[s.ID] = s
			}
		} else {
			curScopeID := ""
			if cur != nil {
				curScopeID = cur.Scope
			}
			name := defaultScopeName(d, p, callerProject, curScopeID)
			s = &Scope{
				ID:         newScopeID(d),
				Name:       name,
				About:      OneLine(o.About),
				Project:    callerProject,
				StallAfter: o.Stall,
				Created:    now,
			}
			d.Scopes[s.ID] = s
		}

		pl := activeLeaseForPane(d, me.ID, t.callerRef())
		var l *Lease
		if cur != nil && cur.Scope == "" && cur.Job == "" {
			// Upgrade bare spawn lease in place, preserving CreatedPane, Dialog, Worked, AnsweredAt.
			l = cur
			if err := placeLease(d, l, pl, o.SubDepth, o.TreeMax, maxDepth, false); err != nil {
				return err
			}
			l.Project = callerProject
			l.Scope = s.ID
		} else {
			l = &Lease{
				ID:       newLeaseID(d),
				Owner:    me.ID,
				OwnerRef: t.callerRef(),
				Pane:     p.ID,
				Ref:      ref,
				Harness:  HarnessName(p),
				Label:    p.Label,
				Cwd:      p.Folder,
				Project:  callerProject,
				Scope:    s.ID,
				State:    "active",
				Launched: true,
				Started:  now,
			}
			if err := placeLease(d, l, pl, o.SubDepth, o.TreeMax, maxDepth, false); err != nil {
				return err
			}
			d.Leases[l.ID] = l
		}
		s.Lease = l.ID
		s.Dropped = false
		outScope, outLease = *s, *l
		view = d.ScopeView(s.ID)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	t.applyRenames(renames)
	if takenOver {
		t.logScope("scope.adopt", &outScope, &outLease, "live", "adopted from "+oldOwner)
		if _, ferr := t.FlushPending(); ferr != nil {
			Warn(fmt.Sprintf("could not deliver notices: %v", ferr))
		}
	}
	t.logScope("scope.attach", &outScope, &outLease, "live", outScope.About)

	if o.Notify {
		text := fmt.Sprintf(`Attached to scope %s (%s) under lead %s. Report progress with: harness-ops report "<one line>" [--notify].`, outScope.Name, outScope.ID, me.Address())
		sent, merr := t.scopeOps(b, me, &outScope, &outLease).Msg(p, text, MsgOpts{})
		if sent.Typed {
			if uerr := t.Reg.Update(func(d *Data) error {
				if l := d.Leases[outLease.ID]; l != nil {
					l.Told = true
				}
				return nil
			}); uerr != nil {
				Warn(fmt.Sprintf("could not record attach notification: %v", uerr))
			} else {
				view.Told = true
			}
		}
		if merr != nil && !sent.Typed {
			return view, updated, merr
		}
	}

	return view, updated, nil
}

// DetachOpts configures Tasks.Detach.
type DetachOpts struct {
	Target string
	Keep   bool
	Notify bool
}

// Detach ends the caller's active Scope lease on a scope or pane without closing the pane.
func (t *Tasks) Detach(o DetachOpts) (*ScopeView, error) {
	me, err := t.owner()
	if err != nil {
		return nil, err
	}
	if err := t.Reconcile(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}

	var s *Scope
	var l *Lease
	callerProj := filepath.Clean(ProjectOf(me))
	if sc, serr := t.findScope(d, o.Target); serr == nil {
		s = sc
		if s.Lease != "" {
			l = d.Leases[s.Lease]
		}
	} else if errors.Is(serr, errSeveralScopes) {
		return nil, serr
	} else if ls, lerr := t.findLease(d, o.Target); lerr == nil && ls.Scope != "" {
		l = ls
		s = d.Scopes[ls.Scope]
	} else if errors.Is(lerr, errSeveral) {
		return nil, lerr
	} else {
		b, p, perr := t.paneTarget(d, o.Target)
		if perr != nil {
			return nil, term.TargetErr("no scope or attached pane matches '%s'", o.Target)
		}
		al := activeLeaseForPane(d, p.ID, b.ServerRef())
		if al == nil || al.Scope == "" {
			return nil, term.TargetErr("pane %s has no attached scope", p.Address())
		}
		l = al
		s = d.Scopes[al.Scope]
	}
	if s == nil {
		return nil, term.TargetErr("no scope matches '%s'", o.Target)
	}

	// Unbound scope case.
	if l == nil || l.State != "active" {
		if filepath.Clean(s.Project) != callerProj {
			return nil, term.TargetErr("scope %s (%s) is unbound and belongs to project %s, not %s", s.Name, s.ID, s.Project, callerProj)
		}
		if o.Keep {
			return nil, term.UsageErr("scope %s (%s) is already unbound", s.Name, s.ID)
		}
		var view *ScopeView
		if err := t.Reg.Update(func(d *Data) error {
			sc := d.Scopes[s.ID]
			if sc == nil {
				return term.TargetErr("no scope matches '%s'", o.Target)
			}
			sc.Lease = ""
			sc.Dropped = true
			view = d.ScopeView(sc.ID)
			return nil
		}); err != nil {
			return nil, err
		}
		t.logScope("scope.detach", s, nil, "dropped", "")
		return view, nil
	}

	if !t.owns(l) {
		return nil, term.TargetErr("only the lead of scope %s (%s) can detach it", s.Name, s.ID)
	}

	var b term.Backend
	var p *term.Pane
	if o.Notify {
		b, p, err = t.paneByID(l.Pane, l.Ref)
		if err != nil {
			return nil, err
		}
		if p != nil {
			if err := t.ops(b, me).CheckDraft(p, ForProcess(p.Process), false); err != nil {
				return nil, err
			}
		}
	}

	var outScope Scope
	var outLease Lease
	var view *ScopeView
	err = t.Reg.Update(func(d *Data) error {
		sc := d.Scopes[s.ID]
		cl := d.Leases[l.ID]
		if sc == nil || cl == nil || cl.State != "active" {
			return term.UsageErr("scope %s (%s) is no longer active", s.Name, s.ID)
		}
		now := t.clock().Now()
		endLease(cl, now)
		cl.Dialog = nil
		sc.Lease = ""
		sc.Dropped = !o.Keep
		outScope, outLease = *sc, *cl
		view = d.ScopeView(sc.ID)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if o.Notify && p != nil {
		text := fmt.Sprintf("Detached from scope %s (%s); you no longer report to %s.", outScope.Name, outScope.ID, me.Address())
		if sent, merr := t.scopeOps(b, me, &outScope, &outLease).Msg(p, text, MsgOpts{}); merr != nil && !sent.Typed {
			Warn(fmt.Sprintf("could not notify %s of detach: %v", p.Address(), merr))
		}
	}
	t.logScope("scope.detach", &outScope, &outLease, view.State, "")
	return view, nil
}

// recordProgress updates l.Progress, clears idle/stall marks, and optionally queues a deduped "progress" notice.
func (t *Tasks) recordProgress(d *Data, l *Lease, tag, noticeText string, text string, notify bool, now float64) error {
	l.Progress = &ProgressNote{Text: text, At: now}
	l.IdleSince, l.Stall = 0, nil
	if notify {
		keep := d.Notices[:0]
		for _, n := range d.Notices {
			if n.Kind == "progress" && n.Lease == l.ID && n.Claimed == 0 {
				continue // not being typed: the newer line replaces it
			}
			keep = append(keep, n)
		}
		d.Notices = keep
		d.Notices = append(d.Notices, &Notice{
			ID:       newNoticeID(d),
			Lease:    l.ID,
			Owner:    l.Owner,
			OwnerRef: l.OwnerRef,
			Tag:      tag,
			Kind:     "progress",
			Text:     noticeText,
			Created:  now,
		})
	}
	return nil
}

// ReportResult summarizes what Tasks.Report did.
type ReportResult struct {
	Scope     *ScopeView
	Pane      string
	Notified  bool
	OwnerGone bool
}

// Report records a one-line progress report from an attached scope (or bare supervised pane).
func (t *Tasks) Report(text string, notify bool) (*ReportResult, error) {
	me, err := t.owner()
	if err != nil {
		return nil, err
	}
	text = OneLine(text)
	if text == "" {
		return nil, term.UsageErr("report needs non-empty text")
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	cur := activeLeaseForPane(d, me.ID, t.callerRef())
	if cur == nil {
		return nil, term.TargetErr("this pane (%s) is not attached to a scope or supervised", me.Address())
	}
	if cur.Job != "" {
		if j := d.Jobs[cur.Job]; j != nil && j.Status == "running" && !j.Closed {
			return nil, term.UsageErr("this pane runs task %s; use harness-ops task progress %s --summary ...", j.ID, j.ID)
		}
	}

	ownerGone := false
	if cur.Owner != "" {
		if _, op, oerr := t.paneByID(cur.Owner, cur.OwnerRef); oerr == nil && op == nil {
			ownerGone = true
		}
	}

	var outLease Lease
	var outScope *Scope
	var sv *ScopeView
	var queuedNoticeID string
	queued := false
	err = t.Reg.Update(func(d *Data) error {
		l := d.Leases[cur.ID]
		if l == nil || l.State != "active" {
			return term.TargetErr("this pane (%s) is no longer supervised", me.Address())
		}
		if l.Job != "" {
			if j := d.Jobs[l.Job]; j != nil && j.Status == "running" && !j.Closed {
				return term.UsageErr("this pane runs task %s; use harness-ops task progress %s --summary ...", j.ID, j.ID)
			}
		}
		now := t.clock().Now()
		tag, err := t.ops(t.Backend, me).Tag()
		if err != nil {
			return err
		}
		s := d.Scopes[l.Scope]
		noticeText := fmt.Sprintf("PROGRESS pane %s: %s", me.ID, text)
		if s != nil {
			noticeText = fmt.Sprintf("PROGRESS scope %s (%s): %s", s.Name, s.ID, text)
		}
		queue := notify && !ownerGone && l.Owner != ""
		if err := t.recordProgress(d, l, tag, noticeText, text, queue, now); err != nil {
			return err
		}
		queued = queue
		if queue {
			for i := len(d.Notices) - 1; i >= 0; i-- {
				if n := d.Notices[i]; n.Kind == "progress" && n.Lease == l.ID && n.Claimed == 0 {
					queuedNoticeID = n.ID
					break
				}
			}
		}
		outLease = *l
		if s != nil {
			sc := *s
			outScope = &sc
			sv = d.ScopeView(s.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if outScope != nil {
		t.logScope("scope.report", outScope, &outLease, sv.State, text)
	} else {
		e := Entry{
			Event: "scope.report", Project: outLease.Project,
			From: address(me), FromName: entryName(me), To: outLease.Owner,
			Harness: outLease.Harness, Text: text,
		}
		t.journal().Record(e)
	}
	if queued {
		delivered, ferr := t.FlushPending()
		if ferr != nil {
			Warn(fmt.Sprintf("could not deliver report notice: %v", ferr))
		}
		needWake := ferr != nil || delivered == 0
		if !needWake && queuedNoticeID != "" {
			if d2, rerr := t.Reg.Read(); rerr == nil {
				for _, n := range d2.Notices {
					if n.ID == queuedNoticeID {
						needWake = true
						break
					}
				}
			}
		}
		if needWake {
			t.WakeWatcherFor(outLease.Owner, outLease.OwnerRef)
		}
	}
	return &ReportResult{Scope: sv, Pane: me.ID, Notified: queued, OwnerGone: ownerGone}, nil
}

// ScopeLs lists scopes: by default caller's active owned scopes + unbound/exited/lost non-dropped scopes
// in caller's project; with all: all scopes including dropped and other owners/projects.
func (t *Tasks) ScopeLs(all bool) ([]*ScopeView, error) {
	if err := t.Reconcile(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	myProj := filepath.Clean(ProjectOf(t.Sender))
	var out []*ScopeView
	for _, s := range d.Scopes {
		sv := d.ScopeView(s.ID)
		if all {
			out = append(out, sv)
			continue
		}
		l := d.Leases[s.Lease]
		if l != nil && l.State == "active" {
			if t.Sender == nil || t.owns(l) {
				out = append(out, sv)
			}
			continue
		}
		if !s.Dropped && (t.Sender == nil || filepath.Clean(s.Project) == myProj) {
			out = append(out, sv)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ScopeShow returns one scope by name or ID.
func (t *Tasks) ScopeShow(target string) (*ScopeView, error) {
	if err := t.Reconcile(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	s, err := t.findScope(d, target)
	if err != nil {
		return nil, err
	}
	return d.ScopeView(s.ID), nil
}

// ScopeTree returns scopes with descendant scopes and jobs nested under each scope's active (or
// crashed-with-orphans) lease.
func (t *Tasks) ScopeTree(all bool) ([]*TreeNode, error) {
	scopes, err := t.ScopeLs(all)
	if err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*ScopeView, len(scopes))
	for _, sv := range scopes {
		byID[sv.ID] = sv
	}
	// Map parent lease ID -> direct child leases (jobs and scopes).
	kids := map[string][]*Lease{}
	for _, l := range d.Leases {
		if l.Parent != "" {
			kids[l.Parent] = append(kids[l.Parent], l)
		}
	}
	for _, k := range kids {
		sort.Slice(k, func(i, j int) bool {
			if k[i].Started != k[j].Started {
				return k[i].Started < k[j].Started
			}
			return k[i].ID < k[j].ID
		})
	}

	// Pick the primary lease ID for each scope (active lease, or latest crashed lease with children).
	scopeLease := map[string]string{}
	scopeHasAncestorScope := map[string]bool{}
	for _, sv := range scopes {
		lid := sv.Lease
		if lid == "" {
			if latest := latestScopeLease(d, sv.ID); latest != nil && (latest.State == "exited" || latest.State == "lost") {
				lid = latest.ID
			}
		}
		if lid != "" {
			scopeLease[sv.ID] = lid
		}
	}
	for _, sv := range scopes {
		lid := scopeLease[sv.ID]
		if lid == "" {
			continue
		}
		seen := map[string]bool{lid: true}
		for p := d.Leases[d.Leases[lid].Parent]; p != nil && !seen[p.ID]; p = d.Leases[p.Parent] {
			seen[p.ID] = true
			if p.Scope != "" && byID[p.Scope] != nil && scopeLease[p.Scope] == p.ID {
				scopeHasAncestorScope[sv.ID] = true
				break
			}
		}
	}

	seenLease := map[string]bool{}
	var buildChildren func(parentLID string) []*TreeNode
	buildChildren = func(parentLID string) []*TreeNode {
		out := []*TreeNode{}
		if parentLID == "" {
			return out
		}
		for _, c := range kids[parentLID] {
			if seenLease[c.ID] {
				continue
			}
			if c.Job != "" {
				j := d.Jobs[c.Job]
				if j == nil || (!all && j.Closed) {
					continue
				}
				seenLease[c.ID] = true
				out = append(out, &TreeNode{
					Kind:     "task",
					Task:     d.TaskView(j.ID),
					Children: buildChildren(c.ID),
				})
				continue
			}
			if c.Scope != "" {
				sv := byID[c.Scope]
				if sv == nil || scopeLease[sv.ID] != c.ID {
					continue
				}
				seenLease[c.ID] = true
				out = append(out, &TreeNode{
					Kind:     "scope",
					Scope:    sv,
					Children: buildChildren(c.ID),
				})
			}
		}
		return out
	}

	roots := []*TreeNode{}
	for _, sv := range scopes {
		if scopeHasAncestorScope[sv.ID] {
			continue
		}
		lid := scopeLease[sv.ID]
		if lid != "" {
			seenLease[lid] = true
		}
		roots = append(roots, &TreeNode{
			Kind:     "scope",
			Scope:    sv,
			Children: buildChildren(lid),
		})
	}
	return roots, nil
}

// PrepareMsg returns the scope ID (for journal attribution) and the piggyback attach suffix (if the caller
// owns an untold active scope on pane p and this send is not deferred).
func (t *Tasks) PrepareMsg(b term.Backend, p *term.Pane, deferMsg bool) (scopeID, suffix string) {
	if t.Sender == nil || p == nil || t.isSelf(p.ID) {
		return "", ""
	}
	d, err := t.Reg.Read()
	if err != nil {
		return "", ""
	}
	l := t.ownsPane(d, p.ID, b.ServerRef())
	if l == nil || l.Scope == "" {
		return "", ""
	}
	if !l.Told && !deferMsg {
		if s := d.Scopes[l.Scope]; s != nil {
			return l.Scope, AttachSuffix(s.Name, t.Sender.Address())
		}
	}
	return l.Scope, ""
}

// MarkScopeTold records that an attached scope pane was told of its scope lease (after a typed send).
func (t *Tasks) MarkScopeTold(b term.Backend, p *term.Pane) error {
	if t.Sender == nil || p == nil {
		return nil
	}
	ref := b.ServerRef()
	return t.Reg.Update(func(d *Data) error {
		if l := t.ownsPane(d, p.ID, ref); l != nil && l.Scope != "" {
			l.Told = true
		}
		return nil
	})
}

// MineEntry holds ls --mine metadata for one pane owned by the caller.
type MineEntry struct {
	Pane    *term.Pane
	Dialog  string
	Scope   string
	Stalled bool
}

// MineInfo filters panes to those owned by the caller and attaches open dialog, scope, and stall state.
func (t *Tasks) MineInfo(panes []*term.Pane) ([]MineEntry, error) {
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	ref := t.Backend.ServerRef()
	var out []MineEntry
	for _, p := range panes {
		l := t.ownsPane(d, p.ID, ref)
		if l == nil {
			continue
		}
		e := MineEntry{Pane: p, Stalled: l.Stall != nil}
		if l.Dialog != nil {
			e.Dialog = l.Dialog.Fingerprint
		}
		if l.Scope != "" {
			if s := d.Scopes[l.Scope]; s != nil {
				e.Scope = s.Name
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// SupervisionLine returns the caller's active supervision summary for whoami, or "" if unsupervised.
func (t *Tasks) SupervisionLine() string {
	if t.Sender == nil {
		return ""
	}
	d, err := t.Reg.Read()
	if err != nil {
		return ""
	}
	l := activeLeaseForPane(d, t.Sender.ID, t.callerRef())
	if l == nil {
		return ""
	}
	if l.Scope != "" {
		if s := d.Scopes[l.Scope]; s != nil {
			return fmt.Sprintf(`scope: %s (%s) · lead: %s · report with: harness-ops report "<one line>"`, s.Name, s.ID, l.Owner)
		}
	}
	if j := d.jobOf(l); j != nil && !j.Closed {
		return fmt.Sprintf("task: %s (%s) · lead: %s", j.ID, j.Title, l.Owner)
	}
	return ""
}
