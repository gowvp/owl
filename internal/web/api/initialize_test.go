package api

import (
	"encoding/json/v2"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gowvp/owl/internal/core/metadata"
	"github.com/gowvp/owl/internal/core/metadata/metadataapi"
	"github.com/gowvp/owl/internal/core/metadata/stores/metadatadb"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gowvp/owl/internal/conf"
)

func initRequest(t *testing.T, api UserAPI, r http.Handler, optional map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(encryptedLoginBody(t, api, `{"username":"operator","password":"chosen-password"}`)), &body); err != nil {
		t.Fatal(err)
	}
	for key, value := range optional {
		body[key] = value
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/init", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestInitializeLifecycle(t *testing.T) {
	for _, credentials := range [][2]string{{"", ""}, {"existing", ""}, {"", "existing"}} {
		t.Run(credentials[0]+credentials[1], func(t *testing.T) {
			api, r := newLoginTestAPI(t)
			api.conf.Server.Username, api.conf.Server.Password = credentials[0], credentials[1]
			api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
			originalSIP := api.conf.Sip
			if w := initRequest(t, api, r, nil); w.Code != 200 {
				t.Fatalf("init: %d %s", w.Code, w.Body.String())
			}
			var saved conf.Bootstrap
			if err := conf.SetupConfig(&saved, api.conf.ConfigPath); err != nil {
				t.Fatal(err)
			}
			if saved.Server.Username != "operator" || saved.Server.Password != "chosen-password" || saved.Sip != originalSIP {
				t.Fatal("credentials/options not persisted")
			}
			if w := initRequest(t, api, r, nil); w.Code != 400 {
				t.Fatalf("repeat: %d", w.Code)
			}
			w := loginRequest(r, encryptedLoginBody(t, api, `{"username":"operator","password":"chosen-password"}`))
			if w.Code != 200 {
				t.Fatalf("new login: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestInitializeExistingAccount(t *testing.T) {
	api, r := newLoginTestAPI(t)
	api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	if w := initRequest(t, api, r, nil); w.Code != 400 {
		t.Fatalf("existing: %d", w.Code)
	}
	if _, err := os.Stat(api.conf.ConfigPath); !os.IsNotExist(err) {
		t.Fatal("existing configuration overwritten")
	}
}

func TestInitializePersistenceFailure(t *testing.T) {
	api, r := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	api.conf.ConfigPath = filepath.Join(t.TempDir(), "missing", "config.toml")
	original := *api.conf
	if w := initRequest(t, api, r, nil); w.Code != 500 {
		t.Fatalf("write failure: %d %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(api.conf.Server, original.Server) || api.conf.Sip != original.Sip || api.conf.ONVIF != original.ONVIF {
		t.Fatal("failed save changed runtime")
	}
	api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	if w := initRequest(t, api, r, nil); w.Code != 200 {
		t.Fatalf("retry: %d", w.Code)
	}
}

func TestInitializeConcurrent(t *testing.T) {
	api, r := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	body := encryptedLoginBody(t, api, `{"username":"operator","password":"chosen-password"}`)
	results := make(chan int, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/init", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			results <- w.Code
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for code := range results {
		if code == 200 {
			successes++
		} else if code != 400 {
			t.Fatalf("concurrent: %d", code)
		}
	}
	if successes != 1 {
		t.Fatalf("successes: %d", successes)
	}
}

func TestInitializeOptionsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options map[string]any
		status  int
	}{
		{"options", map[string]any{"sip": map[string]any{"host": "192.0.2.10", "id": "34020000002000000001", "password": "sip-secret"}, "onvif_password": "onvif-secret"}, 200},
		{"invalid id", map[string]any{"sip": map[string]any{"id": "invalid"}}, 400},
		{"invalid host", map[string]any{"sip": map[string]any{"host": "http://bad/path"}}, 400},
		{"port cannot change", map[string]any{"sip": map[string]any{"port": 16060}}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, r := newLoginTestAPI(t)
			api.conf.Server.Username = ""
			api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
			w := initRequest(t, api, r, tc.options)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 200 {
				if api.conf.Server.Username != "" {
					t.Fatal("invalid request initialized account")
				}
				return
			}
			var saved conf.Bootstrap
			if err := conf.SetupConfig(&saved, api.conf.ConfigPath); err != nil {
				t.Fatal(err)
			}
			if saved.Sip.Host != "192.0.2.10" || saved.Sip.ID != "34020000002000000001" || saved.Sip.Password != "sip-secret" || saved.ONVIF.Password != "onvif-secret" {
				t.Fatal("options missing")
			}
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response["restart_required"] != true || response["token"] != nil {
				t.Fatal("restart/login response contract")
			}
		})
	}
}

func TestInitializeGateAndAPISecret(t *testing.T) {
	api, _ := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	r := gin.New()
	auth := api.requireInitialized(AuthMiddleware("jwt-test-secret", "test-api-secret", "", ""))
	r.GET("/protected", auth, func(c *gin.Context) { c.JSON(200, gin.H{"username": c.GetString("username")}) })
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer test-api-secret")
		r.ServeHTTP(w, req)
		return w
	}
	if w := request(); w.Code != 409 {
		t.Fatalf("uninitialized API-secret access: %d", w.Code)
	}
	if err := api.conf.Update(func(next *conf.Bootstrap) error {
		next.Server.Username, next.Server.Password = "operator", "chosen"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := request()
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"operator"`) {
		t.Fatalf("initialized API-secret identity: %d %s", w.Code, w.Body.String())
	}
}

func TestCredentialsChangeWithoutAdminFallback(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			api, r := newLoginTestAPI(t)
			api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
			if empty {
				api.conf.Server.Password = ""
			}
			body := encryptedLoginBody(t, api, `{"username":"updated","password":"updated-password","old_password":"admin"}`)
			req := httptest.NewRequest("PUT", "/users", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if empty {
				if w.Code != 400 || api.conf.Server.Password != "" {
					t.Fatalf("empty password fallback: %d", w.Code)
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("ordinary change: %d %s", w.Code, w.Body.String())
			}
			var saved conf.Bootstrap
			if err := conf.SetupConfig(&saved, api.conf.ConfigPath); err != nil {
				t.Fatal(err)
			}
			if saved.Server.Username != "updated" || saved.Server.Password != "updated-password" {
				t.Fatal("normal account edit not saved")
			}
		})
	}
}

func TestInitializeInvalidCredentials(t *testing.T) {
	for _, input := range []string{`{"username":"","password":"chosen"}`, `{"username":"operator","password":" "}`, `{"username":"operator"}`, `not-json`, fmt.Sprintf(`{"username":"%s","password":"%s"}`, strings.Repeat("u", 60), strings.Repeat("p", 100))} {
		api, r := newLoginTestAPI(t)
		api.conf.Server.Username = ""
		api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
		body := encryptedLoginBody(t, api, input)
		req := httptest.NewRequest("POST", "/init", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 400 || !api.conf.NeedsInit() {
			t.Fatalf("invalid credentials: %d", w.Code)
		}
		if _, err := os.Stat(api.conf.ConfigPath); !os.IsNotExist(err) {
			t.Fatal("invalid request wrote config")
		}
	}
}

func TestInitializeBodyLimit(t *testing.T) {
	api, r := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	body := `{"unused":"` + strings.Repeat("x", 9000) + `",` + strings.TrimPrefix(encryptedLoginBody(t, api, `{"username":"operator","password":"chosen"}`), "{")
	req := httptest.NewRequest("POST", "/init", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 || !api.conf.NeedsInit() {
		t.Fatalf("oversized request: %d", w.Code)
	}
}

func TestInitializeMetadataFlow(t *testing.T) {
	api, r := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	core := metadata.NewCore(metadatadb.NewDB(db).AutoMigrate(true))
	metadataapi.RegisterMetadata(r, metadataapi.NewMetadataAPI(core).WithInitializationStatus(api.conf.NeedsInit))
	check := func(want bool) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/init", nil))
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || response["need_init"] != want {
			t.Fatalf("status endpoint: %d %v", w.Code, response)
		}
	}
	check(true)
	key := httptest.NewRecorder()
	r.ServeHTTP(key, httptest.NewRequest("GET", "/login/key", nil))
	if key.Code != 200 {
		t.Fatalf("key: %d", key.Code)
	}
	if w := initRequest(t, api, r, map[string]any{"onvif_password": ""}); w.Code != 200 {
		t.Fatalf("init: %d", w.Code)
	}
	check(false)
	if w := loginRequest(r, encryptedLoginBody(t, api, `{"username":"operator","password":"chosen-password"}`)); w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
}
