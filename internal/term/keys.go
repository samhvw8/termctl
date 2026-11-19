package term

import (
	"regexp"
	"strings"
)

// Key names. NormalizeKey turns any spelling a caller may use into one of these; each backend maps the
// ones it can send (tmux key names, iTerm2 bytes, Rex and hc CLI names) and refuses the rest by name.
// A plain digit is never a key: on Claude Code a digit answers a permission dialog at once.
var keyNames = []string{"enter", "escape", "tab", "shift-tab", "backspace", "delete", "space",
	"up", "down", "left", "right", "home", "end", "pageup", "pagedown"}

var (
	ctrlKey = regexp.MustCompile(`^ctrl-[a-z]$`)
	altKey  = regexp.MustCompile(`^alt-[a-z0-9]$`)
	fnKey   = regexp.MustCompile(`^f([1-9]|1[0-2])$`)
)

// altAliases are other names for the alt modifier (Option on a Mac keyboard, Meta in Emacs and tmux).
var altAliases = []string{"meta-", "option-"}

// NormalizeKey reads a key in any case, with "-" or "+" between modifier and key (alt+m is alt-m), and
// with meta- or option- for alt-. It returns the canonical name, or a usage error that lists the names.
func NormalizeKey(key string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(key))
	if len(k) > 1 {
		k = strings.ReplaceAll(k, "+", "-")
	}
	for _, a := range altAliases {
		if strings.HasPrefix(k, a) {
			k = "alt-" + k[len(a):]
		}
	}
	for _, n := range keyNames {
		if k == n {
			return k, nil
		}
	}
	if ctrlKey.MatchString(k) || altKey.MatchString(k) || fnKey.MatchString(k) {
		return k, nil
	}
	return "", UsageErr("unknown key '%s'. Use one of: %s, f1-f12, ctrl-<letter>, alt-<letter or digit> "+
		"(meta- and option- mean alt-; + works as well as -). A plain digit is not a key: it would answer a dialog at once",
		key, strings.Join(keyNames, ", "))
}

// KeyChecker is a backend that can tell, before anything is sent, whether it can send a key. Every
// backend that sends keys has it; a key it cannot send is refused by name, never approximated.
type KeyChecker interface {
	CheckKey(key string) error
}

// CheckKeys normalizes every key and checks that b can send each one, so a list is refused before its
// first key is pressed.
func CheckKeys(b Backend, keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		k, err := NormalizeKey(key)
		if err != nil {
			return nil, err
		}
		if c, ok := b.(KeyChecker); ok {
			if err := c.CheckKey(k); err != nil {
				return nil, err
			}
		}
		out = append(out, k)
	}
	return out, nil
}

func cannotSendKey(backend, key string) error {
	return UsageErr("the %s backend cannot send key '%s'; no approximation is sent", backend, key)
}
