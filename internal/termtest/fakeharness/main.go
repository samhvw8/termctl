// fakeharness is a tiny REPL for acceptance tests: it prints "> ", logs each line it
// receives, and treats exactly "/clear" as a slow clear that ends with a marker.
//
// With FAKE_OSC7501=1 it reports status the way a Herdr-aware agent does (OSC 7501):
// idle at the prompt, working during /clear, and /block, /done, /error set those words
// until the next line arrives. With FAKE_TRAP_INT=1 it logs and survives Ctrl-C.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"time"
)

func report(state string) {
	if os.Getenv("FAKE_OSC7501") == "1" {
		fmt.Printf("\x1b]7501;state=%s\x07", state)
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
		default:
			if len(line) > 40 {
				line = line[:40]
			}
			fmt.Println("got:", line)
		}
	}
}
