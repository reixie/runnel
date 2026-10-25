package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/egoist/mygo"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--internal-stop-gui" {
		if err := guiControl(true); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--internal-gui-running" {
		if guiControl(false) != nil {
			os.Exit(1)
		}
		return
	}
	// Process role is resolved before any MyGO application initialization.
	if err := loadBundledLibrary(); err != nil {
		log.Print(err)
		if len(os.Args) > 1 {
			os.Exit(1)
		}
	}
	if len(os.Args) == 2 && os.Args[1] == "--internal-shutdown-empty" {
		if err := shutdownEmptyRuntime(); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--internal-daemon" {
		cfg := defaultConfig()
		cfg.dir = os.Args[2]
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer cancel()
		if err := serveDaemon(ctx, cfg, nil); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}

	release, err := registerGUI()
	if err != nil {
		log.Print(err)
		return
	}
	defer release()

	mygo.App.SetName("Runnel")
	app := &application{}
	mygo.App.WhenReady(app.open)
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { app.close() })
	if err := mygo.App.Run(); err != nil {
		log.Print(err)
	}
}
