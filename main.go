package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ipadownstudio/internal/config"
	"ipadownstudio/internal/ipatool"
	"ipadownstudio/internal/itunes"
	"ipadownstudio/internal/manager"
	"ipadownstudio/internal/server"
	"ipadownstudio/internal/syswin"
)

const preferredPort = 18990

func main() {
	portFlag := flag.Int("port", 0, "TCP port to listen on (0 prefers 18990, falling back to an ephemeral port)")
	noBrowser := flag.Bool("no-browser", false, "Do not open the desktop app window automatically")
	dataDirFlag := flag.String("data-dir", "", "Override persistent data directory (defaults to <exeDir>/data)")
	flag.Parse()

	dataDir, err := config.ResolveDataDir(*dataDirFlag)
	if err != nil {
		log.Fatalf("resolve data dir: %v", err)
	}
	store, err := config.NewStore(dataDir)
	if err != nil {
		log.Fatalf("initialize store: %v", err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	hub := manager.NewEventHub()
	engine := ipatool.NewClient(cfg.CustomIpatoolPath)
	itunesClient := itunes.NewClient()
	accounts := manager.NewAccountManager(store, engine, hub)
	versions := manager.NewVersionManager(store, engine, accounts, hub)
	downloads, err := manager.NewDownloadManager(store, engine, accounts, hub)
	if err != nil {
		log.Fatalf("initialize download manager: %v", err)
	}
	transfer := manager.NewTransferManager(hub)

	srv := server.New(server.Dependencies{
		Store:     store,
		Engine:    engine,
		ITunes:    itunesClient,
		Hub:       hub,
		Accounts:  accounts,
		Versions:  versions,
		Downloads: downloads,
		Transfer:  transfer,
	})

	ln, err := listenLocal(*portFlag)
	if err != nil {
		log.Fatalf("listen failed: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", addr.Port)

	binInfo := engine.Info()
	log.Printf("IPADownStudio v%s listening on %s", config.AppVersion, baseURL)
	log.Printf("Data dir: %s", dataDir)
	if binInfo.Found {
		log.Printf("ipatool engine: %s (%s)", binInfo.Version, binInfo.BinaryPath)
	} else {
		log.Printf("ipatool engine: NOT FOUND (configure path in Settings)")
	}

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()

	if !*noBrowser {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if openErr := syswin.OpenAppWindow(baseURL); openErr != nil {
				log.Printf("open browser window failed: %v (open manually: %s)", openErr, baseURL)
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("Received %v, shutting down cleanly...", sig)
	case serveErr := <-errCh:
		if serveErr != nil {
			log.Printf("Server error: %v", serveErr)
		}
	}

	_ = transfer.Stop()
	downloads.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = srv.Shutdown(ctx)
	cancel()
}

func listenLocal(requestedPort int) (net.Listener, error) {
	if requestedPort > 0 {
		return net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", requestedPort))
	}
	if ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", preferredPort)); err == nil {
		return ln, nil
	}
	return net.Listen("tcp4", "127.0.0.1:0")
}
