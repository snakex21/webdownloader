// Package webui owns the frontend assets embedded in the desktop application.
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"time"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	URL      string
	server   *http.Server
	listener net.Listener
}

func StartServer() (*Server, error) {
	webRoot, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(
			"Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"font-src 'self'; script-src 'self'; connect-src 'self'",
		)
		response.Header().Set("Cache-Control", "no-store")
		http.FileServer(http.FS(webRoot)).ServeHTTP(response, request)
	})

	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	server := &Server{
		URL:      "http://" + listener.Addr().String() + "/",
		server:   httpServer,
		listener: listener,
	}

	go func() {
		_ = httpServer.Serve(listener)
	}()
	return server, nil
}

func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func Translations() (map[string]any, error) {
	data, err := assets.ReadFile("web/i18n.json")
	if err != nil {
		return nil, err
	}

	translations := make(map[string]any)
	if err := json.Unmarshal(data, &translations); err != nil {
		return nil, err
	}
	return translations, nil
}
