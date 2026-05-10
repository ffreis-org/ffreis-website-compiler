package servecmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ffreis-website-compiler/internal/assetusage"
	"ffreis-website-compiler/internal/cmdutil"
	"ffreis-website-compiler/internal/sitegen"
)

func Run(args []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	opts, err := parseServeOptions(args)
	if err != nil {
		return err
	}

	assetsRoot, templatesRoot, err := cmdutil.ResolveWebsitePaths(opts.websiteRoot)
	if err != nil {
		return err
	}

	pages, siteDataResult, err := loadAndValidateSiteData(logger, templatesRoot, opts.siteDataSource, opts.enableSanity)
	if err != nil {
		return err
	}
	if err := validateAssetUsage(assetsRoot, pages, siteDataResult.Data); err != nil {
		return err
	}

	srv, shutdownTimeout := newServer(opts.addr, assetsRoot, pages, siteDataResult.Data, logger)
	logServerStart(logger, opts, assetsRoot, templatesRoot, pages, srv, shutdownTimeout)
	return serveUntilShutdown(logger, srv, shutdownTimeout)
}

type serveOptions struct {
	websiteRoot    string
	siteDataSource string
	addr           string
	enableSanity   bool
}

func parseServeOptions(args []string) (serveOptions, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var opts serveOptions
	fs.StringVar(&opts.websiteRoot, "website-root", ".", "website project root; expects <website-root>/src/{assets,templates} (legacy fallback: <website-root>/{site,templates})")
	fs.StringVar(&opts.siteDataSource, "site-data", "", "optional site data source override; supports file/URL sources or a directory containing YAML layers")
	fs.StringVar(&opts.addr, "addr", ":8080", "HTTP listen address")
	fs.BoolVar(&opts.enableSanity, "sanity", true, "fail server startup if generic sanity checks fail (site contract + invariants + asset reachability)")
	if err := fs.Parse(args); err != nil {
		return serveOptions{}, err
	}
	return opts, nil
}

func loadAndValidateSiteData(logger *slog.Logger, templatesRoot, siteDataSource string, enableSanity bool) ([]sitegen.PageTemplate, sitegen.SiteDataLoadResult, error) {
	pages, err := sitegen.LoadPageTemplatesFromRoot(templatesRoot)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, fmt.Errorf("loading templates: %w", err)
	}
	siteDataResult, err := sitegen.LoadSiteData(templatesRoot, siteDataSource)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, fmt.Errorf("loading site data: %w", err)
	}
	siteDataContractResult, err := sitegen.LoadSiteDataContract(templatesRoot)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, fmt.Errorf("loading site data contract: %w", err)
	}
	cmdutil.LogSiteDataOverride(logger, siteDataResult)
	if err := sitegen.ValidateSiteDataAndUsage(pages, siteDataResult, siteDataContractResult); err != nil {
		return nil, sitegen.SiteDataLoadResult{}, err
	}
	if enableSanity {
		if err := sitegen.ValidateSiteSanity(siteDataResult.Data, sitegen.DefaultSanityConfig()); err != nil {
			return nil, sitegen.SiteDataLoadResult{}, fmt.Errorf("validating site sanity rules: %w", err)
		}
	}
	return pages, siteDataResult, nil
}

func validateAssetUsage(assetsRoot string, pages []sitegen.PageTemplate, siteData map[string]any) error {
	renderedPages, err := sitegen.RenderPages(pages, siteData)
	if err != nil {
		return err
	}
	if _, err := assetusage.Validate(assetsRoot, renderedPages); err != nil {
		return fmt.Errorf("validating local css/js asset usage: %w", err)
	}
	return nil
}

