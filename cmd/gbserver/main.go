package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"gbserver/internal/config"
	"gbserver/internal/gb"
	"gbserver/internal/httpapi"
)

func main() {
	cfgPath := flag.String("c", "config.ini", "config file path")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("config %s not found, using defaults", *cfgPath)
			cfg = config.Default()
			cfg.Upper.ID = cfg.SIPID
			cfg.Upper.Domain = cfg.SIPDomain
		} else {
			log.Fatalf("load config: %v", err)
		}
	}

	if cfg.SIPIP == "" {
		cfg.SIPIPAuto = true
		cfg.SIPIP = config.DetectLocalIP(cfg.Upper.IP)
	}
	if cfg.SIPIP == "" {
		log.Fatalf("cannot detect local ip, please set sip.ip in %s", *cfgPath)
	}

	platform, err := gb.New(cfg)
	if err != nil {
		log.Fatalf("init platform: %v", err)
	}
	platform.Start()

	addr := fmt.Sprintf(":%d", cfg.HTTPPort)
	srv := &http.Server{Addr: addr, Handler: httpapi.New(platform, *cfgPath).Handler()}
	go func() {
		log.Printf("http api listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server: %v", err)
		}
	}()

	log.Printf("GBServer started: sip=%s@%s listen=%d (udp+tcp) transfer=%s transcode=%v/%s",
		cfg.SIPID, cfg.SIPDomain, cfg.SIPPort, cfg.TransferURL, cfg.Transcode, cfg.TranscodeFormat)
	if cfg.Upper.Enable {
		log.Printf("upper platform: %s:%d/%s id=%s", cfg.Upper.IP, cfg.Upper.Port, cfg.Upper.Transport, cfg.Upper.ID)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down")
	platform.Close()
	_ = srv.Close()
}
