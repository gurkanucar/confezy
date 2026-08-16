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

// Environment variables read by serve to bootstrap the admin account, for
// deployments where running an interactive command is awkward.
const (
	envAdminUsername     = "CONFEZY_ADMIN_USERNAME"
	envAdminPassword     = "CONFEZY_ADMIN_PASSWORD"
	envAdminPasswordFile = "CONFEZY_ADMIN_PASSWORD_FILE"
)

const usage = `confezy — feature flag + JSON config servisi

Kullanım:
  confezy serve [-port 8080] [-db ./data.db]
  confezy admin-create -username admin [-db ./data.db] [-reset]

Komutlar:
  serve          HTTP sunucusunu başlatır (API + admin paneli)
  admin-create   Admin paneli için hesap oluşturur; şifre terminalden sorulur

Ortam değişkenleri (serve):
  CONFEZY_ADMIN_USERNAME       Hesap yoksa açılışta bu adla oluşturulur
  CONFEZY_ADMIN_PASSWORD       Şifresi (en az 8 karakter)
  CONFEZY_ADMIN_PASSWORD_FILE  Şifreyi dosyadan okur; CONFEZY_ADMIN_PASSWORD
                               yerine kullanılır (docker/k8s secret'ları için)

  Var olan hesabın şifresi hiçbir zaman ezilmez; değiştirmek için
  admin-create -reset kullan.
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("confezy: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
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
		fmt.Fprintf(os.Stderr, "bilinmeyen komut: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		log.Fatal(err)
	}
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "dinlenecek port")
	host := fs.String("host", "", "bağlanılacak adres (boş = tüm arayüzler)")
	dbPath := fs.String("db", "./data.db", "SQLite veritabanı dosyası")
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
		log.Printf("dinleniyor http://localhost:%d  (db: %s)", *port, database.Path)
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
		log.Print("kapatılıyor…")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	return srv.Shutdown(shutdownCtx)
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
			log.Printf("hiç admin hesabı yok — 'confezy admin-create -username admin' çalıştır "+
				"veya %s / %s ver", envAdminUsername, envAdminPassword)
		}
		return nil
	}
	if username == "" || password == "" {
		return fmt.Errorf("%s ve %s (ya da %s) birlikte verilmeli",
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
		log.Printf("admin %q zaten var, şifresi değiştirilmedi "+
			"(değiştirmek için: confezy admin-create -username %s -reset)", username, username)
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

	log.Printf("admin %q ortam değişkenlerinden oluşturuldu", username)
	return nil
}

// adminPasswordFromEnv prefers the file form, so the secret can stay out of the
// process environment (and out of `docker inspect`).
func adminPasswordFromEnv() (string, error) {
	if path := strings.TrimSpace(os.Getenv(envAdminPasswordFile)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s okunamadı: %w", envAdminPasswordFile, err)
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
				log.Printf("oturum temizliği: %v", err)
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
	username := fs.String("username", "", "admin kullanıcı adı (zorunlu)")
	dbPath := fs.String("db", "./data.db", "SQLite veritabanı dosyası")
	reset := fs.Bool("reset", false, "hesap varsa şifresini değiştir")
	if err := fs.Parse(args); err != nil {
		return err
	}

	name := strings.TrimSpace(*username)
	if !model.ValidUsername(name) {
		return errors.New(model.ErrInvalidUsername.Error())
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
		return errors.New(model.ErrInvalidPassword.Error())
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	_, err = database.CreateUser(ctx, name, hash)
	if errors.Is(err, db.ErrDuplicate) {
		if !*reset {
			return fmt.Errorf("%q zaten var; şifresini değiştirmek için -reset kullan", name)
		}
		if err := database.SetUserPassword(ctx, name, hash); err != nil {
			return err
		}
		fmt.Printf("%q kullanıcısının şifresi güncellendi.\n", name)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Printf("%q oluşturuldu. Panele /ui/login adresinden gir.\n", name)
	return nil
}

// readPassword prompts twice, without echo when stdin is a terminal.
func readPassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Piped input: read a single line.
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("şifre okunamadı: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	fmt.Print("Şifre: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("şifre okunamadı: %w", err)
	}

	fmt.Print("Şifre (tekrar): ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("şifre okunamadı: %w", err)
	}

	if string(first) != string(second) {
		return "", errors.New("şifreler eşleşmedi")
	}
	return string(first), nil
}
