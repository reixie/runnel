package main

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type terminalView struct {
	term        *terminal.Terminal
	conn        *connection
	closeWindow func()
	reattach    func()
	end         func()
	focus       bool
	noAutoFocus bool
	embedded    bool
}

func (v *terminalView) build(c *ui.Context) {
	column := ui.Column(c).FillWidth()
	if v.embedded {
		column.Grow(1)
	} else {
		column.FillHeight()
	}
	column.Children(func() {
		e := terminal.View(c, v.term).Grow(1).FillWidth()
		if !v.noAutoFocus {
			e.AutoFocus()
		}
		if v.focus {
			e.Focus()
		}
		// State comes from the protocol, not presentation.Terminal.Done/ExitCode.
		state := v.conn.status()
		if state.Exited || !state.Connected {
			ui.Row(c).Padding(8).Gap(12).Children(func() {
				message := fmt.Sprintf("Shell exited (status %d).", state.Code)
				if !state.Connected {
					message = "Disconnected. The session may still be running."
					if state.Ended {
						message = "Session ended."
					}
				}
				ui.Text(c, message)
				if !state.Connected && !state.Ended && v.reattach != nil && ui.Button(c, "Reattach").Clicked() {
					v.reattach()
				}
				if state.Exited && state.Connected && ui.Button(c, "End Session").Clicked() {
					if v.end != nil {
						v.end()
					} else {
						go v.conn.end()
					}
				}
				if ui.Button(c, "Close Window").Clicked() {
					v.closeWindow()
				}
			})
		}
	})
}
