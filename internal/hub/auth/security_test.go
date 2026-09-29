package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

func TestTOTPEnrollmentLoginRecoveryAndRevocation(t *testing.T) {
	a, st, clk := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("192.0.2.1")
	if err := st.SetAdminPassword(ctx, cheapPHC(goodPassword)); err != nil {
		t.Fatal(err)
	}
	session, err := a.Login(ctx, goodPassword, from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SecurityAction(ctx, SecurityInput{Action: "totp_begin"}, session, from); err == nil {
		t.Fatal("old session alone enrolled factor")
	}
	begin, err := a.SecurityAction(ctx, SecurityInput{Action: "totp_begin", Password: goodPassword}, session, from)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(begin.TOTPSecret, clk.Now().Unix()/30)
	finish, err := a.SecurityAction(ctx, SecurityInput{Action: "totp_enable", ChallengeID: begin.ChallengeID, OTP: code}, session, from)
	if err != nil {
		t.Fatal(err)
	}
	if len(finish.RecoveryCodes) != 10 {
		t.Fatal("missing one-time recovery codes")
	}
	if _, ok, _ := a.AuthenticateSession(ctx, []string{session}); ok {
		t.Fatal("factor change retained old session")
	}
	if _, err = a.Login(ctx, goodPassword, from); !errors.Is(err, ErrSecurity) {
		t.Fatalf("password bypassed factor: %v", err)
	}
	if _, err = a.LoginFactors(ctx, goodPassword, code, "", from); !errors.Is(err, ErrSecurity) {
		t.Fatalf("enrollment code replay: %v", err)
	}
	clk.Advance(30 * time.Second)
	code = totpCode(begin.TOTPSecret, clk.Now().Unix()/30)
	if _, err = a.LoginFactors(ctx, goodPassword, code, "", from); err != nil {
		t.Fatal(err)
	}
	if _, err = a.LoginFactors(ctx, goodPassword, code, "", from); !errors.Is(err, ErrSecurity) {
		t.Fatal("TOTP replay accepted")
	}
	if _, err = a.LoginFactors(ctx, goodPassword, "", finish.RecoveryCodes[0], from); err != nil {
		t.Fatal(err)
	}
	if _, err = a.LoginFactors(ctx, goodPassword, "", finish.RecoveryCodes[0], from); !errors.Is(err, ErrSecurity) {
		t.Fatal("recovery replay accepted")
	}
	b, err := st.AdminSecurity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range finish.RecoveryCodes {
		if strings.Contains(b.Data, code) {
			t.Fatal("recovery plaintext persisted")
		}
	}
	_, h := NewToken()
	if err = st.CreateSession(ctx, h, clk.Now(), clk.Now().Add(time.Hour), b.PasswordHash); !errors.Is(err, store.ErrAdminChanged) {
		t.Fatal("password-only store issuance bypassed TOTP")
	}
	if err = a.ResetSecurity(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Login(ctx, goodPassword, from); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityCASAndChallengeBindings(t *testing.T) {
	a, st, clk := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("192.0.2.1")
	if err := st.SetAdminPassword(ctx, cheapPHC(goodPassword)); err != nil {
		t.Fatal(err)
	}
	b, s, err := a.readSecurity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.commitSecurity(ctx, b, s, false, nil); err != nil {
		t.Fatal(err)
	}
	_, h := NewToken()
	if err = a.commitSecurity(ctx, b, s, false, &h); !errors.Is(err, store.ErrAdminChanged) {
		t.Fatal("stale security state issued session")
	}
	for _, kind := range []string{"session", "purpose", "generation", "expiration", "replay"} {
		t.Run(kind, func(t *testing.T) {
			id, err := a.putChallenge(securityChallenge{Kind: "proof", Session: "session", Generation: 7})
			if err != nil {
				t.Fatal(err)
			}
			purpose, session, generation := "proof", "session", int64(7)
			switch kind {
			case "session":
				session = "other"
			case "purpose":
				purpose = "login"
			case "generation":
				generation++
			case "expiration":
				clk.Advance(5 * time.Minute)
			case "replay":
				if _, err = a.takeChallenge(id, purpose, session, generation); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = a.takeChallenge(id, purpose, session, generation); !errors.Is(err, ErrSecurity) {
				t.Fatal("challenge binding failed")
			}
		})
	}
	session, err := a.Login(ctx, goodPassword, from)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := a.SecurityAction(ctx, SecurityInput{Action: "totp_begin", Password: goodPassword}, session, from)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Logout(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SecurityAction(ctx, SecurityInput{Action: "totp_enable", ChallengeID: begin.ChallengeID, OTP: totpCode(begin.TOTPSecret, clk.Now().Unix()/30)}, session, from); !errors.Is(err, store.ErrAdminChanged) {
		t.Fatal("revoked session completed enrollment")
	}
}

func TestWebAuthnOrigins(t *testing.T) {
	a, _, _ := setup(t)
	for _, origin := range []string{"http://example.com", "https://admin.example/path", "https://user:pass@admin.example", "https://admin.example?x=1"} {
		if err := a.ConfigureWebAuthn(origin, ""); err == nil {
			t.Fatalf("bad origin accepted: %s", origin)
		}
	}
	if err := a.ConfigureWebAuthn("https://admin.example", "https://admin.example:9443"); err == nil {
		t.Fatal("theme host accepted as RP")
	}
	if err := a.ConfigureWebAuthn("https://ADMIN.example.", "https://admin.example:9443"); err == nil {
		t.Fatal("canonical theme host bypassed RP separation")
	}
	if err := a.ConfigureWebAuthn("http://localhost:8080", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPasskeyChallengeAndVerificationAdmission(t *testing.T) {
	a, st, _ := setup(t)
	ctx := context.Background()
	first := netip.MustParseAddr("2001:db8:1::1")
	second := netip.MustParseAddr("2001:db8:2::1")
	if err := st.SetAdminPassword(ctx, cheapPHC(goodPassword)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfigureWebAuthn("https://admin.example", ""); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := a.BeginPasskeyLogin(ctx, first); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.BeginPasskeyLogin(ctx, netip.MustParseAddr("2001:db8:1::2")); !errors.Is(err, ErrLoginBusy) {
		t.Fatal("same source filled challenge pool")
	}
	if _, err := a.BeginPasskeyLogin(ctx, second); err != nil {
		t.Fatal("one source denied independent source")
	}
	a.loginGate.Lock()
	_, err := a.FinishPasskeyLogin(ctx, "unknown", "{}", second)
	a.loginGate.Unlock()
	if !errors.Is(err, ErrLoginBusy) {
		t.Fatal("Passkey verification bypassed shared gate")
	}
	for range failLimit {
		if _, err := a.FinishPasskeyLogin(ctx, "unknown", "{}", second); !errors.Is(err, ErrSecurity) {
			t.Fatal(err)
		}
	}
	if _, err := a.BeginPasskeyLogin(ctx, second); !errors.Is(err, ErrLocked) {
		t.Fatal("Passkey failures did not lock beginning")
	}
	if _, err := a.Login(ctx, goodPassword, second); !errors.Is(err, ErrLocked) {
		t.Fatal("Passkey failures did not lock password login")
	}
}

func challengeValue(t *testing.T, options string) string {
	t.Helper()
	var v struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(options), &v); err != nil {
		t.Fatal(err)
	}
	return v.PublicKey.Challenge
}

func softwareCredential(t *testing.T, key *ecdsa.PrivateKey, id, userID []byte, challenge, origin string, register bool, count uint32, overrideFlags ...byte) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	typ := "webauthn.get"
	if register {
		typ = "webauthn.create"
	}
	client, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	rp := sha256.Sum256([]byte("admin.example"))
	data := append([]byte{}, rp[:]...)
	flags := byte(5)
	if register {
		flags |= 64
	}
	if len(overrideFlags) > 0 {
		flags = overrideFlags[0]
	}
	data = append(data, flags)
	data = binary.BigEndian.AppendUint32(data, count)
	response := map[string]any{"clientDataJSON": enc(client)}
	if register {
		cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, make([]byte, 16)...)
		data = binary.BigEndian.AppendUint16(data, uint16(len(id)))
		data = append(data, id...)
		data = append(data, cose...)
		att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": data})
		if err != nil {
			t.Fatal(err)
		}
		response["attestationObject"] = enc(att)
	} else {
		clientHash := sha256.Sum256(client)
		signed := append(append([]byte{}, data...), clientHash[:]...)
		hash := sha256.Sum256(signed)
		sig, err := ecdsa.SignASN1(rand.Reader, key, hash[:])
		if err != nil {
			t.Fatal(err)
		}
		response["authenticatorData"] = enc(data)
		response["signature"] = enc(sig)
		response["userHandle"] = enc(userID)
	}
	raw, _ := json.Marshal(map[string]any{"id": enc(id), "rawId": enc(id), "type": "public-key", "response": response, "clientExtensionResults": map[string]any{}})
	return string(raw)
}

func TestPasskeyRegistrationLoginOriginProofAndDeletion(t *testing.T) {
	a, st, _ := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("192.0.2.1")
	if err := st.SetAdminPassword(ctx, cheapPHC(goodPassword)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfigureWebAuthn("https://admin.example", ""); err != nil {
		t.Fatal(err)
	}
	session, err := a.Login(ctx, goodPassword, from)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := a.SecurityAction(ctx, SecurityInput{Action: "passkey_begin", Password: goodPassword}, session, from)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := []byte("credential-identifier-for-test")
	raw := softwareCredential(t, key, id, nil, challengeValue(t, begin.OptionsJSON), "https://admin.example", true, 0)
	if _, err = a.SecurityAction(ctx, SecurityInput{Action: "passkey_register", ChallengeID: begin.ChallengeID, CredentialJSON: raw, Name: "测试密钥"}, session, from); err != nil {
		t.Fatal(err)
	}
	_, state, err := a.readSecurity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	login, err := a.BeginPasskeyLogin(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	raw = softwareCredential(t, key, id, state.UserID, challengeValue(t, login.OptionsJSON), "https://theme.example", false, 1)
	if _, err = a.FinishPasskeyLogin(ctx, login.ChallengeID, raw, from); !errors.Is(err, ErrSecurity) {
		t.Fatal("foreign origin assertion accepted")
	}
	login, err = a.BeginPasskeyLogin(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	raw = softwareCredential(t, key, id, state.UserID, challengeValue(t, login.OptionsJSON), "https://admin.example", false, 1, 1)
	if _, err = a.FinishPasskeyLogin(ctx, login.ChallengeID, raw, from); !errors.Is(err, ErrSecurity) {
		t.Fatal("assertion without user verification accepted")
	}
	login, err = a.BeginPasskeyLogin(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	raw = softwareCredential(t, key, id, state.UserID, challengeValue(t, login.OptionsJSON), "https://admin.example", false, 1)
	session, err = a.FinishPasskeyLogin(ctx, login.ChallengeID, raw, from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.FinishPasskeyLogin(ctx, login.ChallengeID, raw, from); !errors.Is(err, ErrSecurity) {
		t.Fatal("passkey assertion replay accepted")
	}
	proof, err := a.SecurityAction(ctx, SecurityInput{Action: "reauth_begin"}, session, from)
	if err != nil {
		t.Fatal(err)
	}
	raw = softwareCredential(t, key, id, state.UserID, challengeValue(t, proof.OptionsJSON), "https://admin.example", false, 2)
	proof, err = a.SecurityAction(ctx, SecurityInput{Action: "reauth_finish", ChallengeID: proof.ChallengeID, CredentialJSON: raw}, session, from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SecurityAction(ctx, SecurityInput{Action: "passkey_delete", CredentialID: base64.RawURLEncoding.EncodeToString(id), ProofToken: proof.ProofToken}, session, from); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.AuthenticateSession(ctx, []string{session}); ok {
		t.Fatal("credential removal retained session")
	}
	_, state, err = a.readSecurity(ctx)
	if err != nil || len(state.Passkeys) != 0 {
		t.Fatal("credential removal did not persist")
	}
}
