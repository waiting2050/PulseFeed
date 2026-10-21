package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"pulsefeed/internal/account/domain"
	"pulsefeed/internal/shared/jwtx"
)

type memoryRepository struct {
	mu        sync.Mutex
	users     map[int64]*domain.User
	byAccount map[string]int64
	nextID    int64
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		users:     make(map[int64]*domain.User),
		byAccount: make(map[string]int64),
	}
}

func (r *memoryRepository) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byAccount[user.Account]; exists {
		return domain.ErrAccountExists
	}

	r.nextID++
	now := time.Now()
	user.ID = r.nextID
	user.CreatedAt = now
	user.UpdatedAt = now
	r.users[user.ID] = cloneUser(user)
	r.byAccount[user.Account] = user.ID
	return nil
}

func (r *memoryRepository) FindByAccount(_ context.Context, account string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, exists := r.byAccount[account]
	if !exists {
		return nil, domain.ErrUserNotFound
	}
	return cloneUser(r.users[id]), nil
}

func (r *memoryRepository) FindByID(_ context.Context, id int64) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, exists := r.users[id]
	if !exists {
		return nil, domain.ErrUserNotFound
	}
	return cloneUser(user), nil
}

func (r *memoryRepository) UpdateProfile(_ context.Context, id int64, nickname, avatarURL, bio string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, exists := r.users[id]
	if !exists {
		return domain.ErrUserNotFound
	}
	user.Nickname = nickname
	user.AvatarURL = avatarURL
	user.Bio = bio
	user.UpdatedAt = time.Now()
	return nil
}

func cloneUser(user *domain.User) *domain.User {
	cloned := *user
	return &cloned
}

func newTestService() (*Service, *jwtx.Manager) {
	repository := newMemoryRepository()
	manager := jwtx.NewManager("test-secret-key", time.Hour)
	return NewService(repository, manager), manager
}

func TestRegister(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		service, _ := newTestService()

		user, err := service.Register(context.Background(), RegisterInput{
			Account:  "  Alice_01  ",
			Password: "password123",
			Nickname: "",
		})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		if user.Account != "alice_01" {
			t.Fatalf("account not normalized: %s", user.Account)
		}
		if user.Nickname != "alice_01" {
			t.Fatalf("expected account as default nickname, got %s", user.Nickname)
		}
		if user.PasswordHash == "password123" {
			t.Fatal("password must not be stored in plaintext")
		}
		if user.ID <= 0 || user.Role != domain.RoleUser || user.Status != domain.StatusOK {
			t.Fatalf("unexpected user defaults: %+v", user)
		}
	})

	t.Run("duplicate account", func(t *testing.T) {
		service, _ := newTestService()
		input := RegisterInput{Account: "bob_2026", Password: "password123"}

		if _, err := service.Register(context.Background(), input); err != nil {
			t.Fatalf("first register: %v", err)
		}
		_, err := service.Register(context.Background(), RegisterInput{
			Account:  "BOB_2026",
			Password: "password456",
		})
		if !errors.Is(err, domain.ErrAccountExists) {
			t.Fatalf("expected ErrAccountExists, got %v", err)
		}
	})

	t.Run("invalid account", func(t *testing.T) {
		service, _ := newTestService()
		_, err := service.Register(context.Background(), RegisterInput{
			Account:  "ab",
			Password: "password123",
		})
		if !errors.Is(err, domain.ErrInvalidAccount) {
			t.Fatalf("expected ErrInvalidAccount, got %v", err)
		}
	})

	t.Run("invalid password", func(t *testing.T) {
		service, _ := newTestService()
		_, err := service.Register(context.Background(), RegisterInput{
			Account:  "carol_01",
			Password: "short",
		})
		if !errors.Is(err, domain.ErrInvalidPassword) {
			t.Fatalf("expected ErrInvalidPassword, got %v", err)
		}
	})

	t.Run("nickname too long", func(t *testing.T) {
		service, _ := newTestService()
		_, err := service.Register(context.Background(), RegisterInput{
			Account:  "dave_01",
			Password: "password123",
			Nickname: strings.Repeat("昵", 33),
		})
		if !errors.Is(err, domain.ErrInvalidNickname) {
			t.Fatalf("expected ErrInvalidNickname, got %v", err)
		}
	})
}

func TestLogin(t *testing.T) {
	service, manager := newTestService()
	if _, err := service.Register(context.Background(), RegisterInput{
		Account:  "alice_01",
		Password: "password123",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	t.Run("success", func(t *testing.T) {
		result, err := service.Login(context.Background(), "ALICE_01", "password123")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if result.Token == "" {
			t.Fatal("expected access token")
		}
		if result.ExpiresIn != int64(time.Hour.Seconds()) {
			t.Fatalf("unexpected expires_in: %d", result.ExpiresIn)
		}

		userID, err := manager.Parse(result.Token)
		if err != nil {
			t.Fatalf("parse token: %v", err)
		}
		if userID != result.User.ID {
			t.Fatalf("token subject mismatch: %d vs %d", userID, result.User.ID)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		_, err := service.Login(context.Background(), "alice_01", "wrong-password")
		if !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("expected ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("unknown account", func(t *testing.T) {
		_, err := service.Login(context.Background(), "nobody_01", "password123")
		if !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("expected ErrInvalidCredentials, got %v", err)
		}
	})
}

func TestUpdateProfile(t *testing.T) {
	service, _ := newTestService()
	user, err := service.Register(context.Background(), RegisterInput{
		Account:  "erin_01",
		Password: "password123",
		Nickname: " Erin ",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	nickname := "  Erin Zhang  "
	bio := "  backend engineer  "
	updated, err := service.UpdateProfile(context.Background(), user.ID, ProfileUpdate{
		Nickname: &nickname,
		Bio:      &bio,
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if updated.Nickname != "Erin Zhang" {
		t.Fatalf("nickname not trimmed: %q", updated.Nickname)
	}
	if updated.Bio != "backend engineer" {
		t.Fatalf("bio not trimmed: %q", updated.Bio)
	}

	t.Run("empty nickname", func(t *testing.T) {
		empty := "   "
		_, err := service.UpdateProfile(context.Background(), user.ID, ProfileUpdate{Nickname: &empty})
		if !errors.Is(err, domain.ErrInvalidNickname) {
			t.Fatalf("expected ErrInvalidNickname, got %v", err)
		}
	})

	t.Run("bio too long", func(t *testing.T) {
		long := strings.Repeat("x", 256)
		_, err := service.UpdateProfile(context.Background(), user.ID, ProfileUpdate{Bio: &long})
		if !errors.Is(err, domain.ErrInvalidProfile) {
			t.Fatalf("expected ErrInvalidProfile, got %v", err)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		_, err := service.UpdateProfile(context.Background(), 9999, ProfileUpdate{})
		if !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})
}

func TestGetByID(t *testing.T) {
	service, _ := newTestService()

	if _, err := service.GetByID(context.Background(), 0); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound for zero id, got %v", err)
	}
	if _, err := service.GetByID(context.Background(), 42); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound for unknown id, got %v", err)
	}
}
