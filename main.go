// Command confezy is a single-binary feature flag and JSON config service:
// SQLite storage, a JSON API for clients, and an embedded HTMX admin panel.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"confezy/internal/api"
	"confezy/internal/auth"
	"confezy/internal/db"
	"confezy/internal/model"
	"confezy/internal/ui"
)

// Environment variables read by serve. They exist for deployments where running
// an interactive command is awkward.
const (
	envAdminUsername     = "CONFEZY_ADMIN_USERNAME"
	envAdminPassword     = "CONFEZY_ADMIN_PASSWORD"
	envAdminPasswordFile = "CONFEZY_ADMIN_PASSWORD_FILE"
	envSeedData          = "CONFEZY_SEED_DATA"
)

// defaultEnvFile is loaded at startup when present; CONFEZY_ENV_FILE overrides
// the path.
const defaultEnvFile = ".env"

const usage = `confezy — feature flag and JSON config service

Usage:
  confezy serve [-port 8080] [-db ./data.db]
  confezy admin-create -username admin [-db ./data.db] [-reset]

Commands:
  serve          Start the HTTP server (JSON API + admin panel)
  admin-create   Create an admin account; the password is read from the terminal

Environment variables (serve):
  CONFEZY_ADMIN_USERNAME       Account created at startup if it does not exist
  CONFEZY_ADMIN_PASSWORD       Its password (at least 8 characters)
  CONFEZY_ADMIN_PASSWORD_FILE  Read the password from a file instead; takes
                               precedence over CONFEZY_ADMIN_PASSWORD and keeps
                               the secret out of the process environment
  CONFEZY_SEED_DATA            Insert the demo dataset into an empty database.
                               Default: 1 (enabled). Set to 0 to turn it off.

  An existing account is never modified; use admin-create -reset to change a
  password. A .env file in the working directory is loaded at startup
  (CONFEZY_ENV_FILE overrides the path); real environment variables win over it.
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("confezy: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	envFile := os.Getenv("CONFEZY_ENV_FILE")
	if envFile == "" {
		envFile = defaultEnvFile
	}
	if err := loadDotEnv(envFile); err != nil {
		log.Fatal(err)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "admin-create":
		err = cmdAdminCreate(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		log.Fatal(err)
	}
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "port to listen on")
	host := fs.String("host", "", "address to bind (empty = all interfaces)")
	dbPath := fs.String("db", "./data.db", "SQLite database file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := database.Migrate(ctx); err != nil {
		return err
	}
	if err := seedDemoData(ctx, database); err != nil {
		return err
	}
	if err := bootstrapAdmin(ctx, database); err != nil {
		return err
	}

	handler, err := buildHandler(database)
	if err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stopSessions := startSessionJanitor(database)
	defer stopSessions()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on http://localhost:%d  (db: %s)", *port, database.Path)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-quit:
		log.Print("shutting down…")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	return srv.Shutdown(shutdownCtx)
}

// seedDemoData inserts the demo dataset unless it is switched off. It only
// touches a database with no projects in it, so restarts never duplicate rows
// and real data is never overwritten.
func seedDemoData(ctx context.Context, database *db.DB) error {
	enabled, err := envBool(envSeedData, true)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}

	seeded, err := database.Seed(ctx)
	if err != nil {
		return err
	}
	if seeded {
		log.Printf("demo data inserted (set %s=0 to disable). "+
			"The seeded API keys are display-only and authenticate nothing; "+
			"create a real key from the admin panel.", envSeedData)
	}
	return nil
}

// loadDotEnv reads KEY=VALUE lines from path into the process environment. A
// missing file is not an error — the file is a convenience, not a config source
// the service depends on.
//
// Variables already set in the real environment are left alone, so an explicit
// `CONFEZY_ADMIN_PASSWORD=… ./confezy serve` and a container's env_file both win
// over the file on disk.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not read %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected 'KEY=value'", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		if _, alreadySet := os.LookupEnv(key); alreadySet {
			continue
		}
		if err := os.Setenv(key, unquote(strings.TrimSpace(value))); err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
	}
	return scanner.Err()
}

// unquote strips one layer of matching quotes. Values are otherwise taken
// literally, so a '#' inside a value stays part of the value.
func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// envBool reads a boolean environment variable, falling back to def when it is
// unset or empty. An unrecognised value is an error rather than a silent false.
func envBool(name string, def bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s: expected a boolean (1/0, true/false, yes/no), got %q", name, raw)
}

// bootstrapAdmin creates the admin account from the environment when that
// account is not there yet.
//
// An existing account is never touched: a restart must not silently undo a
// password changed with admin-create -reset, and it must not resurrect a
// password that has since been rotated.
func bootstrapAdmin(ctx context.Context, database *db.DB) error {
	username := strings.TrimSpace(os.Getenv(envAdminUsername))
	password, err := adminPasswordFromEnv()
	if err != nil {
		return err
	}

	if username == "" && password == "" {
		// Nothing configured: fall back to the CLI, but say so if the panel
		// would be unreachable.
		if count, err := database.CountUsers(ctx); err == nil && count == 0 {
			log.Printf("no admin account exists — run 'confezy admin-create -username admin' "+
				"or set %s / %s", envAdminUsername, envAdminPassword)
		}
		return nil
	}
	if username == "" || password == "" {
		return fmt.Errorf("%s and %s (or %s) must be set together",
			envAdminUsername, envAdminPassword, envAdminPasswordFile)
	}
	if !model.ValidUsername(username) {
		return fmt.Errorf("%s: %w", envAdminUsername, model.ErrInvalidUsername)
	}
	if !model.ValidPassword(password) {
		return fmt.Errorf("%s: %w", envAdminPassword, model.ErrInvalidPassword)
	}

	switch _, err := database.GetUserByUsername(ctx, username); {
	case err == nil:
		log.Printf("admin %q already exists, password left unchanged "+
			"(to change it: confezy admin-create -username %s -reset)", username, username)
		return nil
	case !errors.Is(err, db.ErrNotFound):
		return err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := database.CreateUser(ctx, username, hash); err != nil {
		if errors.Is(err, db.ErrDuplicate) {
			return nil
		}
		return err
	}

	log.Printf("admin %q created from the environment", username)
	return nil
}

// adminPasswordFromEnv prefers the file form, so the secret can stay out of the
// process environment (and out of `docker inspect`).
func adminPasswordFromEnv() (string, error) {
	if path := strings.TrimSpace(os.Getenv(envAdminPasswordFile)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("could not read %s: %w", envAdminPasswordFile, err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	return os.Getenv(envAdminPassword), nil
}

// buildHandler wires the three surfaces onto one mux: static assets, the JSON
// API (API key auth) and the admin UI (session auth).
func buildHandler(database *db.DB) (http.Handler, error) {
	mux := http.NewServeMux()

	// Embedded static assets.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static assets: %w", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/",
		cacheControl(http.FileServerFS(staticSub))))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	// JSON API.
	apiAuth := &auth.APIAuthenticator{DB: database}
	client := &api.Client{DB: database}
	client.Register(mux, apiAuth.Require(model.ScopeRead))
	manage := &api.Manage{DB: database}
	manage.Register(mux, apiAuth.Require(model.ScopeWrite))

	// Admin UI.
	sessions := &auth.Sessions{DB: database}
	uiServer, err := ui.New(database, sessions, templatesFS)
	if err != nil {
		return nil, err
	}
	uiServer.Register(mux)

	return requestLog(mux), nil
}

// startSessionJanitor prunes expired sessions hourly. The returned func stops it.
func startSessionJanitor(database *db.DB) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := database.DeleteExpiredSessions(ctx); err != nil && ctx.Err() == nil {
				log.Printf("session cleanup: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

func cmdAdminCreate(args []string) error {
	fs := flag.NewFlagSet("admin-create", flag.ExitOnError)
	username := fs.String("username", "", "admin username (required)")
	dbPath := fs.String("db", "./data.db", "SQLite database file")
	reset := fs.Bool("reset", false, "change the password if the account already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	name := strings.TrimSpace(*username)
	if !model.ValidUsername(name) {
		return model.ErrInvalidUsername
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := database.Migrate(ctx); err != nil {
		return err
	}

	password, err := readPassword()
	if err != nil {
		return err
	}
	if !model.ValidPassword(password) {
		return model.ErrInvalidPassword
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	_, err = database.CreateUser(ctx, name, hash)
	if errors.Is(err, db.ErrDuplicate) {
		if !*reset {
			return fmt.Errorf("%q already exists; pass -reset to change its password", name)
		}
		if err := database.SetUserPassword(ctx, name, hash); err != nil {
			return err
		}
		fmt.Printf("Password updated for %q.\n", name)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Printf("%q created. Sign in at /ui/login.\n", name)
	return nil
}

// readPassword prompts twice, without echo when stdin is a terminal.
func readPassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Piped input: read a single line.
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("could not read password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	fmt.Print("Password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("could not read password: %w", err)
	}

	fmt.Print("Password (again): ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("could not read password: %w", err)
	}

	if string(first) != string(second) {
		return "", errors.New("passwords did not match")
	}
	return string(first), nil
}
