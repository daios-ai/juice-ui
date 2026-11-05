# supervisor

The process supervisor: it runs an installation's programs as its children, restarts
them when they end, and stops them when asked or when it closes. The UI embeds it;
`juice-supervisor` runs it from the command line on a machine with no UI. It imports
only the standard library and knows nothing of Juice, so moving it to a repository
of its own is moving this directory and `cmd/juice-supervisor`.

## A program

A program runs in the foreground and ends on SIGTERM. Nothing else is asked of it.

It is one file, `$JUICE_HOME/supervisor/programs/<name>.json`. Only `command` is
required:

```json
{"command": ["/opt/weather/server", "--port", "9100"]}
```

| Key | Default | Meaning |
|---|---|---|
| `command` | required | The executable and arguments, with no shell. A bare name is looked up in `$JUICE_HOME/bin`, then `PATH`. |
| `env` | `{}` | Variables added to the supervisor's environment. |
| `dir` | the supervisor's | The working directory. |
| `restart` | `"always"` | `"always"` or `"no"`: whether it is started again when it ends by itself. |
| `disabled` | `false` | It must not run. |
| `stop_seconds` | `30` | The time between SIGTERM and SIGKILL. |
| `lock` | none | The program's own lock file, for one that holds such a lock with its process id in it. A process holding it is stopped before the program starts. |

A key the schema (`schemas/juice-supervisor.schema.json`) does not know refuses that
program, which is reported; the others run.

The UI writes its own programs' files once, when they are absent. After that they are
the operator's. The kernel, an agent and chat, as it writes them:

```json
{"command": ["juice", "kernel", "serve", "play"],
 "env": {"JUICE_HOME": "/home/ana/.juice"},
 "lock": "/home/ana/.juice/kernels/play/serve.lock"}

{"command": ["juice-agent", "serve", "assistant@alpha"],
 "env": {"JUICE_HOME": "/home/ana/.juice"},
 "lock": "/home/ana/.juice/agents/assistant@alpha/serve.lock"}

{"command": ["juice-chat", "serve"],
 "env": {"JUICE_HOME": "/home/ana/.juice"},
 "lock": "/home/ana/.juice/services/chat/serve.lock"}
```

## The command

```
juice-supervisor              run the programs until SIGTERM or SIGINT, then stop them
juice-supervisor --version
```

SIGHUP reads the files again and acts on what changed. Under the UI, an edited file
takes effect at the next start of the UI.

## Behaviour

- **Start.** Each program that is not disabled is a child in its own process group.
- **Output.** Written to `supervisor/log/<name>.log` through a pipe. At 10 MiB the file
  becomes `<name>.log.1` and a new one begins, so a program's logs never exceed 20 MiB.
- **End.** A program that ends by itself is started again after 1 s, doubling to 60 s,
  back to 1 s after a run of 60 s, indefinitely. So no start order is needed: an agent
  started before its kernel answers fails and is retried.
- **Stop.** SIGTERM to the group, then SIGKILL to whatever remains of it after
  `stop_seconds`, subprocesses included. A program counts as ended only when its group
  has, and one that ended by itself has its remaining subprocesses stopped the same way
  before it is started again.
- **Close.** Every program is stopped at once, and the supervisor waits for them.
- **One supervisor per installation.** `supervisor/supervisor.lock`; a second is refused.

## When a supervisor is killed

Its programs are left running. The next supervisor stops them before it starts
anything, using `supervisor/run/<name>`, which holds each child's id and start time as
`ps` reports it, and a program's own `lock`.

This is best effort, as it is for every tool that stops a process it did not start.
The limits:

- A start time has whole seconds, so an id reused within the second of the original
  start would pass for it.
- The check and the signal are two steps, and no portable call closes that gap for a
  process that is not one's child.
- With no way to read a start time (no `ps`), nothing is signalled and the record goes.
- A supervisor killed between starting a child and writing its record leaves no record.
  A program with a `lock` is still found by it. One without stays until stopped by hand,
  and its replacement is retried, for instance while it cannot bind its port.
- If a program has ended and processes it started remain, they are not found.

A program also loses its output pipe when the supervisor dies, and a Go program then
ends at its next write to it.
