package middleware

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/clipcascade/server/config"
)

func TestCheckClearsExpiredLockAndAllowsRequest(t *testing.T) {
	bf := NewBruteForceProtection(&config.Config{
		MaxAttemptsPerIP:   3,
		LockTimeoutSeconds: 1,
		LockScalingFactor:  2,
	})
	const requestIP = "0.0.0.0"
	bf.attempts[requestIP] = &attemptInfo{
		Count:    3,
		LockedAt: time.Now().Add(-5 * time.Second),
		LockDur:  time.Second,
	}

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		return bf.Check(c)
	})
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(fiber.MethodGet, "http://127.0.0.1/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	if _, exists := bf.attempts[requestIP]; exists {
		t.Fatal("expired lock should be removed after Check")
	}
}

func TestCheckAllowsHandlerToRecordSuccessWithoutDeadlock(t *testing.T) {
	bf := NewBruteForceProtection(&config.Config{
		MaxAttemptsPerIP:   3,
		LockTimeoutSeconds: 1,
		LockScalingFactor:  2,
	})

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		return bf.Check(c)
	})
	app.Post("/login", func(c *fiber.Ctx) error {
		bf.RecordSuccess(c.IP())
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(fiber.MethodPost, "http://127.0.0.1/login", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
}
