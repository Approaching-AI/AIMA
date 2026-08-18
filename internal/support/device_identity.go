package support

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	deviceIdentityAlgorithm       = "ed25519"
	deviceIdentityStorageClass    = "software"
	deviceIdentitySessionMethod   = "POST"
	deviceIdentitySessionPath     = "/api/v1/devices/identity/session"
	deviceIdentityCanonicalV1     = "AIMA-DEVICE-AUTH-v1"
	deviceIdentityEmptyBodySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type identityKeyResponse struct {
	KeyID          string `json:"key_id"`
	DeviceID       string `json:"device_id"`
	Algorithm      string `json:"algorithm"`
	StorageClass   string `json:"storage_class"`
	AssuranceLevel string `json:"assurance_level"`
	Status         string `json:"status"`
}

type identityChallengeResponse struct {
	ChallengeID             string `json:"challenge_id"`
	DeviceID                string `json:"device_id"`
	KeyID                   string `json:"key_id"`
	Nonce                   string `json:"nonce"`
	ExpiresAt               string `json:"expires_at"`
	CanonicalizationVersion string `json:"canonicalization_version"`
}

type identitySessionResponse struct {
	Token                          string `json:"token"`
	TokenExpiresAt                 string `json:"token_expires_at"`
	DeviceID                       string `json:"device_id"`
	KeyID                          string `json:"key_id"`
	AssuranceLevel                 string `json:"assurance_level"`
	TokenKind                      string `json:"token_kind"`
	TokenPersistence               string `json:"token_persistence"`
	PersistentTokenFallbackEnabled bool   `json:"persistent_token_fallback_enabled"`
}

func isNonPersistentToken(persistence string) bool {
	switch strings.ToLower(strings.TrimSpace(persistence)) {
	case "bootstrap_only", "session_only":
		return true
	default:
		return false
	}
}

func hasLocalIdentityKey(state deviceState) bool {
	if strings.TrimSpace(state.DeviceID) == "" {
		return false
	}
	if strings.TrimSpace(state.IdentityKeyID) == "" || strings.TrimSpace(state.IdentityPrivateKeyPEM) == "" {
		return false
	}
	identityDeviceID := strings.TrimSpace(state.IdentityDeviceID)
	return identityDeviceID == "" || identityDeviceID == strings.TrimSpace(state.DeviceID)
}

func (s *Service) ensureIdentitySession(ctx context.Context, endpoint string, state deviceState) (deviceState, bool, error) {
	if strings.TrimSpace(state.DeviceID) == "" {
		return state, false, nil
	}
	if !hasLocalIdentityKey(state) {
		if strings.TrimSpace(state.Token) == "" {
			return state, false, nil
		}
		next, err := s.enrollIdentityKey(ctx, endpoint, state)
		if err != nil {
			return state, false, err
		}
		state = next
	}
	return s.refreshTokenWithIdentity(ctx, endpoint, state)
}

func (s *Service) enrollIdentityKey(ctx context.Context, endpoint string, state deviceState) (deviceState, error) {
	publicPEM, privatePEM, err := generateIdentityKeyPEM()
	if err != nil {
		return state, err
	}

	var resp identityKeyResponse
	body := map[string]any{
		"device_id":      state.DeviceID,
		"public_key_pem": publicPEM,
		"algorithm":      deviceIdentityAlgorithm,
		"storage_class":  deviceIdentityStorageClass,
	}
	if err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/identity/enroll", state.Token, body, &resp); err != nil {
		return state, err
	}
	if resp.KeyID == "" {
		return state, fmt.Errorf("device identity enroll response missing key_id")
	}
	state.IdentityDeviceID = firstNonEmpty(resp.DeviceID, state.DeviceID)
	state.IdentityKeyID = resp.KeyID
	state.IdentityPrivateKeyPEM = privatePEM
	state.IdentityPublicKeyPEM = publicPEM
	state.IdentityAlgorithm = firstNonEmpty(resp.Algorithm, deviceIdentityAlgorithm)
	state.IdentityStorageClass = firstNonEmpty(resp.StorageClass, deviceIdentityStorageClass)
	if err := s.saveState(ctx, state); err != nil {
		return state, err
	}
	return state, nil
}

func (s *Service) refreshTokenWithIdentity(ctx context.Context, endpoint string, state deviceState) (deviceState, bool, error) {
	if !hasLocalIdentityKey(state) {
		return state, false, nil
	}
	privateKey, err := parseIdentityPrivateKey(state.IdentityPrivateKeyPEM)
	if err != nil {
		return state, true, err
	}

	var challenge identityChallengeResponse
	challengeReq := map[string]any{
		"device_id": state.DeviceID,
		"key_id":    state.IdentityKeyID,
		"purpose":   "session",
	}
	if err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/identity/challenge", "", challengeReq, &challenge); err != nil {
		return state, true, err
	}
	if challenge.ChallengeID == "" || challenge.Nonce == "" {
		return state, true, fmt.Errorf("device identity challenge response missing challenge_id or nonce")
	}
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	bodySHA := deviceIdentityEmptyBodySHA256
	signature := signIdentitySession(privateKey, state.DeviceID, state.IdentityKeyID, timestamp, challenge.Nonce, bodySHA)

	var session identitySessionResponse
	sessionReq := map[string]any{
		"device_id":    state.DeviceID,
		"key_id":       state.IdentityKeyID,
		"challenge_id": challenge.ChallengeID,
		"nonce":        challenge.Nonce,
		"timestamp":    timestamp,
		"method":       deviceIdentitySessionMethod,
		"path":         deviceIdentitySessionPath,
		"body_sha256":  bodySHA,
		"signature":    signature,
	}
	if err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/identity/session", "", sessionReq, &session); err != nil {
		return state, true, err
	}
	if session.Token == "" {
		return state, true, fmt.Errorf("device identity session response missing token")
	}
	state.Token = session.Token
	state.TokenExpiresAt = session.TokenExpiresAt
	state.TokenKind = firstNonEmpty(session.TokenKind, "session_ticket")
	state.TokenPersistence = firstNonEmpty(session.TokenPersistence, "session_only")
	state.PersistentTokenFallbackEnabled = session.PersistentTokenFallbackEnabled
	if session.DeviceID != "" {
		state.IdentityDeviceID = session.DeviceID
	}
	if session.KeyID != "" {
		state.IdentityKeyID = session.KeyID
	}
	if err := s.saveState(ctx, state); err != nil {
		return state, true, err
	}
	return state, true, nil
}

func generateIdentityKeyPEM() (string, string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate device identity key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal device identity public key: %w", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal device identity private key: %w", err)
	}
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	return publicPEM, privatePEM, nil
}

func parseIdentityPrivateKey(raw string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, fmt.Errorf("device identity private key PEM is invalid")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse device identity private key: %w", err)
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("device identity private key is not Ed25519")
	}
	return privateKey, nil
}

func signIdentitySession(privateKey ed25519.PrivateKey, deviceID, keyID, timestamp, nonce, bodySHA string) string {
	message := strings.Join([]string{
		deviceIdentityCanonicalV1,
		"device_id:" + deviceID,
		"key_id:" + keyID,
		"timestamp:" + timestamp,
		"nonce:" + nonce,
		"method:" + deviceIdentitySessionMethod,
		"path:" + deviceIdentitySessionPath,
		"body_sha256:" + strings.ToLower(bodySHA),
	}, "\n")
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(message)))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
