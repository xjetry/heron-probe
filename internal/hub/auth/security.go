package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/xjetry/probe/internal/hub/store"
	"golang.org/x/net/idna"
)

var ErrSecurity = errors.New("authentication proof invalid or expired")

type Passkey struct {
	Name       string              `json:"name"`
	Credential webauthn.Credential `json:"credential"`
}
type securityState struct {
	Secret   string    `json:"secret,omitempty"`
	LastStep int64     `json:"last_step,omitempty"`
	Recovery []string  `json:"recovery,omitempty"`
	UserID   []byte    `json:"user_id,omitempty"`
	Passkeys []Passkey `json:"passkeys,omitempty"`
}

func (s securityState) WebAuthnID() []byte          { return s.UserID }
func (s securityState) WebAuthnName() string        { return "admin" }
func (s securityState) WebAuthnDisplayName() string { return "probe 管理员" }
func (s securityState) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(s.Passkeys))
	for _, p := range s.Passkeys {
		out = append(out, p.Credential)
	}
	return out
}

type securityChallenge struct {
	Kind, Session, Secret string
	Generation            int64
	Expires               time.Time
	Data                  *webauthn.SessionData
}
type securityRuntime struct {
	mu      sync.Mutex
	web     *webauthn.WebAuthn
	pending map[string]securityChallenge
}
type SecurityInfo struct {
	TOTPEnabled, PasskeyAvailable bool
	Passkeys                      []Passkey
	RecoveryRemaining             int
}
type SecurityActionKind string

const (
	SecurityTOTPBegin          SecurityActionKind = "totp_begin"
	SecurityTOTPEnable         SecurityActionKind = "totp_enable"
	SecurityTOTPDisable        SecurityActionKind = "totp_disable"
	SecurityPasskeyBegin       SecurityActionKind = "passkey_begin"
	SecurityPasskeyRegister    SecurityActionKind = "passkey_register"
	SecurityPasskeyDelete      SecurityActionKind = "passkey_delete"
	SecurityReauthBegin        SecurityActionKind = "reauth_begin"
	SecurityReauthFinish       SecurityActionKind = "reauth_finish"
	SecurityRecoveryRegenerate SecurityActionKind = "recovery_regenerate"
)

type SecurityInput struct {
	Action                                                                                   SecurityActionKind
	Password, OTP, RecoveryCode, ChallengeID, CredentialJSON, Name, CredentialID, ProofToken string
}
type SecurityResult struct {
	RevokeSession                                             bool
	ChallengeID, OptionsJSON, TOTPSecret, TOTPURI, ProofToken string
	RecoveryCodes                                             []string
}

// ConfigureWebAuthn 只在启动时调用。RP 来源由部署者指定，不信任请求 Host 或转发头。
func (a *Auth) ConfigureWebAuthn(origin, themeOrigin string) error {
	if origin == "" {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid admin origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return errors.New("admin origin requires HTTPS except localhost")
	}
	host, err := securityHostname(u.Hostname())
	if err != nil {
		return errors.New("invalid admin hostname")
	}
	if host == "" {
		return errors.New("invalid admin hostname")
	}
	if t, e := url.Parse(themeOrigin); e == nil && t.Hostname() != "" {
		themeHost, err := securityHostname(t.Hostname())
		if err != nil {
			return errors.New("invalid theme hostname")
		}
		if themeHost == host {
			return errors.New("admin and theme must use different hostnames")
		}
	}
	port := u.Port()
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	w, err := webauthn.New(&webauthn.Config{RPDisplayName: "probe", RPID: host, RPOrigins: []string{u.Scheme + "://" + u.Host}, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}})
	if err != nil {
		return err
	}
	a.security.web = w
	return nil
}