func newServer(addr, assetsRoot string, pages []sitegen.PageTemplate, siteData map[string]any, logger *slog.Logger) (*http.Server, time.Duration) {
	mux := http.NewServeMux()
	registerStatic(mux, assetsRoot)
	registerPages(mux, pages, siteData, logger)

	var handler http.Handler = mux
	handler = loggingMiddleware(logger, handler)
	handler = securityHeadersMiddleware(handler)
	handler = recoveryMiddleware(logger, handler)
	handler = requestIDMiddleware(handler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       getEnvDuration("SERVE_READ_TIMEOUT", 10*time.Second),
		WriteTimeout:      getEnvDuration("SERVE_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:       getEnvDuration("SERVE_IDLE_TIMEOUT", 60*time.Second),
		ReadHeaderTimeout: getEnvDuration("SERVE_READ_HEADER_TIMEOUT", 5*time.Second),
		MaxHeaderBytes:    getEnvInt("SERVE_MAX_HEADER_BYTES", 1_048_576),
	}
	return srv, getEnvDuration("SERVE_SHUTDOWN_TIMEOUT", 10*time.Second)
}

func logServerStart(logger *slog.Logger, opts serveOptions, assetsRoot, templatesRoot string, pages []sitegen.PageTemplate, srv *http.Server, shutdownTimeout time.Duration) {
	logger.Info(
		"starting local server",
		"addr", opts.addr,
		"website_root", opts.websiteRoot,
		"assets_dir", assetsRoot,
		"templates_dir", templatesRoot,
		"pages", len(pages),
		"read_timeout", srv.ReadTimeout.String(),
		"write_timeout", srv.WriteTimeout.String(),
		"idle_timeout", srv.IdleTimeout.String(),
		"shutdown_timeout", shutdownTimeout.String(),
	)
}

func serveUntilShutdown(logger *slog.Logger, srv *http.Server, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown failed: %w", err)
	}

	if err := <-errCh; err != nil && err != http.ErrServerClosed {
		return err
	}

	logger.Info("server shutdown complete")
	return nil
}

func registerPages(mux *http.ServeMux, pages []sitegen.PageTemplate, siteData map[string]any, logger *slog.Logger) {
	for _, page := range pages {
		if page.Name == "index" {
			mux.HandleFunc("/", makeIndexHandler(page.Tmpl, page.Name, siteData, logger))
		}
		mux.HandleFunc("/"+page.Name+".html", makePageHandler(page.Tmpl, page.Name, siteData, logger))
	}
}

func makeIndexHandler(tpl *template.Template, pageName string, siteData map[string]any, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		renderTemplate(w, r, tpl, pageName, siteData, logger)
	}
}

func makePageHandler(tpl *template.Template, pageName string, siteData map[string]any, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		renderTemplate(w, r, tpl, pageName, siteData, logger)
	}
}

func renderTemplate(w http.ResponseWriter, r *http.Request, tpl *template.Template, pageName string, siteData map[string]any, logger *slog.Logger) {
	if err := tpl.ExecuteTemplate(w, "layout", sitegen.NewTemplateData(pageName, siteData)); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		logger.Error("template execution failed", "path", r.URL.Path, "error", err)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

type ctxKey string

const ctxKeyRequestID ctxKey = "request_id"

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = generateRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, requestID)))
	})
}

func recoveryMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error(
					"panic recovered",
					"panic", rec,
					"path", r.URL.Path,
					"method", r.Method,
					"request_id", requestIDFromContext(r.Context()),
					"stack", string(debug.Stack()),
				)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				recorder.status = http.StatusInternalServerError
				panic(rec)
			}
			duration := time.Since(start)
			logger.Info(
				"http request completed",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration_ms", duration.Milliseconds(),
				"request_id", requestIDFromContext(r.Context()),
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
			)
		}()
		next.ServeHTTP(recorder, r)
	})
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(ctxKeyRequestID).(string)
	return requestID
}

func generateRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func getEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func registerStatic(mux *http.ServeMux, siteRoot string) {
	mux.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir(filepath.Join(siteRoot, "css")))))
	mux.Handle("/fonts/", http.StripPrefix("/fonts/", http.FileServer(http.Dir(filepath.Join(siteRoot, "fonts")))))
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir(filepath.Join(siteRoot, "images")))))
	mux.Handle("/js/", http.StripPrefix("/js/", http.FileServer(http.Dir(filepath.Join(siteRoot, "js")))))
	mux.Handle("/ld/", http.StripPrefix("/ld/", http.FileServer(http.Dir(filepath.Join(siteRoot, "ld")))))
	mux.Handle("/send.js", http.FileServer(http.Dir(siteRoot)))
	mux.Handle("/contactScript.js", http.FileServer(http.Dir(siteRoot)))
	mux.Handle("/robots.txt", http.FileServer(http.Dir(siteRoot)))
	mux.Handle("/sitemap.xml", http.FileServer(http.Dir(siteRoot)))
}
