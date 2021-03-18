package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pires/go-proxyproto"
	"golang.org/x/net/http2"

	"git.sr.ht/~emersion/soju"
	"git.sr.ht/~emersion/soju/config"
)

func main() {
	var listen, configPath string
	var debug bool
	flag.StringVar(&listen, "listen", "", "listening address")
	flag.StringVar(&configPath, "config", "", "path to configuration file")
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.Parse()

	var cfg *config.Server
	if configPath != "" {
		var err error
		cfg, err = config.Load(configPath)
		if err != nil {
			log.Fatalf("failed to load config file: %v", err)
		}
	} else {
		cfg = config.Defaults()
	}

	if listen != "" {
		cfg.Listen = append(cfg.Listen, listen)
	}
	if len(cfg.Listen) == 0 {
		cfg.Listen = []string{":6697"}
	}

	db, err := soju.OpenSQLDB(cfg.SQLDriver, cfg.SQLSource)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	var tlsCfg *tls.Config
	if cfg.TLS != nil {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertPath, cfg.TLS.KeyPath)
		if err != nil {
			log.Fatalf("failed to load TLS certificate and key: %v", err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	srv := soju.NewServer(db)
	// TODO: load from config/DB
	srv.Hostname = cfg.Hostname
	srv.LogPath = cfg.LogPath
	srv.HTTPOrigins = cfg.HTTPOrigins
	srv.AcceptProxyIPs = cfg.AcceptProxyIPs
	srv.Debug = debug

	for _, listen := range cfg.Listen {
		listenURI := listen
		if !strings.Contains(listenURI, ":/") {
			// This is a raw domain name, make it an URL with an empty scheme
			listenURI = "//" + listenURI
		}
		u, err := url.Parse(listenURI)
		if err != nil {
			log.Fatalf("failed to parse listen URI %q: %v", listen, err)
		}

		switch u.Scheme {
		case "ircs", "":
			if tlsCfg == nil {
				log.Fatalf("failed to listen on %q: missing TLS configuration", listen)
			}
			host := u.Host
			if _, _, err := net.SplitHostPort(host); err != nil {
				host = host + ":6697"
			}
			ircsTLSCfg := tlsCfg.Clone()
			ircsTLSCfg.NextProtos = []string{"irc"}
			ln, err := tls.Listen("tcp", host, ircsTLSCfg)
			if err != nil {
				log.Fatalf("failed to start TLS listener on %q: %v", listen, err)
			}
			ln = proxyProtoListener(ln, srv)
			go func() {
				if err := srv.Serve(ln); err != nil {
					log.Printf("serving %q: %v", listen, err)
				}
			}()
		case "irc+insecure":
			host := u.Host
			if _, _, err := net.SplitHostPort(host); err != nil {
				host = host + ":6667"
			}
			ln, err := net.Listen("tcp", host)
			if err != nil {
				log.Fatalf("failed to start listener on %q: %v", listen, err)
			}
			ln = proxyProtoListener(ln, srv)
			go func() {
				if err := srv.Serve(ln); err != nil {
					log.Printf("serving %q: %v", listen, err)
				}
			}()
		case "unix":
			ln, err := net.Listen("unix", u.Path)
			if err != nil {
				log.Fatalf("failed to start listener on %q: %v", listen, err)
			}
			ln = proxyProtoListener(ln, srv)
			go func() {
				if err := srv.Serve(ln); err != nil {
					log.Printf("serving %q: %v", listen, err)
				}
			}()
		case "wss":
			addr := u.Host
			if _, _, err := net.SplitHostPort(addr); err != nil {
				addr = addr + ":https"
			}
			wssTLSCfg := tlsCfg.Clone()
			wssTLSCfg.NextProtos = []string{http2.NextProtoTLS, "http/1.1"}
			ln, err := tls.Listen("tcp", addr, wssTLSCfg)
			if err != nil {
				log.Fatalf("failed to start listener on %q: %v", listen, err)
			}
			ln = proxyProtoListener(ln, srv)
			httpSrv := http.Server{
				Handler: srv,
				ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
					if proxyConn, ok := conn.(*proxyproto.Conn); ok {
						ctx = soju.ContextWithProxyHeader(ctx, proxyConn.ProxyHeader())
					}
					return ctx
				},
			}
			http2.ConfigureServer(&httpSrv, nil)
			go func() {
				if err := httpSrv.Serve(ln); err != nil {
					log.Fatalf("serving %q: %v", listen, err)
				}
			}()
		case "ws+insecure":
			addr := u.Host
			if _, _, err := net.SplitHostPort(addr); err != nil {
				addr = addr + ":http"
			}
			httpSrv := http.Server{
				Addr:    addr,
				Handler: srv,
			}
			go func() {
				if err := httpSrv.ListenAndServe(); err != nil {
					log.Fatalf("serving %q: %v", listen, err)
				}
			}()
		case "ident":
			if srv.Identd == nil {
				srv.Identd = soju.NewIdentd()
			}

			host := u.Host
			if _, _, err := net.SplitHostPort(host); err != nil {
				host = host + ":113"
			}
			ln, err := net.Listen("tcp", host)
			if err != nil {
				log.Fatalf("failed to start listener on %q: %v", listen, err)
			}
			ln = proxyProtoListener(ln, srv)
			go func() {
				if err := srv.Identd.Serve(ln); err != nil {
					log.Printf("serving %q: %v", listen, err)
				}
			}()
		default:
			log.Fatalf("failed to listen on %q: unsupported scheme", listen)
		}

		log.Printf("server listening on %q", listen)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}

	<-sigCh
	log.Print("shutting down server")
	srv.Shutdown()
}

func proxyProtoListener(ln net.Listener, srv *soju.Server) net.Listener {
	return &proxyproto.Listener{
		Listener: ln,
		Policy: func(upstream net.Addr) (proxyproto.Policy, error) {
			tcpAddr, ok := upstream.(*net.TCPAddr)
			if !ok {
				return proxyproto.IGNORE, nil
			}
			if srv.AcceptProxyIPs.Contains(tcpAddr.IP) {
				return proxyproto.USE, nil
			}
			return proxyproto.IGNORE, nil
		},
	}
}
