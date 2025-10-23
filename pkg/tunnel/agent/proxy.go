package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"k8s.io/klog/v2"
)

type proxy struct {
	maxIdleConns          int
	idleConnTimeout       time.Duration
	tLSHandshakeTimeout   time.Duration
	expectContinueTimeout time.Duration

	udsSocketPath string
	rootCAs       *x509.CertPool

	RequestProcessor
	CertificateProvider
	Router
}

func newProxy(rp RequestProcessor, cp CertificateProvider, router Router, udsSocketPath string) *proxy {
	return &proxy{
		maxIdleConns:          100,
		idleConnTimeout:       90 * time.Second,
		tLSHandshakeTimeout:   10 * time.Second,
		expectContinueTimeout: 1 * time.Second,

		udsSocketPath: udsSocketPath,

		RequestProcessor:    rp,
		CertificateProvider: cp,
		Router:              router,
	}
}

func (p *proxy) Run(ctx context.Context) error {
	// Get root CAs
	rootCAs, err := p.GetRootCAs()
	if err != nil {
		return err
	}
	p.rootCAs = rootCAs

	// Remove existing socket file if it exists
	if err := os.RemoveAll(p.udsSocketPath); err != nil {
		return fmt.Errorf("failed to remove existing socket file: %w", err)
	}

	// Create Unix domain socket listener
	listener, err := net.Listen("unix", p.udsSocketPath)
	if err != nil {
		return fmt.Errorf("failed to create UDS listener at %s: %w", p.udsSocketPath, err)
	}
	defer listener.Close()

	klog.InfoS("ServiceProxy started", "socket_path", p.udsSocketPath)

	// Create HTTP server with the serviceProxy as handler
	server := &http.Server{
		Handler: p,
		// Disable automatic HTTP/2 upgrade to support SPDY protocol used by kubectl exec
		// HTTP/2 cannot upgrade to SPDY, so we need to prevent automatic HTTP/2 negotiation
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	// Start server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		klog.InfoS("Starting HTTP server on UDS", "socket_path", p.udsSocketPath)
		errCh <- server.Serve(listener)
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		klog.InfoS("Context canceled, shutting down serviceProxy")
		// Graceful shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			klog.ErrorS(err, "Failed to gracefully shutdown serviceProxy")
		}
		// Clean up socket file
		os.RemoveAll(p.udsSocketPath)
		return ctx.Err()
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serviceProxy server failed: %w", err)
		}
		return nil
	}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	klog.V(4).InfoS("Received request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)

	targetProto, targetHost, targetPath, err := p.ParseTargetService(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get target service URL: %v", err), http.StatusInternalServerError)
		return
	}
	klog.V(4).InfoS("Target service URL", "proto", targetProto, "host", targetHost, "path", targetPath)

	err, statusCode := p.RequestProcessor.Process(targetHost, r)
	if err != nil {
		http.Error(w, fmt.Sprintf("Request processing failed: %v", err), statusCode)
		return
	}

	rp := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: targetProto, Host: targetHost})
	rp.Transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          p.maxIdleConns,
		IdleConnTimeout:       p.idleConnTimeout,
		TLSHandshakeTimeout:   p.tLSHandshakeTimeout,
		ExpectContinueTimeout: p.expectContinueTimeout,
		TLSClientConfig: &tls.Config{
			RootCAs:    p.rootCAs,
			MinVersion: tls.VersionTLS12,
		},
		// golang http pkg automaticly upgrade http connection to http2 connection, but http2 can not upgrade to SPDY which used in "kubectl exec".
		// set ForceAttemptHTTP2 = false to prevent auto http2 upgration
		ForceAttemptHTTP2: false,
	}

	rp.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, e error) {
		http.Error(rw, fmt.Sprintf("proxy to target service failed because %v", e), http.StatusBadGateway)
		klog.Errorf("proxy target service failed because %v", e)
	}

	r.URL.Path = targetPath
	rp.ServeHTTP(w, r)
}
