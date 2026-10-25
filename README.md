# Runnel — Stage 4

A native macOS workspace with Home, local projects and retained terminal sessions.
Each session contains up to four independent terminals in a nested split tree.
Switch sessions, close the window or quit the GUI, then reopen it to continue the
same shells, jobs, terminal state, layout and focused pane. State lives in the
daemon's memory; restarting the daemon does not restore it.

Requires macOS 13+, Xcode Command Line Tools (`xcode-select --install`), and Go
with toolchain auto-download support. **Go 1.27.1** and **MyGO v0.6.3** remain pinned,
including the CLI and SHA-256-verified libghostty-vt. MyGO is unmodified. `go.sum`
locks dependencies. No Node, Zig or webview is needed.

```sh
make setup       # download pinned toolchain/modules
make dev         # build, detach only the old GUI, launch the new GUI
make build       # package for the host architecture
make test vet    # package, run bounded regressions, then go vet
make verify      # build and check the packaged GUI control endpoint
```

Apple Silicon: `dist/darwin-arm64/Runnel.app`; Intel: `dist/darwin-amd64/Runnel.app`.
Open the bundle in Finder, or:

```sh
open -n "dist/darwin-$(./script/go.sh env GOARCH)/Runnel.app"
# Optional universal build; Intel execution remains unverified:
./script/build.sh -platform darwin/universal
```

## Workspace and controls

Home is permanent. Open Project registers/selects a directory without starting a
shell. Git is optional. Projects have opaque IDs and are identified by filesystem
directory identity, resolving symlink aliases. Same-name folders remain distinct;
full roots appear in sidebar labels/tooltips. Missing, inaccessible or replaced
roots report errors, including at session creation. Removing an empty project
removes only metadata. Opening a moved directory updates its path when its identity
is unchanged.

