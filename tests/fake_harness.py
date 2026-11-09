"""A tiny harness REPL for acceptance tests.

Prints `> `, logs each received line, treats exactly `/clear` as a clear
(takes FAKE_CLEAR_DELAY seconds and prints a marker before the next prompt).
"""
import os
import sys
import time

log = sys.argv[1]
delay = float(os.environ.get("FAKE_CLEAR_DELAY", "1.5"))
open(log, "a").close()
print("fake harness ready", flush=True)
while True:
    try:
        line = input("> ")
    except EOFError:
        break
    with open(log, "a") as f:
        f.write(line + "\n")
    if line == "/clear":
        print("clearing...", flush=True)
        time.sleep(delay)
        print("<<CLEARED>>", flush=True)
    else:
        print(f"got: {line[:40]}", flush=True)
