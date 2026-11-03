package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thurstonsand/imsgbus/internal/core"
)

// Serve answers MCP over Streamable HTTP on listen, for requests bearing token, until ctx ends.
func Serve(ctx context.Context, listen, token, version string, service *core.Service) error {
	server := sdk.NewServer(&sdk.Implementation{Name: "imsgbus", Version: version}, &sdk.ServerOptions{Instructions: instructions})
	(&tools{core: service}).register(server)
	// Stateless, so a restart or a proxy in front never strands a client on a dead session.
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{}, nil
	}
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = httpServer.Shutdown(context.WithoutCancel(ctx))
	}()
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
