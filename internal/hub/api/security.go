package api

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/store"
)

func securityError(err error) error {
	if errors.Is(err, auth.ErrLoginBusy) {
		return connect.NewError(connect.CodeResourceExhausted, err)
	}
	if errors.Is(err, auth.ErrSecurity) || errors.Is(err, auth.ErrBadPassword) || errors.Is(err, auth.ErrNoAdmin) || errors.Is(err, auth.ErrLocked) || errors.Is(err, store.ErrAdminChanged) {
		return unauthenticated("认证失败、已过期或认证配置已改变，请重新证明身份")
	}
	return internalError("认证操作失败")
}
func securityResponse(r auth.SecurityResult) *probev1.SecurityActionResponse {
	return &probev1.SecurityActionResponse{ChallengeId: r.ChallengeID, OptionsJson: r.OptionsJSON, TotpSecret: r.TOTPSecret, TotpUri: r.TOTPURI, RecoveryCodes: r.RecoveryCodes, ProofToken: r.ProofToken}
}
func (s *Service) GetSecurity(ctx context.Context, _ *connect.Request[probev1.GetSecurityRequest]) (*connect.Response[probev1.GetSecurityResponse], error) {
	info, err := s.auth.SecurityInfo(ctx)
	if err != nil {
		return nil, securityError(err)
	}
	out := &probev1.GetSecurityResponse{TotpEnabled: info.TOTPEnabled, PasskeyAvailable: info.PasskeyAvailable, RecoveryCodesRemaining: uint32(info.RecoveryRemaining)}
	for _, p := range info.Passkeys {
		out.Passkeys = append(out.Passkeys, &probev1.SecurityCredential{Id: base64.RawURLEncoding.EncodeToString(p.Credential.ID), Name: p.Name})
	}
	return connect.NewResponse(out), nil
}
func (s *Service) BeginPasskeyLogin(ctx context.Context, _ *connect.Request[probev1.BeginPasskeyLoginRequest]) (*connect.Response[probev1.BeginPasskeyLoginResponse], error) {
	r, err := s.auth.BeginPasskeyLogin(ctx, ctx.Value(peerKey{}).(peerInfo).from)
	if err != nil {
		return nil, securityError(err)
	}
	return connect.NewResponse(&probev1.BeginPasskeyLoginResponse{ChallengeId: r.ChallengeID, OptionsJson: r.OptionsJSON}), nil
}
func (s *Service) FinishPasskeyLogin(ctx context.Context, req *connect.Request[probev1.FinishPasskeyLoginRequest]) (*connect.Response[probev1.FinishPasskeyLoginResponse], error) {
	if len(req.Msg.CredentialJson) > 65536 || len(req.Msg.ChallengeId) > 128 {
		return nil, invalid("Passkey 响应过大")
	}
	peer := ctx.Value(peerKey{}).(peerInfo)
	token, err := s.auth.FinishPasskeyLogin(ctx, req.Msg.ChallengeId, req.Msg.CredentialJson, peer.from)
	if err != nil {
		return nil, securityError(err)
	}
	resp := connect.NewResponse(&probev1.FinishPasskeyLoginResponse{})
	resp.Header().Add("Set-Cookie", sessionCookie(token, peer.scheme == "https", int(auth.SessionAbsolute/time.Second)).String())
	return resp, nil
}
func (s *Service) SecurityAction(ctx context.Context, req *connect.Request[probev1.SecurityActionRequest]) (*connect.Response[probev1.SecurityActionResponse], error) {
	in := req.Msg
	if len(in.Password) > 4096 || len(in.Otp) > 16 || len(in.RecoveryCode) > 128 || len(in.CredentialJson) > 65536 || len(in.Name) > 128 || len(in.CredentialId) > 2048 || len(in.ChallengeId) > 128 || len(in.ProofToken) > 128 {
		return nil, invalid("认证字段过大")
	}
	actions := map[probev1.SecurityActionKind]auth.SecurityActionKind{
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_TOTP_BEGIN:          auth.SecurityTOTPBegin,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_TOTP_ENABLE:         auth.SecurityTOTPEnable,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_TOTP_DISABLE:        auth.SecurityTOTPDisable,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_PASSKEY_BEGIN:       auth.SecurityPasskeyBegin,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_PASSKEY_REGISTER:    auth.SecurityPasskeyRegister,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_PASSKEY_DELETE:      auth.SecurityPasskeyDelete,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_RECOVERY_REGENERATE: auth.SecurityRecoveryRegenerate,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_REAUTH_BEGIN:        auth.SecurityReauthBegin,
		probev1.SecurityActionKind_SECURITY_ACTION_KIND_REAUTH_FINISH:       auth.SecurityReauthFinish,
	}
	action, ok := actions[in.Action]
	if !ok {
		return nil, invalid("未知认证操作")
	}
	r, err := s.auth.SecurityAction(ctx, auth.SecurityInput{Action: action, Password: in.Password, OTP: in.Otp, RecoveryCode: in.RecoveryCode, ChallengeID: in.ChallengeId, CredentialJSON: in.CredentialJson, Name: in.Name, CredentialID: in.CredentialId, ProofToken: in.ProofToken}, ctx.Value(sessionKey{}).(string), ctx.Value(peerKey{}).(peerInfo).from)
	if err != nil {
		return nil, securityError(err)
	}
	resp := connect.NewResponse(securityResponse(r))
	if r.RevokeSession {
		clearSessionCookie(ctx, resp.Header())
	}
	return resp, nil
}
