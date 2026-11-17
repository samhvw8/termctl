---
type: Architecture
title: The journal
description: harness-ops' long-term log of every message and task event, the project folders for long texts, and the log, resume and note commands that read and write them.
tags: [architecture, harness, journal, resume]
generated: { by: claude-code/opus-5.5, at: '2026-11-17T12:10:00Z' }
---

# Why

Before the journal, harness-ops kept its history in three places, and none of them was a log: long messages in `~/.cache/agent-ops/<time>-<from>-to-<to>.md` (a cache folder, no project in the name), task briefs in `<state>/briefs/<id>.md` (no project, no time), and `registry.json` (only the current state; each change overwrote the last). Short messages were typed into the pane and kept nowhere. A lead's job briefs were named by hand, against the skill's rule. So a new session could not tell what had been asked, by whom, or when. Sam asked for one place to resume from (2026-11-17).

# Layout

```text
<state>/                         TERMCTL_STATE_DIR, else ~/.local/state/termctl
├── registry.json                current task state (unchanged)
├── journal.jsonl                one JSON line per action, appended, never rewritten
├── journal.lock                 flock around each append
└── projects/<project>/          long messages, prompts, briefs and notes
    ├── 1794845342-msg-6A617B57-to-81F463EF.md
    ├── 1794845390-brief-t0486f8.md
    ├── 1794845400-prompt-spawn-to-%3.md
    └── 1794846001-note-job-plan.md
```

- **`<project>`** is the omp form of the folder, without its leading dash (`ProjectSlug`): the path below home with `/` turned into `-` (`~/workspace/tools/termctl` is `workspace-tools-termctl`), `home` for home itself, and the whole path for a folder outside home (`/private/tmp` is `private-tmp`). A leading dash would make the name an option to `rm` and `cat`.
- **The project** of an action is the sender pane's folder (`ProjectOf`), else the working directory. A task stores its owner's folder at `task new` (`Task.Project`), and every event of the task, including the worker's `task done` from another folder, is filed there.
- **Every file starts with one header line**, then a blank line: `<project path> · <unix time> · <local time> (<zone>)`, for example `/Users/samhv/workspace/tools/termctl · 1794845342 · 2026-11-16 23:09:02 +07 (Asia/Ho_Chi_Minh)`. The zone is `TZ`, else the `/etc/localtime` link, else the abbreviation. File names start with the Unix time, so they sort in time order; a name taken in the same second gets `-2`, `-3`; a file is never overwritten.

# Entries

`Entry` in `internal/harness/journal.go`: `t` (Unix seconds), `time` (RFC 3339, local), `zone`, `event`, `project`, `from`/`from_name`, `to`/`to_name`, `task`, `harness`, `status`, `text`, `file`, `error`.

| Event | Written by | Holds |
|---|---|---|
| `msg` | `Ops.Msg` (msg, task send, a delivered report) | the full text, or the first 200 characters and `file` for a long one; `status: queued` when the target was working; `error` when the submit failed |
| `spawn` | `Ops.Spawn` | the harness and the first prompt (or its file) |
| `slash`, `interrupt` | `Ops.Slash` (also each `/clear`), `Ops.Interrupt` | the command, the keys |
| `close` | `Tasks.ClosePane` | `status: forced` with `--force` |
| `task.new` | `Tasks.New` | the title, the brief's `file`; `status: failed` and the cause when the start failed |
| `task.done` | `Tasks.Done` | the full summary; `to` is the owner |
| `task.accept`, `task.close`, `task.exited` | `Tasks` | the status, the exit code |
| `note` | `harness-ops note` | the start of the text and its `file` |

Rules:

- **An action never fails because of the journal**, and never loses an entry quietly: a failed append prints `harness-ops: warning: not recorded in the journal`.
- **Appends are whole lines under `flock`.** `TestConcurrentAppendsStayWhole` runs 16 writers of 200 KB lines.
- **A report that waits for the owner's draft** (`FlushPending`, retried) is journaled once, when it is delivered: `MsgOpts.Retry` keeps failed tries out.
- **The journal is live data** in the live state directory: never delete it. `state clean` removes it only from a `TERMCTL_STATE_DIR`.

# Commands

- `harness-ops log [--project DIR] [--task ID] [--pane ID] [--since 2h|3d|DATE] [-n N] [--json]`: the timeline, oldest first, the last 50 by default. One row per entry: local time, project (when not filtered by project or task), event, `from → to`, task, status, text on one line, file. Unreadable lines are counted on stderr, never hidden.
- `harness-ops resume TASK-ID`: the task's state and last report, its brief in full, then every journal entry with its whole text; a long message's file is printed under its entry. `resume DIR` (`.` is the caller's project, the folder its pane started in, which is where its writes go; a shell that changed folder does not change it): the project's tasks that are not closed, its notes, and its last 30 entries (`-n`).
- `harness-ops note WHAT (TEXT... | --file F | stdin)`: files a text in the caller's project folder, journals it, and prints the path. Leads write job briefs with it instead of naming files by hand.

# Not done

- **No import** of the files written before the journal: their names hold no project, so an import would guess. They stay where they are, and the registry still points at the old briefs.
- **No rotation.** At the rate of 2026-11-10 to 2026-11-17 (about 1 MB of messages a week in `~/.cache/agent-ops`) the journal grows by a few MB a month; long texts are in files, so lines stay short.

Checked: unit tests (`journal_test.go`, `internal/cli/journal_test.go`) and real tmux (`TestTaskNewDoneNotifyClose` runs `log --task` and `resume` after a two-round task and checks the events in order and every text).
