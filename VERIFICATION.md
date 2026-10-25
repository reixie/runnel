# Stage 4 verification

Performed 2026-10-25 on Apple Silicon, macOS 27.0.1 (26A434), with **Go 1.27.1**
and unmodified **MyGO v0.6.3**. Neither pin changed. Only Stage 4 was added.
[README.md](README.md) documents the model, controls, budgets and protocol transition.

## Automated checks — passed

Final command:

```sh
make test vet
```

The final **51-test** suite passed in **15.583 seconds**, with a 90-second suite
timeout, followed by `go vet ./...`. Packaging, deep/strict ad hoc signature
verification and Info.plist lint also passed. The final command log is ignored
`.cache/verification/stage4-final-test.log`; an earlier detailed 51-test run is
`stage4-final-tests-verbose.log` (15.029 seconds). The final package uses its bundled
dylib explicitly. `./script/go.sh version` reported `go1.27.1 darwin/arm64`.

The restricted command sandbox rejected Unix socket binding and Directory Services
lookup on the initial attempt. Integration tests were then run with sandbox
permission, using their private `/private/tmp/rt-*` / `rt-process-*` runtimes.
Automated tests never contact the normal runtime. Socket/terminal/file waits are
bounded, generally five seconds; cleanup waits four seconds; slow-client tests
use production write deadlines. The 64-pane capacity fixture is also bounded.

The 38 earlier regressions remain, adapted to explicit pane IDs and per-pane exit
codes. Thirteen additional tests cover Stage 4 and protocol-3 compatibility:

- Immutable mixed-axis trees, stable internal/leaf identities, visual order and
  nested removals through all 24 removal orders. Nonfocused removal preserves focus; focused removal chooses the
  next visual survivor, then the previous at the end. Parent collapse preserves
  the sibling's node identity. Final removal leaves no leaf/focus. Invalid targets,
  axes, missing children/references, duplicate nodes, invalid focus, depth and
  non-finite/out-of-range ratios are rejected. Socket mutations reject stale/zero
  layout revisions and leave the previous tree/revision intact on failure.
- A real-socket **four-pane project session** with distinct shell PIDs and PTYs,
  isolated variables/output, project cwd and preserved HOME. All four connections
  remain attached simultaneously. MyGO's actual recursive view, keys and persistent
  handles exercise pointer/header focus, visual next/previous focus, keyboard input,
  clipboard paste, Edit-menu Copy and authoritative Cmd+K Clear. Other panes retain
  their own output. Rename text stays in the modal through a late sibling attachment.
- Actual MyGO divider press/move/release and window-size changes update affected
  terminal grids. Every shell runs `stty size`; its result must equal that pane's
  acknowledged rows/columns. A small 600×450 headless viewport exercises scrolling,
  usable pane controls and valid grids (the native minimum is 640×400).
- Delayed divider replies cannot restore an older percentage. The final queued
  ratio persists even after selection retirement; a callback cannot update retired
  presentation state. Drag notifications are coalesced by the UI timer.
- An unsaved Unicode Vim and sibling background job survive four-stream retirement,
  another working session, fresh catalog ownership and complete four-pane reopening.
  Vim writes its own **same PID, exact unsaved line, modified flag and actual 22×65
  grid** after resize. Shell/job identity is preserved. Accepted input, resize and
  Clear precede flush acknowledgement; callbacks after retirement are rejected.
  Earlier rapid-switch/delayed success/failure regressions remain.
- Close a nonfocused pane whose child ignores SIGHUP. Its leaf collapses immediately,
  focus stays on the chosen sibling and the pane retains a budget slot until cleanup
  completes. Catalog rename and sibling input work during cleanup. The child is gone
  afterward. A single broken pane stream does not exit its shell or affect siblings;
  reattachment keeps its PID. Separate actual exit codes are retained; session state
  stays running until all retained panes exit. Final-pane closure removes its session,
  and explicit creation afterward works.
- End Session starts cleanup of **four SIGHUP-ignoring pane children concurrently**.
  Another session can be created and used while they end. Every recorded child is
  reaped; the bounded elapsed assertion also catches serial cleanup.
- A noisy pane emits 1,200 × 8,000-character lines into a stalled 1 KiB-buffer client.
  Sibling commands and catalog rename remain available. The producer finishes, the
  stalled stream reaches EOF before a replacement attaches, and the original shell
  PID reattaches. Earlier unselected-session and PTY-input saturation tests remain.
- **Four panes/session and 64 total retained panes** reject overflow before another
  process starts. Exited panes still consume slots; ending panes cannot release one
  early. All four original panes reattach at total capacity. One catalog, three
  pending handshakes and four live pane connections fit the eight accepted slots.
- Foreign ownership, session/pane mismatch, unknown/ended IDs and old generations
  cannot displace siblings. A replaced/unavailable initial directory or failed shell
  exec leaves the original tree and process usable. Empty-runtime shutdown refuses
  live sessions and atomically shuts down a verified compatible empty daemon.
