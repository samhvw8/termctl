// fakeharness is a tiny REPL for acceptance tests: it prints "> ", logs each line it
// receives, and treats exactly "/clear" as a slow clear that ends with a marker.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"time"
)

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
	fmt.Println("fake harness ready")
	in := bufio.NewReaderSize(os.Stdin, 1<<16)
	for {
		fmt.Print("> ")
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		line = line[:len(line)-1]
		fmt.Fprintln(f, line)
		f.Sync()
		if line == "/clear" {
			fmt.Println("clearing...")
			time.Sleep(time.Duration(delay * float64(time.Second)))
			fmt.Println("<<CLEARED>>")
		} else {
			if len(line) > 40 {
				line = line[:40]
			}
			fmt.Println("got:", line)
		}
	}
}
