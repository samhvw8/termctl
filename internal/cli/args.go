// Package cli holds the termctl and harness-ops command lines.
package cli

import (
	"strconv"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

type parsed struct {
	bools map[string]bool
	vals  map[string]string
	pos   []string
	rest  []string
}

func has(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// parseArgs reads flags and positionals. With restAfter >= 0, everything after that many
// positionals is kept raw: message text may look like flags.
func parseArgs(argv, bools, values []string, restAfter int) (*parsed, error) {
	values = append(append([]string{}, values...), "--backend", "-L")
	p := &parsed{bools: map[string]bool{}, vals: map[string]string{}}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		// With restAfter 0 the raw words start at the first non-flag: flags before a command are flags.
		if restAfter >= 0 && len(p.pos) == restAfter && (restAfter > 0 || !strings.HasPrefix(arg, "-")) {
			p.rest = append([]string{}, argv[i:]...)
			return p, nil
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			p.pos = append(p.pos, arg)
			continue
		}
		name, val, inline := strings.Cut(arg, "=")
		switch {
		case has(bools, name) && !inline:
			p.bools[name] = true
		case has(values, name):
			if !inline {
				i++
				if i >= len(argv) {
					return nil, term.UsageErr("%s needs a value", name)
				}
				val = argv[i]
			}
			p.vals[name] = val
		default:
			return nil, term.UsageErr("unrecognized argument: %s", arg)
		}
	}
	return p, nil
}

func (p *parsed) float(name string, def float64) (float64, error) {
	v, ok := p.vals[name]
	if !ok {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, term.UsageErr("%s must be a number, got %q", name, v)
	}
	return f, nil
}

func (p *parsed) int(name string, def int) (int, error) {
	v, ok := p.vals[name]
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, term.UsageErr("%s must be a whole number, got %q", name, v)
	}
	return n, nil
}

// splitDoubleDash takes the literal tail after the first `--`.
func splitDoubleDash(argv []string) ([]string, []string, bool) {
	for i, a := range argv {
		if a == "--" {
			return argv[:i], argv[i+1:], true
		}
	}
	return argv, nil, false
}

// attachLiteral adds the tail after `--` to a command's raw words.
func attachLiteral(rest, tail []string, hadDash bool) ([]string, bool) {
	if !hadDash {
		return rest, false
	}
	if len(rest) > 0 { // `--` inside the message is part of the message
		return append(append(rest, "--"), tail...), false
	}
	return tail, true
}

// globals strips leading --backend/-L, returning the subcommand argv.
func globals(argv []string, o *Options) ([]string, error) {
	for len(argv) > 0 {
		name, val, inline := strings.Cut(argv[0], "=")
		if name != "--backend" && name != "-L" {
			break
		}
		if !inline {
			if len(argv) < 2 {
				return nil, term.UsageErr("%s needs a value", name)
			}
			val, argv = argv[1], argv[1:]
		}
		if name == "--backend" {
			o.Backend = val
		} else {
			o.Socket = val
		}
		argv = argv[1:]
	}
	return argv, nil
}

func applyOptions(p *parsed, o *Options) error {
	if v, ok := p.vals["--backend"]; ok {
		o.Backend = v
	}
	if v, ok := p.vals["-L"]; ok {
		o.Socket = v
	}
	if o.Backend != "" && !term.IsBackend(o.Backend) {
		return term.UsageErr("unknown backend '%s'. Known: %s", o.Backend, strings.Join(term.BackendNames, ", "))
	}
	return nil
}
