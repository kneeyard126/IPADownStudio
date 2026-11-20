package manager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"ipadownstudio/internal/config"
	"ipadownstudio/internal/ipatool"
)

type pending2FA struct {
	ChallengeID string
	Email       string
	Password    string
	ProfileID   string
	Region      string
	ExpiresAt   time.Time
	Attempts    int
}

// LoginResult is returned to the UI after initiating or completing Apple ID authentication.
type LoginResult struct {
	Success     bool                  `json:"success"`
	Needs2FA    bool                  `json:"needs2fa,omitempty"`
	ChallengeID string                `json:"challengeId,omitempty"`
	Email       string                `json:"email,omitempty"`
	Account     *config.AccountRecord `json:"account,omitempty"`
	Message     string                `json:"message,omitempty"`
}

// AccountsSnapshot lists persisted profiles alongside the active selection.
type AccountsSnapshot struct {
	ActiveProfileID string                 `json:"activeProfileId"`
	Accounts        []config.AccountRecord `json:"accounts"`
}

// AccountManager coordinates isolated Apple ID sessions and in-memory 2FA challenges.
type AccountManager struct {
	mu         sync.RWMutex
	store      *config.Store
	engine     *ipatool.Client
	hub        *EventHub
	challenges map[string]*pending2FA
}

// NewAccountManager creates an AccountManager.
func NewAccountManager(store *config.Store, engine *ipatool.Client, hub *EventHub) *AccountManager {
	return &AccountManager{
		store:      store,
		engine:     engine,
		hub:        hub,
		challenges: make(map[string]*pending2FA),
	}
}

// ProfileIDForEmail derives a deterministic profile identifier from an Apple ID email.
func ProfileIDForEmail(email string) string {
	norm := strings.ToLower(strings.TrimSpace(email))
	sum := sha256.Sum256([]byte(norm))
	return "acc_" + hex.EncodeToString(sum[:])[:12]
}

// Snapshot returns all saved account metadata and the active profile ID.
func (m *AccountManager) Snapshot() (AccountsSnapshot, error) {
	accounts, err := m.store.LoadAccounts()
	if err != nil {
		return AccountsSnapshot{}, err
	}
	cfg, err := m.store.LoadConfig()
	if err != nil {
		return AccountsSnapshot{}, err
	}
	return AccountsSnapshot{
		ActiveProfileID: cfg.ActiveProfileID,
		Accounts:        accounts,
	}, nil
}

// GetProfile resolves a specific profile ID or falls back to the currently active account.
func (m *AccountManager) GetProfile(profileID string) (*config.AccountRecord, string, error) {
	accounts, err := m.store.LoadAccounts()
	if err != nil {
		return nil, "", err
	}
	cfg, err := m.store.LoadConfig()
	if err != nil {
		return nil, "", err
	}
	target := strings.TrimSpace(profileID)
	if target == "" {
		target = cfg.ActiveProfileID
	}
	if target == "" {
		return nil, "", errors.New("no active Apple ID account selected; please sign in first")
	}
	for i := range accounts {
		if accounts[i].ProfileID == target {
			rec := accounts[i]
			stateDir := m.store.ProfileStateDir(rec.ProfileID)
			_ = os.MkdirAll(stateDir, 0o755)
			return &rec, stateDir, nil
		}
	}
	return nil, "", fmt.Errorf("account profile %q not found", target)
}

// StartLogin initiates Apple ID login via ipatool and enters 2FA challenge flow when required.
func (m *AccountManager) StartLogin(ctx context.Context, email, password, region string) (*LoginResult, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return nil, errors.New("Apple ID email and password are required")
	}
	profileID := ProfileIDForEmail(email)
	stateDir := m.store.ProfileStateDir(profileID)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}

	res, err := m.engine.AuthLogin(ctx, profileID, stateDir, email, password, "")
	if err != nil {
		if errors.Is(err, ipatool.ErrNeeds2FA) {
			chID := randomHex(12)
			m.mu.Lock()
			m.purgeExpiredLocked()
			m.challenges[chID] = &pending2FA{
				ChallengeID: chID,
				Email:       email,
				Password:    password,
				ProfileID:   profileID,
				Region:      strings.ToLower(strings.TrimSpace(region)),
				ExpiresAt:   time.Now().Add(5 * time.Minute),
			}
			m.mu.Unlock()
			return &LoginResult{
				Success:     false,
				Needs2FA:    true,
				ChallengeID: chID,
				Email:       email,
				Message:     "Two-factor authentication code required",
			}, nil
		}
		return nil, err
	}

	rec, saveErr := m.upsertAccount(profileID, email, res.Name, region, "valid", true)
	if saveErr != nil {
		return nil, saveErr
	}
	return &LoginResult{
		Success: true,
		Email:   rec.Email,
		Account: rec,
	}, nil
}

// Submit2FA completes an in-memory 2FA login challenge without writing credentials to disk.
func (m *AccountManager) Submit2FA(ctx context.Context, challengeID, authCode string) (*LoginResult, error) {
	code := strings.TrimSpace(authCode)
	if challengeID == "" || code == "" {
		return nil, errors.New("challenge ID and 2FA verification code are required")
	}

	m.mu.Lock()
	m.purgeExpiredLocked()
	ch, ok := m.challenges[challengeID]
	if !ok {
		m.mu.Unlock()
		return nil, errors.New("2FA challenge expired or not found; please sign in again")
	}
	if ch.Attempts >= 5 {
		ch.Password = ""
		delete(m.challenges, challengeID)
		m.mu.Unlock()
		return nil, errors.New("too many failed 2FA attempts; please sign in again")
	}
	ch.Attempts++
	email := ch.Email
	password := ch.Password
	profileID := ch.ProfileID
	region := ch.Region
	m.mu.Unlock()

	stateDir := m.store.ProfileStateDir(profileID)
	res, err := m.engine.AuthLogin(ctx, profileID, stateDir, email, password, code)
	if err != nil {
		if !errors.Is(err, ipatool.ErrNeeds2FA) {
			m.Cancel2FA(challengeID)
		}
		return nil, err
	}

	m.Cancel2FA(challengeID)
	rec, saveErr := m.upsertAccount(profileID, email, res.Name, region, "valid", true)
	if saveErr != nil {
		return nil, saveErr
	}
	return &LoginResult{
		Success: true,
		Email:   rec.Email,
		Account: rec,
	}, nil
}

