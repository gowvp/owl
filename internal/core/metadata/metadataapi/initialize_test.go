package metadataapi

import (
	"encoding/json/v2"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/metadata"
	"github.com/gowvp/owl/internal/core/metadata/stores/metadatadb"
	"gorm.io/gorm"
)

func TestLoginPageInitializationStatus(t *testing.T) {
	for _, stored := range []bool{false, true} {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sqlDB.Close() })
		store := metadatadb.NewDB(db).AutoMigrate(true)
		if stored {
			if err := store.Metadata().Create(t.Context(), &metadata.Metadata{ID: "login_page", Ext: `{"title":"custom"}`}); err != nil {
				t.Fatal(err)
			}
		}
		cfg := &conf.Bootstrap{}
		api := NewMetadataAPI(metadata.NewCore(store)).WithInitializationStatus(cfg.NeedsInit)
		r := gin.New()
		RegisterMetadata(r, api)
		for _, credentials := range [][2]string{{"", ""}, {"user", ""}, {"", "password"}, {"admin", "admin"}} {
			cfg.Server.Username, cfg.Server.Password = credentials[0], credentials[1]
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/metadatas/login_page", nil))
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			expected := credentials[0] == "" || credentials[1] == ""
			if result["need_init"] != expected || result["id"] != "login_page" {
				t.Fatalf("response %v", result)
			}
			if stored && result["ext"] != `{"title":"custom"}` {
				t.Fatal("appearance metadata changed")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("initialization state may be cached")
			}
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/metadatas/other", nil))
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if _, ok := result["need_init"]; ok {
			t.Fatal("status added to unrelated metadata")
		}
	}
}