- Both simulated live protocol-2 and protocol-3 endpoints are left listening and
  untouched by the protocol-4 client. Independent GUI-control version 2 remains
  covered without signalling a real GUI in that test.

Verification found and fixed a viewport that did not fill the window, a transient
EOF under eight-socket admission pressure, and catalog snapshot bursts from multiple
pane exits. Admission now waits for a reserved slot before accept; replaceable
catalog snapshots are coalesced separately from correlated replies. Close Pane and
End Session acknowledge initiation immediately; resource release follows actor
cleanup, so catalog operations are not serialized behind process termination.

## Observed packaged native app checks

A uniquely identified disposable bundle at
`.cache/verification/Runnel Stage4 Test.app` used identifier
`dev.reixie.runnel.stage4-test` and a fixed Info.plist `LSEnvironment` override to
`/private/tmp/rt-stage4-native-8x3mqde8`. It was ad hoc signed. Runtime fixture files
were preserved under ignored `.cache/verification/stage4-native-fixtures/` before
that runtime was removed. The private catalog helper is ignored
`.cache/verification/native_stage4.py`.

Observed with the packaged app:

- Native Cmd+D and Cmd+Shift+D create nested right/down splits. Four simultaneous
  pane headers, focus dots, Close Pane buttons and standard dividers render. Pointer
  terminal/header focus and Cmd+]/Cmd+[ change the intended focus in visual order.
  Focus does not reconnect siblings. The split canvas fills the native viewport.
- The project fixture was registered/created through its private catalog, then
  split in the native GUI. Shells **55782, 55925, 55901 and 55917** were independent;
  project cwd was `/private/tmp/rt-stage4-native-8x3mqde8/Project`, with account
  `HOME=/Users/alex`. Files recorded the values `two`, `one`, `three`, `four`.
  Initial actual `stty` grids were 15×45. Native pointer/keyboard focus was observed
  before continuing paste operations; see the automation qualifications below.
- Native paste/Return launched Vim **56046** in shell **55925**, holding unsaved
  `UNSAVED_STAGE4_NATIVE — Žąsis 日本語`. A sibling shell ran SIGHUP-ignoring child
  **56049**. Other panes accepted commands independently. Cmd+K cleared the focused
  third pane; sibling output and Vim remained. Native divider focus/arrow movement
  and window zoom changed grids; private-socket inspection recorded Vim at **25×91**.
- Switching to the other retained session and back reconstructed the same project
  tree. Cmd+Q detached only the GUI. `ps` and a bounded private-socket helper verified
  the same daemon **53389**, four shell PIDs, job 56049 and Vim 56046 afterward.
  Reopening displayed all panes with Vim focused and retained content.
- Native Close Pane on the **nonfocused** shell 55782 removed only that leaf and
  expanded its sibling. Shell 55782 and ignoring child 56049 disappeared. Vim and
  the other two shells remained usable. Vim subsequently recorded **34×47**, its
  same PID, exact unsaved Unicode line and modified flag `1`.
- With the explicit isolated override, role-aware `make dev` preserved daemon 53389,
  Vim 56046 and the surviving pane shells. Its new GUI control endpoint responded.
  The helper's after-dev record again contained `56046`, `34x47`, the exact line
  and `1`. Native reopening displayed the retained three-pane layout. Native End
  Session removed the project panes and selected the remaining Home session;
  ending Home left an empty catalog while daemon 53389 stayed alive. Its verified
  empty runtime was explicitly shut down with the new internal maintenance action.

Exact development command:

```sh
RUNNEL_INTERNAL_RUNTIME="$(cat .cache/verification/stage4-runtime.path)" make dev
```

Logs: `stage4-dev.log`, `stage4-vim-dev.log`; catalog/grid evidence:
`stage4-after-quit.json`, `stage4-after-close.json`, `stage4-after-dev.json`, and
`stage4-empty-catalog.json`. The entire Vim fixture was not restarted from scratch
following the final admission/heartbeat refinements; the final fresh-package run
below separately exercised the finished build and daemon.

After final `make test vet`, the disposable bundle was refreshed from that exact
package and started a **fresh final daemon 60090**. Native New Session, mixed-axis
four-pane splitting, focus, keyboard divider resize, Cmd+W and reopening were
observed again. The saved catalog recorded shell PIDs **60121, 60125, 60176, 60190**,
stable pane/tree IDs, remembered focus and root ratio **0.5126742553710938**. Reopening
showed all four panes. Native End Session returned to empty Home; another New Session
created Terminal 2; **Close Pane on its only leaf** ended that session. The same daemon
remained empty until explicit compatible-empty shutdown. `lsof` confirmed the final
disposable bundle's `Contents/Resources/libghostty-vt.dylib`. Evidence:
`stage4-final-native-catalog.json`, `stage4-final-library.log`,
`stage4-final-empty-catalog.json`. An empty-selection retirement fast path was added
after this native run to reuse the pending barrier; the final 51-test/vet run includes
it. Final cleanup recorded no remaining verification
GUI, daemon, shell, job or Vim PIDs (`stage4-cleanup.log`).

### Native automation qualifications and isolation incident

The first native inspection took several minutes to return. Some screenshots lagged
AX state. A final key-quit attempt left background fixture GUI 60624 alive; the
process audit found it, verified its exact disposable-bundle path, and stopped it
with SIGTERM. The final cleanup claim includes that explicit audit. Pointer dragging initially returned `noWindowsAvailable`; later attempts
completed without establishing observed divider movement. **Native pointer drag is
not claimed as verified**; actual MyGO dragging passed in the headless integration
check, and native divider keyboard resizing was observed.

Rapid batched AX click/paste produced a clipboard conflict and reached a terminal
before requested focus had settled. Subsequent paste actions used observed committed
terminal focus with separate Return events. Pasting a `:call` inspection command into
normal-mode Vim inserted it as bracketed paste; native Undo restored the original
line before the later exact-line checks. Deterministic Vim inspection used the
bounded private socket after GUI detach. This is not a claim of physical-keyboard
Vim typing or rapid physical click→paste fidelity.

A development-bundle rebind **after quit** relaunched it without the one-shot runtime
override, creating GUI **55438** and an empty normal-runtime daemon **55445**. This
was detected via exact process paths/parentage. Both were stopped only after verifying
protocol 4, daemon PID 55445 and **zero retained sessions**; no user session existed,
was opened or ended there. Permanent lock files were left intact. Consequently this
run cannot claim the normal runtime was never contacted. All remaining native work
used the uniquely identified bundle with fixed `LSEnvironment`; all automated tests
were isolated throughout. Final cleanup verified both accidental process IDs absent.

## Remaining manual coverage and limitations

- Repeat native **pointer dragging**, rapid click→type/paste, physical selection→Copy,
  physical Vim letters/Escape, Ctrl+C/Ctrl+Z/fg and trackpad scrolling. Automated
  socket/view coverage passes; it is separate from physical-input evidence.
- Stage 4 did not successfully repeat native directory-picker navigation. The
  project native fixture used explicit private-catalog registration; Stage 3's
  picker/Cancel/rename observations remain historical below. Modal focus, picker
  cancellation and directory/identity failures have automated coverage. The final
  view disables terminal input while a directory dialog or rename modal owns focus.
- IMEs, VoiceOver, other account shells, real trackpad behavior, Intel/universal
  execution, macOS 13 execution, clean-machine Gatekeeper, Developer ID signing and
  notarization remain unverified. No release or deployment was published.
- The README's explicit Stage 3 empty-daemon shutdown procedure was documented from
  the protocol/lifecycle implementation; it was not executed against a user's old
  daemon. Compatibility is simulated for protocols 2/3. Current protocol-4 atomic
  empty shutdown is automated and was used for both isolated native daemons.
- No new OS-level offline/network-isolation check was performed. Bundled-library
  startup regressions and native mappings pass; earlier offline-style evidence and
  qualifications remain below. The full 64-project stress case remains untested.
- Scrollback is an approximate 512 KiB budget per terminal, with eviction. Images
  remain disabled. Hidden main-screen history arrives after alternate-screen exit.
  Initial PTY grids are 80×24 until a view resize is acknowledged. Very small views
  use a scrollable minimum canvas; ratio and grid bounds are intentional.
- State is memory-only. Daemon death/reboot and intentionally detached processes
  remain outside restoration/cleanup guarantees. There is no rearrangement, pane
  zoom mode, browser/remote panes, agent functionality or later-stage feature.

## Historical Stage 3 record

The following record is preserved as Stage 3 evidence. Its one-terminal sessions,
protocol 3, file names and remaining gaps describe that stage. They are not claims
about Stage 4 or another run. The historical Stage 2 record remains below it.

# Historical Stage 3 verification

Performed 2026-10-25 on Apple Silicon, macOS 27.0.1 (26A434), using
**Go 1.27.1** and unmodified **MyGO v0.6.3**. Neither pin changed. The model,
protocol, limits and Stage 2 transition are described in [README.md](README.md).

## Automated checks — passed

```sh
make test vet
```

The final **38-test** suite passed in **13.152 seconds**, with a 90-second suite
timeout, followed by `go vet ./...`. The command packages the actual app and
explicitly selects its bundled library. Evidence is retained in the ignored
`.cache/verification/stage3-test.log`. Socket/PTY tests require execution outside
the command sandbox: the restricted sandbox rejects Unix socket binding and
Directory Services lookup. Those setup failures were not treated as product bugs.

All runtime tests use private `/private/tmp/rt-*` or `rt-process-*` directories.
Account-style fixtures use temporary homes/startup files. Test cleanup only
cancels its own daemon, ends its own IDs or signals its recorded packaged daemon.
No test contacts `/private/tmp/runnel-<uid>` or the user's normal sessions.
Individual terminal/catalog waits are five seconds; cleanup waits are four
seconds; the raw slow-client fixture has an eight-second production deadline.

Coverage includes the Stage 2 regressions and these Stage 3 cases:

- Real sockets create sessions in two temporary projects and Home. Shell PIDs
  differ; cwd, logical `PWD`, physical `pwd -P` and `HOME` are checked. Each shell
  retains a different variable and its background job after switching and after
  opening a fresh catalog/presentation connection. The daemon's cwd is untouched.
- Space/Unicode paths, same-name folders, symlink aliases, missing and inaccessible
  roots, replacement directory identity, unknown project IDs and removal of
  empty project metadata. Opening projects never implicitly creates a shell.
  Picker cancellation is covered at the UI action boundary and observed natively.
- Renaming remains stable after OSC title output. `cd /` leaves project membership
  unchanged. The sidebar binding test covers clicks, rebuilds, empty Home selection
  and programmatic selection without a stale click restoring the previous ID.
- A shell exits with status 37 while another terminal is attached; the catalog
  receives its actual status/name independently. Ended/unknown attachment IDs
  return errors without creating shells. Ending the selected ID picks the oldest
  eligible session deterministically; ending the last ID leaves an empty registry;
  an explicit create afterward gets a new ID/PID in the same daemon.
- Rapid attachment changes while a background job writes output. An older hello
  generation cannot displace the current peer. Delayed successful/failed GUI
  attachment results cannot replace the current selection. Input, Clear and End
  target their IDs; the first session's history survives clearing the second.
  The existing frame/grid/resize/query regressions remain in the suite. A
  retiring stream rejects late callbacks and acknowledges its preceding input/
  resize on the daemon FIFO before replacement; a dedicated real-socket test
  verifies accepted commands and the exact grid survive an immediate switch.
- Unsaved Unicode Vim survives switching to another working shell, closing the
  catalog connection, opening a fresh one and resizing. Its PID and unsaved line
  are checked from Vim itself; return to the original shell is verified.
- End Session kills a background child ignoring SIGHUP while another session accepts
  commands during and after cleanup. Late removal/exit events do not change the
  surviving session's identity or running state.
- An unpaced producer emits 1,200 × 8,000-character lines in an unselected session
  while another accepts input and catalog renaming remains available. Completion
  is checked by files, since bounded scrollback may legitimately evict a screen
  marker before a test observes it.
- A stalled terminal stream with a 1 KiB receive buffer still allows catalog rename
  and explicit creation of a second session. Another unpaced output producer
  completes; the stalled socket reaches EOF before any replacement attachment.
  The second session and the original PID both accept commands afterward.
- Saturating the GUI action queue reports an error and resets create/reconnect
  pending flags, so rejection cannot leave those actions permanently disabled.
- The retained-session cap of 32 rejects additional creation without eviction;
  ending an ID releases its slot. Oversized frames/inputs, invalid grids, PTY input
  saturation, heartbeat/write bounds, stale sockets and daemon startup exclusion
  retain their regression coverage. The project cap is 64 by implementation;
  a full 64-project stress fixture was not added.
- Six concurrent packaged startup attempts create one daemon and no implicit shell.
  Explicit creation then reattachment preserves its PID. A competing daemon cannot
  acquire its lock. An incompatible protocol hello leaves a compatible attachment
  usable. A simulated live Stage 2 endpoint is not replaced or unlinked; the new
  client reports incompatibility. The independent GUI stop protocol still sends
  version 2, tested against a disposable GUI-control server without signalling a
  real process.

Important fixes found during verification: End Session now lets its stream writer
send the ended acknowledgement before closure; raw commands include explicit IDs;
macOS `/var` alias expectations distinguish logical and physical cwd; background
exit fixtures wait until their command actually starts before detaching. The
sidebar uses persistent application state, rather than a temporary per-frame value.
A dedicated switch-retirement test seals late callbacks and checks that an
already accepted command and exact 111×37 resize survive an immediate switch.
Each session's single bounded FIFO preserves accepted output/mutation ordering;
separate session loops keep noise out of the registry and other sessions.

## Observed packaged native app checks

A disposable bundle at `.cache/verification/Runnel Stage3 Test.app` initially used
identifier `dev.reixie.runnel.stage3-test` and an Info.plist `LSEnvironment` value
`RUNNEL_INTERNAL_RUNTIME=/private/tmp/rt-final3-s_26ttbo`. Later `make dev` launched
the normal development bundle with that same explicit isolated override. The
normal user runtime was never used. The runtime path is recorded in ignored
`.cache/verification/stage3-final-runtime.path`.

Observed in the native UI:

- Empty Home on startup; Cmd+O opens MyGO's native directory sheet; Cancel leaves
  it empty. Native Open registered `projects/One Žąsis` and `projects/Two 日本語`
  without creating terminals. Cmd+T explicitly created their sessions. The
  standard Open prompt is retained. Accessibility-driven folder navigation
  sometimes needed another native focus/scroll event before picker validation
  enabled Open; a separate AppKit-only probe showed similar behavior. A custom
  confirmation label was initially suspected but was not established as the cause.
  No framework patch or dependency update was made.
- Home/project rows, running states, selection, empty group views, New Session,
  Rename Session and End Session controls use MyGO's existing widgets. The sidebar
  actions remain visible after the layout correction. Full roots are available in
  tooltips where labels truncate. Rename gives its text field focus; typed rename
  text does not enter the shell.
- Project one used shell **46663**, variable `V=one`, initial cwd
  `/private/tmp/rt-final3-s_26ttbo/projects/One Žąsis`, account `HOME=/Users/alex`
  and initial job **46691**. Project two used shell **46742**, `V=two`, job **47243**
  and its own root. Home used shell **47245**, `V=home`, job **47251** and account
  home cwd. Fixture records `one.txt`, `two.txt`, `home.txt` and `before-quit.txt`
  in the isolated runtime confirmed these identities/values. The sleeps later
  completed naturally during verification; this is not reported as termination
  caused by a switch or GUI quit.
- Home was renamed **Home Notes**. Selection changed between Home and both
  projects without shell replacement. An isolated catalog inspection deliberately
  replaced the GUI transport; it did not create shells. The subsequent role-aware
  development relaunch reconciled the two existing project sessions.

Exact development check:

```sh
RUNNEL_INTERNAL_RUNTIME="$(cat .cache/verification/stage3-final-runtime.path)" make dev
```

It preserved daemon **46631** and both existing project shell PIDs **46663** and
**46742**. The newly built GUI displayed their existing catalog. Its independent
GUI endpoint was checked with `--internal-gui-running` under the same override.
The command/output are in ignored `.cache/verification/stage3-dev.log`.

Native clipboard/Return launched Vim in project one with an explicitly unsaved
buffer; setup used Vim startup commands to avoid claiming physical-keyboard
letter/Escape fidelity:

```sh
/usr/bin/vi -u NONE -i NONE -n \
  '+call setline(1, "UNSAVED_STAGE3 — Žąsis 日本語")' \
  '+set modified' \
  '+call writefile([string(getpid())], "/private/tmp/rt-final3-s_26ttbo/vim-pid")'
```

Vim PID **47578** displayed the unsaved line. Native zoom/restore resized the
alternate screen. Home remained usable while Vim was unselected. Cmd+Q removed
only the GUI. `ps` still showed daemon 46631, shells 46663/46742/47245 and Vim 47578.
A bounded private-socket inspection after quit asked Vim to write its own state,
then asked the other shells for their variables. It recorded:

```text
47578
36x100
UNSAVED_STAGE3 — Žąsis 日本語
1
46742 two
47245 home
```

The final `1` is Vim's modified flag. This helper attached existing IDs only;
it never created a replacement shell or changed the remembered selection.
The ignored `.cache/verification/native_stage3.py`,
`stage3-native-after-quit.json` and runtime `native-vim-state.txt` retain the
exact fixture/evidence. Reopening with an explicit isolated LaunchServices
environment restored **Home Notes** as selected and showed all three sessions.
Selecting project one's terminal displayed the same unsaved Vim line again.

The fixture sessions were subsequently explicitly ended by their recorded IDs.
`ps` showed only daemon 46631, and a real catalog listed zero sessions. The GUI
showed **Disconnected. Retained sessions may still be running.** after transport
replacement; native Reconnect displayed the empty workspace without creating a
shell. After cleanup, the final package was launched in the isolated runtime with
`./script/build_and_run.sh --verify`; its role-aware launch log is saved at
`.cache/verification/stage3-final-launch.log`. Final signature verification and
Info.plist lint passed. In that final fresh native build, New Session created
`Terminal 1`; End Session returned to empty Home; another New Session created
`Terminal 2`. A native command immediately followed by Cmd+T selected `Terminal 3`
and still wrote `accepted-before-switch` to `final-flush.txt`. The final private
catalog recorded daemon **48186**, shells **48239** and **48249**, and two retained
Home sessions. `lsof` confirmed that daemon mapped the final bundle's
`Contents/Resources/libghostty-vt.dylib`. The GUI was detached, both exact IDs were
explicitly ended, and only that recorded empty daemon was stopped. All verification
processes created for this run were cleaned up. Evidence is in ignored
`stage3-final-native-catalog.json` and the runtime fixture files. The entire earlier
three-session/Vim fixture was not repeated from scratch after the last refinements;
the final immediate-switch path and empty/final-session actions were observed.

## Remaining manual coverage and limitations

- Repeat the complete picker/sidebar workflow with a physical keyboard/trackpad.
  Accessibility automation required extra focus events during picker navigation
  and terminal attachment. It successfully completed both projects, Home,
  rename, selection, native resize and GUI quit/reopen; it is not a latency or
  physical input fidelity measurement. Later native automation also returned
  ScreenCaptureKit invalid-parameter/frontmost-app errors; those failed attempts
  are not counted as observed checks.
- Stage 2 physical letter/Escape, native Ctrl+C/Ctrl+Z/fg and selection-to-Cmd+C
  round trips still need a human repeat on the final build. Their socket/view
  regression tests pass. Stage 3 Vim setup/inspection also uses deterministic
  startup commands and a bounded protocol helper, not physical Vim typing.
- IMEs, VoiceOver, other account shells, real trackpad behavior, Intel/universal
  execution and macOS 13 execution remain unverified. The adapter is macOS-only.
- Exact live Stage 2 upgrade was checked with protocol simulation, not by attaching
  to a user's Stage 2 daemon. Use the compatible Stage 2 app to save work and End
  Session before changing protocols. GUI control compatibility is separate.
- The prior Stage 2 minimal/offline-style packaged check below was not rerun as a
  Stage 3 network-isolation test. The bundle-loading regression and packaging path
  remain covered; no claim of an OS-level network block or packet capture is made.
- Scrollback is approximately 512 KiB per terminal. Images are disabled. Mirror's
  hidden main-screen history is delivered after returning from alternate screen.
  Metadata/selection exist only for this daemon lifetime. Daemon death, reboot,
  deliberate process detachment and an uncatchable daemon SIGKILL remain outside
  restoration/cleanup guarantees. Ad hoc signing only; no notarization, clean-machine
  Gatekeeper, public release, disk restoration, panes or later-stage features.

## Historical Stage 2 record

The original Stage 2 evidence follows unchanged apart from its title. It documents
what was checked then, including its remaining manual gaps; it is not another
Stage 3 native run. In particular, its one-session limit and daemon-stopping End
Session semantics describe Stage 2, not the current implementation.

# Historical Stage 2 verification

Performed 2026-10-24–25 on Apple Silicon, macOS 27.0.1 (26A434), with
**Go 1.27.1** and unmodified **MyGO v0.6.3**, tag commit
`15242900157b77228d35ca071f43e94a11bc90a0`. No pinned version changed. Purego
v0.11.1 is now marked as a direct dependency because the macOS adapter uses it.

## Automated checks — passed

Final command:

```sh
make test vet
```

This packages the app, selects its bundled library explicitly, runs the full
25-test suite with a 90-second timeout, and runs `go vet ./...`. Final suite time:
9.469 seconds. Individual terminal waits are bounded to five seconds; daemon
cleanup waits to four seconds. Socket tests use private `/private/tmp/rt-*`
directories; packaged startup tests use `rt-process-*`. They never attach to the
normal user runtime. Cleanup cancels or ends only daemons created by that test.

Coverage exercised through the real socket and actual Runnel terminal view:

- Login/interactive zsh, temporary `.zprofile`, home cwd, `tty`, environment and
  locale normalization, Directory Services lookup, invalid startup paths.
- Shell PID, shell variable and background-job survival across detach/reattach;
  output produced while detached; retained history; authoritative Clear remaining
  cleared after a second attachment.
- Unsaved Unicode Vim buffer, the same Vim PID across attachment, alternate
  screen, resize acknowledgements, actual Vim `&lines`/`&columns`, and return to
  the same shell.
- Ctrl+C, Ctrl+Z/`fg`, Ctrl+D, Unicode, multiline bracketed paste, Edit commands,
  selection/copying, scrolling and view resizing through the daemon.
- Broken transport displays Disconnected without shell exit; reattachment keeps
  the PID. Exit 23 remains connected and survives another attachment with the
  same PID/status, rather than creating a new shell.
- A mismatched hello leaves an already attached compatible GUI/session usable.
  Oversize length headers and invalid sizes are rejected. A stale socket is
  replaced under lock; a live socket/held lock remains undisturbed.
- Six concurrent packaged startup/attach attempts produce the same shell PID.
  Another daemon candidate cannot acquire ownership. This uses the real bundle
  executable in daemon mode, without a GUI event loop, and with an invalid
  inherited `MYGO_GHOSTTY_VT` to prove the bundle overrides it.
- A client with a 1 KiB receive buffer stops reading a large initial snapshot.
  An unpaced background producer emits another 180 × 8,000-character rows, then
  writes a completion file. It completes while the GUI is stalled. EOF is
  verified before any replacement attachment can close that peer; the later
  attachment accepts another shell command. This final fixture removes per-row
  sleeps that were timing-dependent in earlier full-suite runs.
- PTY input saturation disconnects the GUI without reporting shell exit; another
  connection sees the same PID and End Session still completes.
- Raw child queries receive pixel dimensions `408×720`, cell size `17×9`, cursor
  position, propagated light-theme RGB and the light-scheme report. These are
  answered in the headless authoritative terminal.
- End Session reaps the shell, foreground/background jobs, an exec'd process
  ignoring SIGHUP, and a separate background child ignoring SIGHUP. A later
  daemon/attachment creates a different shell PID. Close is idempotent.

Other exact checks performed:

```sh
RUNNEL_INTERNAL_RUNTIME="$(cat .cache/verification/runtime.path)" make dev
RUNNEL_INTERNAL_RUNTIME="$(cat .cache/verification/runtime.path)" \
  ./script/build_and_run.sh --verify
codesign --verify --deep --strict --verbose=2 dist/darwin-arm64/Runnel.app
plutil -lint dist/darwin-arm64/Runnel.app/Contents/Info.plist
./script/go.sh version
```

The launch check uses the GUI control endpoint, not `pgrep -x Runnel`, because
both GUI and daemon have that executable name. Signing and plist validation
passed. The pinned dylib SHA-256 remains:

```text
6f385d3221f8c776ba40530db9baf8c71478907897959849896eeb0ddbf16b41
```

A temporary offline-style harness was run as:

```sh
python3 .cache/verification/offline_stage2.py
```

It launched the exact packaged executable with
`--internal-daemon /private/tmp/rt-offline-…`, cwd `/`, a fresh temporary HOME,
only system PATH/LANG, `MYGO_GHOSTTY_VT=/invalid/inherited.dylib`, and HTTP/HTTPS
proxies at `http://127.0.0.1:1`. It performed the protocol handshake, read a frame,
inspected `lsof`, sent End Session and waited for exit. Daemon PID **35308** loaded
`dist/darwin-arm64/Runnel.app/Contents/Resources/libghostty-vt.dylib`; shell PID
**35310** had cwd `/Users/alex`; no MyGO native cache was created under the temporary
HOME; the daemon exited 0. The harness and log are ignored workspace evidence,
not shipped tooling. This tests the downloader path, not an OS-level network
block or packet capture.

## Observed native app checks — passed

A disposable copy at `.cache/verification/Runnel Stage2 Test.app` used identifier
`dev.reixie.runnel.stage2-test` and an Info.plist `LSEnvironment` value
`RUNNEL_INTERNAL_RUNTIME=/private/tmp/rt-native-qif1dgip`. It was ad hoc signed.
This kept its GUI control, daemon lock, socket and shell separate from the normal
Runnel runtime. The app was launched by double-clicking it in Finder.

The persistence fixture entered through native clipboard paste was:

```sh
export STAGE2_NATIVE=survives
sleep 600 & STAGE2_JOB=$!
printf '\nSHELL=%s VALUE=%s JOB=%s\n' $$ $STAGE2_NATIVE $STAGE2_JOB
tty
stty size
```

Observed: daemon **34426**, shell **34428**, background job **34453**, value
`survives`, PTY `/dev/ttys001`, grid `36 104`, and account home cwd. Cmd+Q removed
the GUI completely. `ps` showed the daemon reparented to PID 1, the same shell and
job still running. Finder reopening restored the screen. Then:

```sh
kill -0 $STAGE2_JOB &&
  printf '\nREATTACHED SHELL=%s VALUE=%s JOB=%s\n' \
    $$ $STAGE2_NATIVE $STAGE2_JOB
```

printed `REATTACHED SHELL=34428 VALUE=survives JOB=34453`.

For the force-termination check, this output was scheduled:

```sh
(sleep 10; printf '\nOUTPUT_WHILE_GUI_DEAD\n') &
```

Only the previously inspected isolated GUI PID was killed:

```sh
kill -KILL 34782
ps -p 34426,34428,34453 -o pid=,ppid=,command=
```

All three retained PIDs remained alive. Finder reopening displayed
`OUTPUT_WHILE_GUI_DEAD`. The same `kill -0`/printf check printed
`AFTER_KILL SHELL=34428 VALUE=survives JOB=34453`.

System Vim was started with:

```sh
/usr/bin/vi -u NONE -i NONE -n
```

Vim PID **34851** held unsaved `UNSAVED_STAGE2 — Žąsis 日本語`. The same PID and
buffer survived Cmd+Q and Finder reopening. Native zoom/restore resized and
redrew the alternate screen. Vim's own inspection after restore recorded:

```vim
:call writefile([string(getpid()), string(&lines)."x".string(&columns), getline(1)], "/Users/alex/Developer/runnel/.cache/verification/native-vim.txt")
```

The file contained `34851`, `36x104`, and the unsaved text. `:q!` returned to shell
PID 34428. Some Escape/letter synthesis from the native automation tool was
inconsistent, so the deterministic buffer setup/inspection and `:q!` were also
sent as input packets through the isolated daemon using the temporary
`.cache/verification/native_input.py` harness. Native rendering, resize,
quit/reopen, process identity and buffer restoration were observed; this is not
claimed as a physical-keyboard Vim check.

Replacing the GUI transport with that harness immediately displayed
**Disconnected. The session may still be running.** Clicking **Reattach** restored
the same Vim without creating a shell. No shell-exit status was shown for the
transport loss.

`make dev`, with the isolated runtime override above, preserved daemon **34426**
and shell **34428**, and launched the freshly built GUI. Its native terminal
printed `AFTER_DEV SHELL=34428 VALUE=survives JOB=34453`. The original sleep later
completed normally after its 600 seconds.

Native context-menu **Clear** removed old output. Cmd+W detached; Finder reopening
showed only the cleared prompt and subsequent job-completion notice, without the
old output. Native clipboard paste and Return continued to accept commands.
`exit 7` displayed **Shell exited (status 7).** Clicking **End Session** displayed
**Session ended.** separately. `lsof` confirmed the bundled dylib in the headless
daemon; the minimal-environment packaged check independently confirmed the final
bundle's daemon load path.

A final freshly built GUI/daemon pair (**36424**/**36433**) both mapped the final
bundle's dylib according to `lsof`. In shell **36435**, native clipboard paste
started this background child:

```sh
sh -c 'trap "" HUP; printf "\nNATIVE_IGNORING=%s\n" $$; exec sleep 600' &
```

The terminal reported child **36612**. **File → End Session** removed daemon
36433, shell 36435 and child 36612; the subsequent `ps -p
36424,36433,36435,36612 -o pid=,ppid=,command=` showed only the GUI. Its native window
displayed **Session ended.** The GUI was then stopped through its isolated control
endpoint and its test runtime was removed.

## Repeat the native checks without touching a real session

Build, then prepare a uniquely identified disposable app/runtime:

```sh
make build
mkdir -p .cache/verification
cp -R dist/darwin-arm64/Runnel.app '.cache/verification/Runnel Stage2 Test.app'
python3 - <<'PY'
import pathlib, plistlib, tempfile
runtime = tempfile.mkdtemp(prefix='rt-native-', dir='/private/tmp')
pathlib.Path('.cache/verification/runtime.path').write_text(runtime)
p = pathlib.Path('.cache/verification/Runnel Stage2 Test.app/Contents/Info.plist')
d = plistlib.loads(p.read_bytes())
d['CFBundleIdentifier'] = 'dev.reixie.runnel.stage2-test'
d['CFBundleName'] = 'Runnel Stage2 Test'
d['CFBundleDisplayName'] = 'Runnel Stage2 Test'
d['LSEnvironment'] = {'RUNNEL_INTERNAL_RUNTIME': runtime}
p.write_bytes(plistlib.dumps(d))
print(runtime)
PY
codesign --force --deep --sign - '.cache/verification/Runnel Stage2 Test.app'
```

Double-click the copy in Finder and use the fixtures above. Record GUI, daemon,
shell, job and Vim PIDs using `ps -axo pid=,ppid=,command=`; distinguish the daemon
by `--internal-daemon <your-runtime>`. Force-kill only the recorded GUI PID, never
use `pkill -x Runnel`. Run the isolated `make dev` command to test rebuild survival.
Use End Session and close the GUI before deleting only the runtime directory you
created. Leave real Runnel processes and `/private/tmp/runnel-<uid>` alone.

## Remaining manual coverage and limitations

- Physical-keyboard letter/Escape entry, native Ctrl+C/Ctrl+Z/fg and native
  selection-to-Cmd+C round trips need a human repeat on the final build. Native
  automation was inconsistent for those key events. Their socket/view integration
  tests passed; that is separate evidence. Native paste, Return, quit/close,
  context-menu Clear, lifecycle banners and Reattach were observed.
- Native IMEs, VoiceOver, physical trackpad behavior, other account shells,
  Intel/universal execution and macOS 13 execution remain unverified. The adapter
  intentionally supports only macOS.
- Scrollback is an approximate 512 KiB VT budget; old rows are intentionally
  evicted. Images are disabled. Mirror sends hidden main-screen history after
  leaving the alternate screen rather than while it is hidden.
- One session and one active GUI attachment only. Initial daemon grid is 80×24
  until the GUI's first resize is acknowledged. A disconnected GUI never creates
  a replacement session through Reattach.
- Normal daemon shutdown cleans owned terminal processes. Recovery after daemon
  death/reboot and cleanup guarantees after an uncatchable daemon SIGKILL are
  outside this stage. Deliberately detached processes are outside PTY ownership.
- Ad hoc signing only. Developer ID, notarization and clean-machine Gatekeeper
  distribution were not performed. No release was published.