| Action | Control |
| --- | --- |
| Open Project | Cmd+O / Open Project… |
| New Session in selected group | Cmd+T / New Session |
| Split focused terminal to the right | Cmd+D / File → Split Right |
| Split focused terminal below | Cmd+Shift+D / File → Split Down |
| Next / previous pane in visual tree order | Cmd+] / Cmd+[ |
| Focus a terminal | Click its terminal or pane header |
| Resize panes | Drag a divider; a focused divider also accepts arrow keys |
| Close one terminal and its owned processes | Its **Close Pane** button / File → Close Pane |
| End every pane in the selected session | **End Session** |
| Rename the session | Rename Session… |
| Copy / paste / Clear in the focused terminal | Cmd+C / Cmd+V / Cmd+K; native Edit/context menus |
| Detach the GUI | Cmd+W, window close or Cmd+Q |

New Session creates exactly one pane. Splitting starts a fresh login shell in the
session's **initial directory**, with the account's `HOME` preserved. A shell's
`cd`, title or OSC directory reports never change session membership or where a
later split starts. Initial-directory access and identity are rechecked before
splitting. Current-directory inheritance is deferred.

Only the focused terminal receives keyboard input and paste. Native context menus
act on their own terminal. Rename fields and directory dialogs own focus while
open; terminal callbacks from a retired attachment are rejected. Each pane has a
header with a focus indicator, lifecycle/actual exit status, and Close Pane.
Closing a nonfocused pane preserves focus. Closing the focused pane chooses the
next surviving leaf in visual order, or the preceding leaf when it was last.
The sibling of a removed leaf takes its parent's space. Closing the final pane
ends that session. Close Pane and End Session discard the corresponding retained
output; GUI/window close preserves it.

The window minimum is 640×400 DIPs. The terminal area fills the viewport; a native
scroll area exposes the full split canvas when a small window or an extreme ratio
cannot fit all headers and terminal rows. Resizes update every affected PTY to its
acknowledged terminal grid. Dividers use MyGO's existing percentage split widgets,
with 10–90% ratios. Drag updates are coalesced at roughly 60 ms; switching or closing
submits the final pending ratio. Older replies cannot restore an obsolete ratio.

Selecting a session attaches all its visible panes. Selecting a group leaves an
empty view with New Session available. The daemon remembers selection and each
session's focused pane. Ending the selected session chooses the oldest non-ending
session in its group, then the oldest anywhere; otherwise its group stays selected.
Explicit group selection stays empty. Reopening reconstructs the complete split
tree with the same pane IDs and processes. There is no disk restoration or eviction.

## Model and lifecycle

A session owns stable identity, project membership, display name, initial directory,
layout revision, split tree and focused pane. A leaf references one stable opaque
pane ID. An internal node has its own stable ID, axis, finite ratio and exactly two
children. Layout operations path-copy the tree and validate targets, revision,
ratios, depth and retained capacity before changing ownership. A failed split leaves
the existing tree/processes intact and closes any partially created resources.

A pane owns one PTY, authoritative terminal, shell/process lifecycle and ordered
actor. Exit codes belong to panes only. Natural exit retains output and the actual
code, including `128 + signal`; a running pane has no exit code. A session is running
while any retained pane is running, exited once all are exited, and ending during
final cleanup (also transiently when only exited/ending panes remain). There is no
shared session exit code. Connection loss is distinct from shell exit.

Close Pane initiates cleanup only for that pane; End Session initiates all its pane
cleanups concurrently. Both acknowledge initiation, while catalog updates report
completion. Ending panes and sessions consume their retained slots until cleanup
finishes. Catalog actions and sibling/session actors continue during cleanup.

Cleanup sends SIGHUP and SIGCONT to processes owned by that private controlling
terminal, closes the PTY, escalates survivors after 400 ms, and allows two seconds
plus 500 ms for shell reaping. PID start timestamps protect recycled PIDs. Processes
that deliberately leave the controlling terminal/session are outside ownership.
Normal daemon shutdown ends all retained panes concurrently. Daemon death, reboot
and an uncatchable daemon SIGKILL have no restoration/cleanup guarantee.

Window close, GUI SIGTERM, crashes and quit detach. One GUI workspace is allowed
per runtime; a replacement catalog connection replaces its presentation ownership.
A broken catalog displays Disconnected. Reconnect contacts the existing daemon;
Reattach Pane opens only that existing pane. Neither creates processes or starts a
replacement daemon. A lost create/split response can leave an unknown outcome:
reconnect and inspect the catalog before explicitly trying again. Creation is never
automatically retried.

Selection drag, Shift+drag when a TUI takes the mouse, wheel/trackpad and
Shift+Page Up/Down retain MyGO's behavior. Multiline paste is bracketed when
requested; Enter executes it at a shell. Clear changes the authoritative terminal,
so cleared history stays cleared on reopening. Alternate-screen programs are left
alone. Ordered frames preserve cursor, screen and modes. Mirror sends hidden
main-screen history after leaving the alternate screen.

Finder startup reads the account's login shell from Directory Services and starts
it with `-l`. Each child gets its own cwd and `PWD`; account `HOME` stays intact.
`USER`, `LOGNAME`, `SHELL`, `TERM=xterm-256color`, true color and a UTF-8 locale are
normalized as in Stage 2. Existing PATH/locale survive; missing PATH gets system
paths. Runnel never changes daemon cwd or edits shell startup files.

## Protocol, scheduling and limits

`daemon_registry.go` owns sessions, projects, revisions and stream ownership.
`layout.go` implements immutable tree operations. `daemon_pane.go` gives each pane a
bounded ordered event loop; `pane.go` owns its terminal/PTY pair.
`workspace_connection.go` carries catalog traffic independently of `connection.go`'s
pane streams. `app.go` schedules attachment batches and selection;
`pane_view.go` renders the tree recursively with stable keys and persistent handles.
`terminal_view.go` retains the standalone terminal view used by earlier regressions.

Protocol **4** uses four-byte big-endian JSON lengths. No operation precedes a
successful hello/welcome negotiation. A catalog welcome returns the ownership token,
revisioned catalog and daemon PID. Operations are `list`, `open-project`,
`remove-project`, `create`, `select`, `rename`, `end`, `split`, `close-pane`, `focus`,
`ratio`, and internal `shutdown-empty`. Layout/focus mutations require the current
session layout revision. Results correlate requests and include a complete catalog;
replaceable catalog snapshots are coalesced separately from ordered replies.

A terminal hello requires **session ID, pane ID, owner token and increasing attachment
generation**. Every terminal mutation, heartbeat, frame, lifecycle notification and
flush acknowledgement identifies both session and pane. A foreign pane, mismatched
command, stale generation or retired peer is rejected. Opening a sibling does not
displace any other pane; changing focus never reconnects or restarts one.

The GUI has one coalesced pending attachment batch and one serial handshake worker.
Workspace/selection generations, live catalog membership and pane identity guard
installation. Late successes are retired and late failures cannot replace a current
pane. Switching seals all old streams against callbacks, concurrently requests a
bounded FIFO flush of accepted input/resize/Clear, and waits off the UI thread before
opening replacements. Final divider mutations use the independent control worker.
Socket work and drag requests do not block interactive rendering.

Each pane FIFO orders accepted PTY output, input, resize, theme, Clear and frame
generation. Queries are answered by its authoritative terminal even while detached.
MyGO `ResizeByConn` applies each acknowledged grid before its frame bytes; synchronized
presentation updates prevent partial initial painting. Unselected/detached panes have
no presentation Mirror or render timer. Noisy producers cannot fill another pane's
queue or the registry with PTY output. Slow streams are disconnected independently.

| Resource | Limit |
| --- | --- |
| Retained sessions | 32, including exited/ending |
| Retained panes per session / total | **4 / 64**, including exited/ending |
| Split depth | 3 edges from root (four leaves maximum) |
| Ratio | finite, 0.1–0.9 inclusive |
| Open projects | 64, plus Home |
| Scrollback | approximately 512 KiB per terminal; oldest rows evict |
| Grid / cell pixels | 2–500 columns, 2–300 rows; 1–128 pixels per dimension |
| PTY output / input chunks | 32 KiB |
| Registry / each pane FIFO | 64 events each |
| PTY input / GUI pane input / control worker | 64 chunks/actions each |
| Pending GUI attachments | one batch of at most four, plus one in progress |
| Peer output | two ordered packets; catalog additionally has one replaceable snapshot |
| Accepted connections, including handshakes | **eight** |
| Incoming commands / outgoing packets | 64 KiB / 16 MiB |
| Handshake / write / retirement flush deadline | three seconds |
| Heartbeat / idle deadline | 15 / 45 seconds |
| Workspace response wait | six seconds; disconnect on timeout |

Four visible streams plus one catalog leave three connection slots for handshakes
and retirement. Session switches flush/close the old batch before opening the next.
Admission reserves a slot before accept, allowing queued replacement handshakes to
wait for retiring handlers instead of receiving a transient EOF. OS socket backlog
and handshake deadlines bound waiting. Deliberately stalled clients can occupy the
eight slots until their deadlines. Retained-pane overflow is rejected before spawn.
There are no terminal images or unbounded frame/input queues.

The private runtime is `/private/tmp/runnel-<uid>/`, mode 0700; sockets/locks are
0600. A permanent-inode kernel `flock` spans daemon lifetime. A responding socket is
never removed; refusal/stale socket removal requires the daemon lock. The internal
runtime override isolates verification; it is not a public session CLI.

## Packaging, development and protocol transition

The builder bundles the pinned dylib under `Contents/Resources` and ad hoc signs
it and the app. Both roles derive the library path from their executable before
creating a terminal, overriding inherited `MYGO_GHOSTTY_VT`. A missing bundled
library is an error; packaged apps never download a fallback. Daemon dispatch
precedes GUI initialization and needs no GUI event loop.

**Stage 4 never kills or replaces a live incompatible daemon.** Keep the compatible
app available, save work there, and explicitly end its sessions before upgrading.
Stage 2/protocol 2 End Session stopped that daemon. **Stage 3/protocol 3 End Session
does not stop its daemon, even after ending every session.** It leaves an empty
workspace. Rebuilding or deleting a live socket is not a migration.

For an old Stage 3 runtime: use its compatible app to save/end every session, quit
that GUI, then explicitly shut down the verified empty protocol-3 runtime. The
following opt-in maintenance command checks private ownership, excludes a GUI via
its lock, negotiates exactly protocol 3, requires zero retained sessions and verifies
the returned PID's daemon command before SIGTERM. An incompatible or nonempty runtime
is left running. Set the runtime path explicitly; this command is **not** part of
`make dev` or tests.

```sh
RUNNEL_STAGE3_RUNTIME="/private/tmp/runnel-$(id -u)" python3 -I - <<'PY'
import os, stat, fcntl, socket, struct, json, subprocess, signal
root = os.environ['RUNNEL_STAGE3_RUNTIME']
for path, kind, mode in [(root, stat.S_ISDIR, 0o700),
                          (root+'/session.sock', stat.S_ISSOCK, 0o600)]:
    s = os.lstat(path)
    assert kind(s.st_mode) and stat.S_IMODE(s.st_mode) == mode and s.st_uid == os.getuid()
f = os.open(root+'/gui.lock', os.O_RDWR | os.O_NOFOLLOW)
fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)  # compatible GUI must be closed
c = socket.socket(socket.AF_UNIX); c.settimeout(3); c.connect(root+'/session.sock')
b = json.dumps(dict(type='hello', version=3, role='catalog')).encode()
c.sendall(struct.pack('>I', len(b)) + b)
def take(n):
    b = b''
    while len(b) < n:
        part = c.recv(n-len(b))
        assert part, 'daemon disconnected'
        b += part
    return b
n = struct.unpack('>I', take(4))[0]; assert 0 < n <= 16*1024*1024
w = json.loads(take(n))
assert w['type'] == 'welcome' and w['version'] == 3, 'leave incompatible daemon alone'
assert w['catalog']['sessions'] == [], 'save and end retained sessions in Stage 3 first'
pid = w['pid']; assert pid > 1
command = subprocess.check_output(['ps', '-p', str(pid), '-o', 'command='], text=True).strip()
assert command.endswith(' --internal-daemon '+root), 'daemon identity does not match'
os.kill(pid, signal.SIGTERM)
print('Stopped verified compatible empty Stage 3 daemon', pid)
c.close(); os.close(f)
PY
```

For a **compatible Stage 4** runtime, quit its GUI and use the atomic empty check:

```sh
# Optional RUNNEL_INTERNAL_RUNTIME must identify the intended runtime.
"dist/darwin-$(./script/go.sh env GOARCH)/Runnel.app/Contents/MacOS/Runnel" \
  --internal-shutdown-empty
```

This never starts a daemon, refuses older protocols/nonempty registries, and shuts
down only after the daemon itself checks emptiness. Leave permanent lock files in
place; never unlink a live socket/lock or use process-name matching.

The separate **GUI control protocol stays at version 2**. `make dev` builds first,
asks only `gui.sock` to quit, waits for `gui.lock`, and launches the new GUI. It can
detach an older GUI but will then report its incompatible daemon. `--verify` checks
GUI ownership, not successful terminal negotiation; `--logs` and `--debug` remain.

For isolated development/verification, choose a fresh private directory and always
carry the override through launch and control commands:

```sh
RUNNEL_INTERNAL_RUNTIME="/private/tmp/<your-private-test-runtime>" make dev
```

For native automation use a uniquely identified disposable bundle with that runtime
in its Info.plist `LSEnvironment`. Rebinding the normal bundle after quit can relaunch
it without a one-shot launch override. See [VERIFICATION.md](VERIFICATION.md).

Build caches/CLI live in ignored `.cache/`. Distribution remains an ad hoc signed
development bundle, without Developer ID, notarization or a published release.
The macOS PTY adapter retains MyGO's MIT notice; MyGO itself is unchanged.

Only Stage 4 is implemented. No drag-and-drop rearrangement, zoomed pane mode,
browser panes, remote hosts, agents, disk restoration, Git features, launch-at-login,
public session CLI or custom visual system is included. See
[VERIFICATION.md](VERIFICATION.md) for automated, observed native and remaining manual
coverage, including earlier unverified work.
