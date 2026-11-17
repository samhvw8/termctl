// fakeharness is a tiny REPL for acceptance tests: it prints "> ", logs each line it
// receives, and treats exactly "/clear" as a slow clear that ends with a marker.
//
// With FAKE_OSC7501=1 it reports status the way a Herdr-aware agent does (OSC 7501):
// idle at the prompt, working during /clear, and /block, /done, /error set those words
// until the next line arrives; while /dialog waits it reports blocked, kind=permission, with
// the request as msg. With FAKE_TRAP_INT=1 it logs and survives Ctrl-C. With FAKE_ECHO=full
// it echoes each line whole ("got: <line>"), not cut to 40 characters, and takes lines of any length.
//
// "/dialog" draws an Antigravity CLI permission dialog (as captured from agy 1.5.10) and reads keys
// unbuffered: up and down move the selection, Enter answers and logs "<<ANSWER n>>", a lone Esc cancels
// and logs "<<CANCELLED>>". Run under the
// name "agy", the pane reads as agy and harness-ops reads the dialog with agy's parser.
package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

func report(state string) {
	if os.Getenv("FAKE_OSC7501") == "1" {
		fmt.Printf("\x1b]7501;state=%s\x07", state)
	}
}

// reportBlocked is OSC 7501's blocked report with its reason: pairs split by ":", msg in base64.
func reportBlocked(msg string) {
	if os.Getenv("FAKE_OSC7501") == "1" {
		fmt.Printf("\x1b]7501;state=blocked:kind=permission:msg=%s\x07", base64.StdEncoding.EncodeToString([]byte(msg)))
	}
}

func main() {
	logPath := os.Args[1]
	delay := 1.5
	if v, err := strconv.ParseFloat(os.Getenv("FAKE_CLEAR_DELAY"), 64); err == nil {
		delay = v
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	if os.Getenv("FAKE_TRAP_INT") == "1" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		go func() {
			for range c {
				fmt.Fprintln(f, "<<SIGINT>>")
				f.Sync()
				fmt.Print("<<INTERRUPTED>>\n> ")
			}
		}()
	}
	lineMode()
	fmt.Println("fake harness ready")
	in := bufio.NewReaderSize(os.Stdin, 1<<16)
	status := "idle"
	for {
		fmt.Print("> ")
		report(status)
		status = "idle"
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		line = line[:len(line)-1]
		fmt.Fprintln(f, line)
		f.Sync()
		switch line {
		case "/clear":
			report("working")
			fmt.Println("clearing...")
			time.Sleep(time.Duration(delay * float64(time.Second)))
			fmt.Println("<<CLEARED>>")
		case "/block":
			status = "blocked"
			fmt.Println("waiting for a person")
		case "/done":
			status = "done"
		case "/error":
			status = "error"
		case "/dialog":
			reportBlocked("Run touch fake-file?")
			n := dialog(in)
			lineMode()
			if n < 0 {
				fmt.Fprintln(f, "<<CANCELLED>>")
				f.Sync()
				fmt.Println("cancelled")
				break
			}
			fmt.Fprintf(f, "<<ANSWER %d>>\n", n)
			f.Sync()
			fmt.Printf("answered %d\n", n)
		default:
			if os.Getenv("FAKE_ECHO") == "full" {
				fmt.Println("got:", line)
				continue
			}
			if len(line) > 40 {
				line = line[:40]
			}
			fmt.Println("got:", line)
		}
	}
}

var dialogOptions = []string{"1. Yes, run command",
	"2. Yes, and always allow in this conversation for commands that start\nwith 'touch'",
	"3. No, cancel"}

func drawDialog(selected int) {
	fmt.Print("\x1b[H\x1b[2J")
	fmt.Print("Command\n" + strings.Repeat("─", 72) + "\n\nRequesting permission for:\n   touch fake-file\n\nRun this command?\n")
	for i, o := range dialogOptions {
		mark := "  "
		if i+1 == selected {
			mark = "> "
		}
		fmt.Print(mark + o + "\n")
	}
	fmt.Print("\n  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command\n")
	fmt.Print("esc to cancel                          auto · Fake · low")
}

// lineMode lets a whole-line echo fake (a lead in tests) take lines longer than the terminal's canonical
// line limit (1024 bytes on macOS), which a long notice can pass: it reads without the line discipline's
// editing; echo stays on.
func lineMode() {
	if os.Getenv("FAKE_ECHO") == "full" {
		stty("-icanon", "min", "1")
	}
}

func stty(args ...string) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	cmd.Run()
}

// dialog shows the dialog until Enter and returns the choice, or -1 for a lone Esc. An arrow key comes as
// one write ("\x1b[A"), so an Esc with nothing buffered after it is the Esc key.
func dialog(in *bufio.Reader) int {
	stty("-icanon", "-echo", "min", "1")
	defer stty("icanon", "echo")
	fmt.Print("\x1b[?1049h")       // the alternate screen, as a full-screen TUI draws it
	defer fmt.Print("\x1b[?1049l") // answered: the dialog is gone, the earlier screen is back
	selected := 1
	for {
		drawDialog(selected)
		b, err := in.ReadByte()
		if err != nil {
			return 0
		}
		switch b {
		case '\r', '\n':
			return selected
		case 0x1b:
			if in.Buffered() == 0 {
				return -1
			}
			if next, _ := in.ReadByte(); next == '[' || next == 'O' {
				switch k, _ := in.ReadByte(); k {
				case 'A':
					if selected > 1 {
						selected--
					}
				case 'B':
					if selected < len(dialogOptions) {
						selected++
					}
				}
			}
		}
	}
}
