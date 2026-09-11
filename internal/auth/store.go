package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"fiatjaf.com/nostr"
	"golang.org/x/oauth2"
)

type OAuthFlow struct {
	StateHash [32]byte
	Provider  string
	Verifier  string
	Nonce     string
	Intent    string
	AccountID string
	ReturnTo  string
	ExpiresAt time.Time
}

func encodeRawURL(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func decodeRawURL(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }

type NostrChallenge struct {
	ChallengeHash [32]byte
	Pubkey        nostr.PubKey
	ExpiresAt     time.Time
}

type GitHubCredential struct {
	AccountID             string
	GitHubUserID          string
	Login                 string
	EncryptedAccessToken  string
	EncryptedRefreshToken string
	AccessTokenExpiresAt  time.Time
	RefreshTokenExpiresAt time.Time
	AppClientID           string
	Status                string
	UpdatedAt             time.Time
}

type Store interface {
	BeginOAuthFlow(context.Context, OAuthFlow) error
	ConsumeOAuthFlow(context.Context, [32]byte, time.Time) (OAuthFlow, error)
	FindAccountByIdentity(context.Context, string, string) (Account, error)
	AccountByID(context.Context, string) (Account, error)
	AccountByPubkey(context.Context, nostr.PubKey) (Account, error)
	CreateManagedAccount(context.Context, ExternalIdentity, ManagedKey) (Account, error)
	CreateExternalAccount(context.Context, nostr.PubKey) (Account, error)
	LinkIdentity(context.Context, ExternalIdentity) error
	ExternalIdentities(context.Context, string) ([]ExternalIdentity, error)
	RemoveIdentity(context.Context, string, string) error
	LoadManagedKey(context.Context, string) (ManagedKey, error)
	CreateSession(context.Context, Session) error
	SessionByTokenHash(context.Context, [32]byte, time.Time) (Session, Account, error)
	TouchSession(context.Context, string, time.Time, time.Time) error
	RevokeSession(context.Context, string, time.Time) error
	RevokeAccountSessions(context.Context, string, string, time.Time) error
	SaveNostrChallenge(context.Context, NostrChallenge) error
	ConsumeNostrChallenge(context.Context, [32]byte, nostr.PubKey, time.Time) error
	SaveGitHubCredential(context.Context, GitHubCredential) error
	GitHubCredentialByPubkey(context.Context, nostr.PubKey) (GitHubCredential, error)
	GitHubCredentialByAccount(context.Context, string) (GitHubCredential, error)
	UpdateGitHubCredentialStatus(context.Context, string, string, time.Time) error
	DisconnectGitHubCredential(context.Context, string, time.Time) error
}

type CredentialCodec struct{ Wrapper KeyWrapper }

func (codec CredentialCodec) Encrypt(accountID, purpose, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	sealed, err := codec.Wrapper.Wrap([]byte(plaintext), []byte("lapis-auth-credential-v1\x00"+accountID+"\x00"+purpose+"\x00"+codec.Wrapper.Version()))
	if err != nil {
		return "", err
	}
	return encodeRawURL(sealed), nil
}

func (codec CredentialCodec) Decrypt(accountID, purpose, ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	raw, err := decodeRawURL(ciphertext)
	if err != nil {
		return "", errors.New("invalid encrypted credential")
	}
	plain, err := codec.Wrapper.Unwrap(raw, []byte("lapis-auth-credential-v1\x00"+accountID+"\x00"+purpose+"\x00"+codec.Wrapper.Version()))
	if err != nil {
		return "", errors.New("decrypt credential")
	}
	defer clear(plain)
	return string(plain), nil
}

func GitHubCredentialFromToken(accountID, userID, login, clientID string, token *oauth2.Token, codec CredentialCodec) (GitHubCredential, error) {
	if token == nil || token.AccessToken == "" {
		return GitHubCredential{}, errors.New("GitHub user token is missing")
	}
	access, err := codec.Encrypt(accountID, "github-access", token.AccessToken)
	if err != nil {
		return GitHubCredential{}, err
	}
	refresh, err := codec.Encrypt(accountID, "github-refresh", token.RefreshToken)
	if err != nil {
		return GitHubCredential{}, err
	}
	refreshExpiry := time.Time{}
	if seconds, ok := token.Extra("refresh_token_expires_in").(float64); ok && seconds > 0 {
		refreshExpiry = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return GitHubCredential{
		AccountID: accountID, GitHubUserID: userID, Login: login,
		EncryptedAccessToken: access, EncryptedRefreshToken: refresh,
		AccessTokenExpiresAt: token.Expiry, RefreshTokenExpiresAt: refreshExpiry,
		AppClientID: clientID, Status: "active", UpdatedAt: time.Now(),
	}, nil
}
