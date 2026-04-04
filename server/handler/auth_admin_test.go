package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clipcascade/server/config"
	"github.com/clipcascade/server/model"
	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := model.InitDB(db); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	return db
}

func newTestAppWithTemplates() *fiber.App {
	engine := html.New("..", ".html")
	return fiber.New(fiber.Config{Views: engine})
}

func TestLoginPageSignupDisabledHidesCreateLink(t *testing.T) {
	app := newTestAppWithTemplates()
	h := &AuthHandler{Config: &config.Config{SignupEnabled: false}}
	app.Get("/login", h.LoginPage)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	body := buf.String()

	if strings.Contains(body, `href="/signup"`) {
		t.Fatalf("unexpected signup link when signup disabled")
	}
	if !strings.Contains(body, "Signup is disabled") {
		t.Fatalf("expected disabled hint in login page")
	}
}

func TestLoginPageSignupEnabledShowsCreateLink(t *testing.T) {
	app := newTestAppWithTemplates()
	h := &AuthHandler{Config: &config.Config{SignupEnabled: true}}
	app.Get("/login", h.LoginPage)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	body := buf.String()

	if !strings.Contains(body, `href="/signup"`) {
		t.Fatalf("expected signup link when signup enabled")
	}
}

func TestAdminRegisterUserAPI(t *testing.T) {
	db := newTestDB(t)
	h := &AdminHandler{DB: db, Config: &config.Config{}}

	app := fiber.New()
	app.Post("/api/admin/users", func(c *fiber.Ctx) error {
		c.Locals("username", "admin")
		return h.RegisterUser(c)
	})

	payload := map[string]string{
		"username": "user1",
		"password": "pass1234",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	var user model.User
	if err := db.Where("username = ?", "user1").First(&user).Error; err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if user.Enabled != true || user.Role != "USER" {
		t.Fatalf("unexpected created user state: enabled=%v role=%s", user.Enabled, user.Role)
	}

	var info model.UserInfo
	if err := db.Where("user_id = ?", user.ID).First(&info).Error; err != nil {
		t.Fatalf("userinfo not created: %v", err)
	}
	if info.HashRounds != 100000 {
		t.Fatalf("unexpected hash rounds: %d", info.HashRounds)
	}
}

func TestAdminUserUpdatesReturnNotFoundForMissingUser(t *testing.T) {
	db := newTestDB(t)
	h := &AdminHandler{DB: db, Config: &config.Config{}}

	app := fiber.New()
	app.Put("/api/admin/users/:id/password", func(c *fiber.Ctx) error {
		c.Locals("username", "admin")
		return h.ResetPassword(c)
	})
	app.Put("/api/admin/users/:id/role", func(c *fiber.Ctx) error {
		c.Locals("username", "admin")
		return h.SetRole(c)
	})
	app.Put("/api/admin/users/:id/status", func(c *fiber.Ctx) error {
		c.Locals("username", "admin")
		return h.ToggleUserStatus(c)
	})
	app.Put("/api/admin/users/:id/username", func(c *fiber.Ctx) error {
		c.Locals("username", "admin")
		return h.UpdateUsername(c)
	})

	testCases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "password",
			path: "/api/admin/users/999/password",
			body: `{"password":"abcd1234"}`,
		},
		{
			name: "role",
			path: "/api/admin/users/999/role",
			body: `{"role":"USER"}`,
		},
		{
			name: "status",
			path: "/api/admin/users/999/status",
			body: `{"enabled":true}`,
		},
		{
			name: "username",
			path: "/api/admin/users/999/username",
			body: `{"username":"ghost"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
			}

			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(resp.Body)
			if !strings.Contains(buf.String(), "user not found") {
				t.Fatalf("response = %q, want user not found", buf.String())
			}
		})
	}
}
