package keypool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestValidatorTestMultipleKeysUsesLatestGroupProxyConfig(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer upstream.Close()

	db := newValidatorProxyTestDB(t)
	encryptionSvc, err := encryption.NewService("")
	if err != nil {
		t.Fatalf("failed to create encryption service: %v", err)
	}

	settingsManager := config.NewSystemSettingsManager()
	clientManager := httpclient.NewHTTPClientManager()
	factory := channel.NewFactory(settingsManager, clientManager)
	provider := NewProvider(db, store.NewMemoryStore(), settingsManager, encryptionSvc)
	validator := NewKeyValidator(KeyValidatorParams{
		DB:              db,
		ChannelFactory:  factory,
		SettingsManager: settingsManager,
		KeypoolProvider: provider,
		EncryptionSvc:   encryptionSvc,
	})

	group := models.Group{
		Name:        "proxy-test",
		ChannelType: "openai",
		TestModel:   "gpt-4.1-nano",
		Upstreams: datatypes.JSON(
			`[{"url":"` + upstream.URL + `","weight":1}]`,
		),
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("failed to create group: %v", err)
	}

	keyValue := "sk-test"
	apiKey := models.APIKey{
		GroupID:  group.ID,
		KeyValue: keyValue,
		KeyHash:  encryptionSvc.Hash(keyValue),
		Status:   models.KeyStatusActive,
	}
	if err := db.Create(&apiKey).Error; err != nil {
		t.Fatalf("failed to create api key: %v", err)
	}

	// Simulate a stale cached group snapshot that predates the proxy_url override.
	staleGroup := group

	if err := db.Model(&models.Group{}).Where("id = ?", group.ID).Update("config", datatypes.JSONMap{
		"proxy_url": "http://127.0.0.1:1",
	}).Error; err != nil {
		t.Fatalf("failed to update group config: %v", err)
	}

	results, err := validator.TestMultipleKeys(&staleGroup, []string{keyValue})
	if err != nil {
		t.Fatalf("TestMultipleKeys returned unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("unexpected results length: %d", len(results))
	}
	if results[0].IsValid {
		t.Fatal("expected validation to fail via unreachable proxy")
	}
	if !strings.Contains(results[0].Error, "127.0.0.1:1") {
		t.Fatalf("expected proxy connection error, got %q", results[0].Error)
	}
}

func newValidatorProxyTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	if err := db.AutoMigrate(&models.Group{}, &models.APIKey{}); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}
	return db
}