func securityHostname(host string) (string, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String(), nil
	}
	return idna.Lookup.ToASCII(host)
}
func (a *Auth) readSecurity(ctx context.Context) (store.AdminSecurity, securityState, error) {
	b, err := a.store.AdminSecurity(ctx)
	var s securityState
	if err == nil {
		err = json.Unmarshal([]byte(b.Data), &s)
	}
	return b, s, err
}
func (a *Auth) commitSecurity(ctx context.Context, b store.AdminSecurity, s securityState, revoke bool, session *[32]byte) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	now := a.clk.Now()
	if revoke && b.Event == nil {
		b.Event = &store.AlertEvent{Transition: store.TransitionAuthChanged, At: now, Summary: "管理员认证方式变更，时间 " + now.In(a.loc).Format(time.RFC3339)}
	}
	err = a.store.CommitAdminSecurity(ctx, b, string(data), revoke, session, now, now.Add(SessionAbsolute))
	if err == nil && b.Event != nil && a.loginSender != nil {
		a.loginSender.Enqueue(*b.Event)
	}
	return err
}
func (a *Auth) SecurityInfo(ctx context.Context) (SecurityInfo, error) {
	_, s, err := a.readSecurity(ctx)
	return SecurityInfo{s.Secret != "", a.security.web != nil, s.Passkeys, len(s.Recovery)}, err
}
func totpCode(secret string, step int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return ""
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(step))
	h := hmac.New(sha1.New, key)
	h.Write(b[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1000000)
}
func consumeFactor(s *securityState, otp, recovery string, now time.Time) bool {
	if s.Secret == "" {
		return otp == "" && recovery == ""
	}
	if otp != "" && recovery != "" {
		return false
	}
	if recovery != "" {
		h := HashToken(recovery)
		encoded := hex.EncodeToString(h[:])
		for i, v := range s.Recovery {
			if subtle.ConstantTimeCompare([]byte(v), []byte(encoded)) == 1 {
				s.Recovery = append(s.Recovery[:i], s.Recovery[i+1:]...)
				return true
			}
		}
		return false
	}
	if len(otp) != 6 {
		return false
	}
	step := now.Unix() / 30
	for _, n := range []int64{step, step - 1, step + 1} {
		if n > s.LastStep && subtle.ConstantTimeCompare([]byte(otp), []byte(totpCode(s.Secret, n))) == 1 {
			s.LastStep = n
			return true
		}
	}
	return false
}
func newRecovery(s *securityState) []string {
	s.Recovery = nil
	out := make([]string, 10)
	for i := range out {
		var b [20]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		out[i] = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
		h := HashToken(out[i])
		s.Recovery = append(s.Recovery, hex.EncodeToString(h[:]))
	}
	return out
}
func (a *Auth) securityFailure(ctx context.Context, from netip.Addr) {
	a.mu.Lock()
	_, locked := a.login.record(from, a.clk.Mono())
	a.mu.Unlock()
	a.notifySecurityFailure(ctx, from, locked)
}
func (a *Auth) notifySecurityFailure(ctx context.Context, from netip.Addr, locked bool) {
	a.notifyLogin(ctx, store.TransitionLoginFailed, "管理员认证失败", from, a.clk.Now())
	if locked {
		a.notifyLogin(ctx, store.TransitionLoginLocked, "登录失败达到锁定阈值", from, a.clk.Now())
	}
}
func (a *Auth) checkLoginSource(from netip.Addr) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.login.locked(from, a.clk.Mono()) {
		return ErrLocked
	}
	return nil
}
func (a *Auth) LoginFactors(ctx context.Context, password, otp, recovery string, from netip.Addr) (string, error) {
	a.mu.Lock()
	failureSequence := a.login.sequence
	a.mu.Unlock()
	phc, locked, err := a.verifyLoginPassword(ctx, password, from)
	if locked {
		a.notifyLogin(ctx, store.TransitionLoginLocked, "登录失败达到锁定阈值", from, a.clk.Now())
	}
	if err != nil {
		if errors.Is(err, ErrBadPassword) || errors.Is(err, ErrNoAdmin) {
			a.notifyLogin(ctx, store.TransitionLoginFailed, "管理员认证失败", from, a.clk.Now())
		}
		return "", err
	}
	b, s, err := a.readSecurity(ctx)
	if err != nil {
		return "", err
	}
	if phc != b.PasswordHash || !consumeFactor(&s, otp, recovery, a.clk.Now()) {
		a.securityFailure(ctx, from)
		return "", ErrSecurity
	}
	plain, h := NewToken()
	if err = a.commitSecurity(ctx, b, s, false, &h); err != nil {
		if errors.Is(err, store.ErrAdminChanged) {
			return "", ErrBadPassword
		}
		return "", err
	}
	a.mu.Lock()
	a.login.clearThrough(from, failureSequence)
	a.mu.Unlock()
	a.notifyLogin(ctx, store.TransitionLoginSuccess, "管理员登录成功（密码）", from, a.clk.Now())
	if _, err = a.store.DeleteExpiredSessions(ctx, a.clk.Now()); err != nil {
		a.log.Warn("purging expired sessions failed", "err", err)
	}
	return plain, nil
}
func (a *Auth) putChallenge(c securityChallenge) (string, error) {
	a.security.mu.Lock()
	defer a.security.mu.Unlock()
	if a.security.pending == nil {
		a.security.pending = map[string]securityChallenge{}
	}
	for k, v := range a.security.pending {
		if !a.clk.Now().Before(v.Expires) {
			delete(a.security.pending, k)
		}
	}
	if len(a.security.pending) >= 128 {
		return "", ErrLoginBusy
	}
	if c.Kind == "login" {
		count := 0
		for _, v := range a.security.pending {
			if v.Kind == "login" && v.Session == c.Session {
				count++
			}
		}
		if count >= 3 {
			return "", ErrLoginBusy
		}
	}
	id, _ := NewToken()
	c.Expires = a.clk.Now().Add(5 * time.Minute)
	a.security.pending[id] = c
	return id, nil
}
func (a *Auth) takeChallenge(id, kind, session string, generation int64) (securityChallenge, error) {
	a.security.mu.Lock()
	defer a.security.mu.Unlock()
	c, ok := a.security.pending[id]
	delete(a.security.pending, id)
	if !ok || c.Kind != kind || c.Session != session || c.Generation != generation || !a.clk.Now().Before(c.Expires) {
		return c, ErrSecurity
	}
	return c, nil
}
func (a *Auth) BeginPasskeyLogin(ctx context.Context, from netip.Addr) (SecurityResult, error) {
	if err := a.checkLoginSource(from); err != nil {
		return SecurityResult{}, err
	}
	if a.security.web == nil {
		return SecurityResult{}, ErrSecurity
	}
	b, _, err := a.readSecurity(ctx)
	if err != nil {
		return SecurityResult{}, err
	}
	options, data, err := a.security.web.BeginDiscoverableLogin()
	if err != nil {
		return SecurityResult{}, err
	}
	id, err := a.putChallenge(securityChallenge{Kind: "login", Session: SourceKey(from).String(), Generation: b.Generation, Data: data})
	raw, _ := json.Marshal(options)
	return SecurityResult{ChallengeID: id, OptionsJSON: string(raw)}, err
}
func (a *Auth) verifyAssertion(s *securityState, c securityChallenge, raw string) error {
	r, err := http.NewRequest(http.MethodPost, "https://localhost", strings.NewReader(raw))
	if err != nil {
		return err
	}
	credential, err := a.security.web.FinishDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		if len(s.UserID) == 0 || !bytes.Equal(userHandle, s.UserID) {
			return nil, ErrSecurity
		}
		return *s, nil
	}, *c.Data, r)
	if err != nil {
		return ErrSecurity
	}
	if credential.Authenticator.CloneWarning {
		return ErrSecurity
	}
	for i := range s.Passkeys {
		if bytes.Equal(s.Passkeys[i].Credential.ID, credential.ID) {
			s.Passkeys[i].Credential = *credential
			return nil
		}
	}
	return ErrSecurity
}
func (a *Auth) FinishPasskeyLogin(ctx context.Context, id, raw string, from netip.Addr) (string, error) {
	a.mu.Lock()
	if a.login.locked(from, a.clk.Mono()) {
		a.mu.Unlock()
		return "", ErrLocked
	}
	if !a.loginGate.TryLock() {
		a.mu.Unlock()
		return "", ErrLoginBusy
	}
	failureSequence := a.login.sequence
	a.mu.Unlock()
	gateHeld := true
	defer func() {
		if gateHeld {
			a.loginGate.Unlock()
		}
	}()
	if a.security.web == nil {
		return "", ErrSecurity
	}
	b, s, err := a.readSecurity(ctx)
	if err != nil {
		return "", err
	}
	c, err := a.takeChallenge(id, "login", SourceKey(from).String(), b.Generation)
	if err == nil {
		err = a.verifyAssertion(&s, c, raw)
	}
	if err != nil {
		a.mu.Lock()
		_, locked := a.login.record(from, a.clk.Mono())
		a.mu.Unlock()
		a.loginGate.Unlock()
		gateHeld = false
		a.notifySecurityFailure(ctx, from, locked)
		return "", ErrSecurity
	}
	plain, h := NewToken()
	if err = a.commitSecurity(ctx, b, s, false, &h); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.login.clearThrough(from, failureSequence)
	a.mu.Unlock()
	a.loginGate.Unlock()
	gateHeld = false
	a.notifyLogin(ctx, store.TransitionLoginSuccess, "管理员登录成功（Passkey）", from, a.clk.Now())
	return plain, nil
}
func (a *Auth) proveSecurity(ctx context.Context, b store.AdminSecurity, s *securityState, in SecurityInput, session string, from netip.Addr) error {
	if err := a.checkLoginSource(from); err != nil {
		return err
	}
	if in.ProofToken != "" {
		_, err := a.takeChallenge(in.ProofToken, "proof", session, b.Generation)
		return err
	}
	phc, locked, err := a.verifyLoginPassword(ctx, in.Password, from)
	if locked {
		a.notifyLogin(ctx, store.TransitionLoginLocked, "登录失败达到锁定阈值", from, a.clk.Now())
	}
	if err != nil {
		if errors.Is(err, ErrBadPassword) || errors.Is(err, ErrNoAdmin) {
			a.notifyLogin(ctx, store.TransitionLoginFailed, "管理员重新认证失败", from, a.clk.Now())
		}
		return err
	}
	if phc != b.PasswordHash || !consumeFactor(s, in.OTP, in.RecoveryCode, a.clk.Now()) {
		a.securityFailure(ctx, from)
		return ErrSecurity
	}
	return nil
}
func (a *Auth) SecurityAction(ctx context.Context, in SecurityInput, session string, from netip.Addr) (SecurityResult, error) {
	var out SecurityResult
	b, s, err := a.readSecurity(ctx)
	if err != nil {
		return out, err
	}
	h := HashToken(session)
	b.SessionHash = &h
	if in.Action == SecurityReauthBegin {
		if a.security.web == nil {
			return out, ErrSecurity
		}
		if err = a.checkLoginSource(from); err != nil {
			return out, err
		}
		options, data, e := a.security.web.BeginDiscoverableLogin()
		if e != nil {
			return out, e
		}
		out.ChallengeID, err = a.putChallenge(securityChallenge{Kind: "reauth", Session: session, Generation: b.Generation, Data: data})
		raw, _ := json.Marshal(options)
		out.OptionsJSON = string(raw)
		return out, err
	}
	if in.Action == SecurityReauthFinish {
		if a.security.web == nil {
			return out, ErrSecurity
		}
		if err = a.checkLoginSource(from); err != nil {
			return out, err
		}
		c, e := a.takeChallenge(in.ChallengeID, "reauth", session, b.Generation)
		if e == nil {
			e = a.verifyAssertion(&s, c, in.CredentialJSON)
		}
		if e != nil {
			a.securityFailure(ctx, from)
			return out, ErrSecurity
		}
		if err = a.commitSecurity(ctx, b, s, false, nil); err != nil {
			return out, err
		}
		out.ProofToken, err = a.putChallenge(securityChallenge{Kind: "proof", Session: session, Generation: b.Generation + 1})
		return out, err
	}
	if in.Action == SecurityTOTPEnable || in.Action == SecurityPasskeyRegister {
		kind := "totp"
		if in.Action == SecurityPasskeyRegister {
			kind = "register"
		}
		c, e := a.takeChallenge(in.ChallengeID, kind, session, b.Generation)
		if e != nil {
			return out, e
		}
		if kind == "totp" {
			s.Secret = c.Secret
			s.LastStep = 0
			if !consumeFactor(&s, in.OTP, "", a.clk.Now()) {
				a.securityFailure(ctx, from)
				return out, ErrSecurity
			}
			out.RecoveryCodes = newRecovery(&s)
		} else {
			if a.security.web == nil || len(in.Name) > 128 || strings.TrimSpace(in.Name) == "" {
				return out, ErrSecurity
			}
			r, _ := http.NewRequest(http.MethodPost, "https://localhost", strings.NewReader(in.CredentialJSON))
			credential, e := a.security.web.FinishRegistration(s, *c.Data, r)
			if e != nil {
				return out, ErrSecurity
			}
			for _, p := range s.Passkeys {
				if bytes.Equal(p.Credential.ID, credential.ID) {
					return out, ErrSecurity
				}
			}
			s.Passkeys = append(s.Passkeys, Passkey{Name: strings.TrimSpace(in.Name), Credential: *credential})
		}
	} else {
		if err = a.proveSecurity(ctx, b, &s, in, session, from); err != nil {
			return out, err
		}
		switch in.Action {
		case SecurityTOTPBegin:
			if s.Secret != "" {
				return out, ErrSecurity
			}
			var secret [20]byte
			if _, err = rand.Read(secret[:]); err != nil {
				return out, err
			}
			out.TOTPSecret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret[:])
			out.TOTPURI = "otpauth://totp/probe:admin?issuer=probe&algorithm=SHA1&digits=6&period=30&secret=" + out.TOTPSecret
			if err = a.commitSecurity(ctx, b, s, false, nil); err != nil {
				return out, err
			}
			out.ChallengeID, err = a.putChallenge(securityChallenge{Kind: "totp", Session: session, Generation: b.Generation + 1, Secret: out.TOTPSecret})
			return out, err
		case SecurityPasskeyBegin:
			if a.security.web == nil || len(s.Passkeys) >= 20 {
				return out, ErrSecurity
			}
			if len(s.UserID) == 0 {
				s.UserID = make([]byte, 32)
				if _, err = rand.Read(s.UserID); err != nil {
					return out, err
				}
			}
			options, data, e := a.security.web.BeginRegistration(s)
			if e != nil {
				return out, e
			}
			if err = a.commitSecurity(ctx, b, s, false, nil); err != nil {
				return out, err
			}
			out.ChallengeID, err = a.putChallenge(securityChallenge{Kind: "register", Session: session, Generation: b.Generation + 1, Data: data})
			raw, _ := json.Marshal(options)
			out.OptionsJSON = string(raw)
			return out, err
		case SecurityTOTPDisable:
			if s.Secret == "" {
				return out, ErrSecurity
			}
			s.Secret = ""
			s.Recovery = nil
			s.LastStep = 0
		case SecurityRecoveryRegenerate:
			if s.Secret == "" {
				return out, ErrSecurity
			}
			out.RecoveryCodes = newRecovery(&s)
		case SecurityPasskeyDelete:
			found := false
			for i, p := range s.Passkeys {
				if base64.RawURLEncoding.EncodeToString(p.Credential.ID) == in.CredentialID {
					s.Passkeys = append(s.Passkeys[:i], s.Passkeys[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				return out, ErrSecurity
			}
		default:
			return out, ErrSecurity
		}
	}
	if err = a.commitSecurity(ctx, b, s, true, nil); err != nil {
		return SecurityResult{}, err
	}
	out.RevokeSession = true
	return out, nil
}

func (a *Auth) ResetSecurity(ctx context.Context) error { return a.store.ResetAdminSecurity(ctx) }