// Cancel2FA clears a pending in-memory 2FA challenge.
func (m *AccountManager) Cancel2FA(challengeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.challenges[challengeID]; ok {
		ch.Password = ""
		delete(m.challenges, challengeID)
	}
}

// SwitchAccount changes the active Apple ID profile.
func (m *AccountManager) SwitchAccount(profileID string) (*config.AccountRecord, error) {
	accounts, err := m.store.LoadAccounts()
	if err != nil {
		return nil, err
	}
	var found *config.AccountRecord
	now := time.Now().UTC()
	for i := range accounts {
		if accounts[i].ProfileID == profileID {
			accounts[i].LastUsedAt = now
			copyRec := accounts[i]
			found = &copyRec
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("account %q not found", profileID)
	}
	if err := m.store.SaveAccounts(accounts); err != nil {
		return nil, err
	}
	cfg, err := m.store.LoadConfig()
	if err != nil {
		return nil, err
	}
	cfg.ActiveProfileID = profileID
	if err := m.store.SaveConfig(cfg); err != nil {
		return nil, err
	}
	m.broadcastAccounts()
	return found, nil
}

// VerifyAccount runs `ipatool auth info` for the specified profile and updates its status.
func (m *AccountManager) VerifyAccount(ctx context.Context, profileID string) (*config.AccountRecord, error) {
	rec, stateDir, err := m.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	res, authErr := m.engine.AuthInfo(ctx, rec.ProfileID, stateDir)
	status := "valid"
	name := rec.Name
	email := rec.Email
	if authErr != nil {
		status = "expired"
	} else {
		if res.Name != "" {
			name = res.Name
		}
		if res.Email != "" {
			email = res.Email
		}
	}
	updated, saveErr := m.upsertAccount(rec.ProfileID, email, name, rec.Region, status, false)
	if saveErr != nil {
		return nil, saveErr
	}
	if authErr != nil {
		return updated, authErr
	}
	return updated, nil
}

// RemoveAccount revokes ipatool credentials (best-effort), deletes isolated state dir, and removes the record.
func (m *AccountManager) RemoveAccount(ctx context.Context, profileID string) error {
	stateDir := m.store.ProfileStateDir(profileID)
	_ = m.engine.AuthRevoke(ctx, profileID, stateDir)
	_ = os.RemoveAll(stateDir)

	accounts, err := m.store.LoadAccounts()
	if err != nil {
		return err
	}
	filtered := make([]config.AccountRecord, 0, len(accounts))
	for _, a := range accounts {
		if a.ProfileID != profileID {
			filtered = append(filtered, a)
		}
	}
	if err := m.store.SaveAccounts(filtered); err != nil {
		return err
	}

	cfg, err := m.store.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.ActiveProfileID == profileID {
		if len(filtered) > 0 {
			cfg.ActiveProfileID = filtered[0].ProfileID
		} else {
			cfg.ActiveProfileID = ""
		}
		if err := m.store.SaveConfig(cfg); err != nil {
			return err
		}
	}
	m.broadcastAccounts()
	return nil
}

func (m *AccountManager) upsertAccount(profileID, email, name, region, status string, setActive bool) (*config.AccountRecord, error) {
	accounts, err := m.store.LoadAccounts()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var saved config.AccountRecord
	found := false
	for i := range accounts {
		if accounts[i].ProfileID == profileID {
			if email != "" {
				accounts[i].Email = email
			}
			if name != "" {
				accounts[i].Name = name
			}
			if region != "" {
				accounts[i].Region = region
			}
			accounts[i].Status = status
			accounts[i].LastUsedAt = now
			saved = accounts[i]
			found = true
			break
		}
	}
	if !found {
		saved = config.AccountRecord{
			ProfileID:  profileID,
			Email:      email,
			Name:       name,
			Region:     strings.ToLower(strings.TrimSpace(region)),
			Status:     status,
			AddedAt:    now,
			LastUsedAt: now,
		}
		accounts = append(accounts, saved)
	}
	if err := m.store.SaveAccounts(accounts); err != nil {
		return nil, err
	}
	if setActive {
		cfg, err := m.store.LoadConfig()
		if err != nil {
			return nil, err
		}
		cfg.ActiveProfileID = profileID
		if err := m.store.SaveConfig(cfg); err != nil {
			return nil, err
		}
	}
	m.broadcastAccounts()
	return &saved, nil
}

func (m *AccountManager) broadcastAccounts() {
	if m.hub == nil {
		return
	}
	if snap, err := m.Snapshot(); err == nil {
		m.hub.Broadcast("account_update", snap)
	}
}

func (m *AccountManager) purgeExpiredLocked() {
	now := time.Now()
	for id, ch := range m.challenges {
		if now.After(ch.ExpiresAt) {
			ch.Password = ""
			delete(m.challenges, id)
		}
	}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
