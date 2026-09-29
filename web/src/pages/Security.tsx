import { createClient } from "@connectrpc/connect";
import { useQuery, useTransport } from "@connectrpc/connect-query";
import { useState } from "react";
import { Link } from "react-router";
import { AdminService, SecurityActionKind } from "../gen/probe/v1/admin_pb";
import { errorText } from "../api/auth";
import { passkeyCredential } from "../lib/passkey";

export function Security() {
  const client = createClient(AdminService, useTransport());
  const [changed, setChanged] = useState(false);
  const security = useQuery(AdminService.method.getSecurity, {}, { enabled: !changed });
  const [password, setPassword] = useState("");
  const [otp, setOTP] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [proofToken, setProofToken] = useState("");
  const [name, setName] = useState("");
  const [pending, setPending] = useState<{ id: string; secret: string; uri: string }>();
  const [setupOTP, setSetupOTP] = useState("");
  const [codes, setCodes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const run = async (action: () => Promise<void>) => {
    setBusy(true); setError("");
    try { await action(); } catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  };
  const proof = () => ({ password, otp, recoveryCode, proofToken });
  const complete = (recoveryCodes: string[] = []) => { setCodes(recoveryCodes); setChanged(true); setPassword(""); setOTP(""); setRecoveryCode(""); setProofToken(""); setPending(undefined); };
  const reauth = () => run(async () => {
    const begin = await client.securityAction({ action: SecurityActionKind.REAUTH_BEGIN });
    const credentialJson = await passkeyCredential(begin.optionsJson);
    const finish = await client.securityAction({ action: SecurityActionKind.REAUTH_FINISH, challengeId: begin.challengeId, credentialJson });
    setProofToken(finish.proofToken);
  });
  const beginTOTP = () => run(async () => {
    const result = await client.securityAction({ action: SecurityActionKind.TOTP_BEGIN, ...proof() });
    setProofToken(""); setOTP(""); setRecoveryCode("");
    setPending({ id: result.challengeId, secret: result.totpSecret, uri: result.totpUri });
  });
  const finishTOTP = () => run(async () => {
    const result = await client.securityAction({ action: SecurityActionKind.TOTP_ENABLE, challengeId: pending?.id, otp: setupOTP });
    complete(result.recoveryCodes);
  });
  const register = () => run(async () => {
    const begin = await client.securityAction({ action: SecurityActionKind.PASSKEY_BEGIN, ...proof() });
    setProofToken(""); setOTP(""); setRecoveryCode("");
    const credentialJson = await passkeyCredential(begin.optionsJson, true);
    await client.securityAction({ action: SecurityActionKind.PASSKEY_REGISTER, challengeId: begin.challengeId, credentialJson, name });
    complete();
  });
  const change = (action: SecurityActionKind, credentialId = "") => run(async () => {
    const result = await client.securityAction({ action, credentialId, ...proof() }); complete(result.recoveryCodes);
  });
  if (changed) return <section className="card"><h1>认证方式已更新</h1><p>全部旧会话已撤销。请先保存恢复码，再重新登录。</p>{codes.length > 0 && <><p>恢复码只显示一次，每个只能使用一次。请离线保管，不要放在同一台认证设备上。</p><pre>{codes.join("\n")}</pre><button onClick={() => {
    const url = URL.createObjectURL(new Blob([codes.join("\n") + "\n"], { type: "text/plain" }));
    const link = document.createElement("a"); link.href = url; link.download = "probe-recovery-codes.txt"; link.click(); URL.revokeObjectURL(url);
  }}>下载恢复码</button></>}<Link to="/login">重新登录</Link></section>;
  return <section><h1>账户安全</h1><p>Passkey 可无密码登录；启用 TOTP 后，密码登录必须同时提供动态验证码或一次性恢复码。任何认证器变更都会撤销全部会话。</p>
    {(error || security.error) && <p role="alert" className="error">{error || errorText(security.error)}</p>}
    <fieldset disabled={busy || security.isPending}><legend>重新证明身份</legend>
      <p>操作前输入密码与当前第二因素，或使用已有 Passkey。Passkey 证明五分钟内有效且仅能使用一次。</p>
      <label>管理员密码<input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} /></label>
      {security.data?.totpEnabled && <><label>当前动态验证码<input autoComplete="one-time-code" inputMode="numeric" value={otp} onChange={(e) => { setOTP(e.target.value); setRecoveryCode(""); }} /></label><label>或一次性恢复码<input autoComplete="off" value={recoveryCode} onChange={(e) => { setRecoveryCode(e.target.value); setOTP(""); }} /></label></>}
      {!!security.data?.passkeys.length && <button type="button" onClick={() => void reauth()}>使用 Passkey 重新认证</button>}
      {proofToken && <p role="status">Passkey 身份证明已准备。</p>}
    </fieldset>
    <fieldset disabled={busy || security.isPending}><legend>TOTP 与恢复码</legend>
      <p>{security.data?.totpEnabled ? `已启用，剩余 ${security.data.recoveryCodesRemaining} 个恢复码。` : "未启用。"}</p>
      {security.data?.totpEnabled ? <><button onClick={() => void change(SecurityActionKind.TOTP_DISABLE)}>关闭 TOTP</button><button onClick={() => void change(SecurityActionKind.RECOVERY_REGENERATE)}>作废旧恢复码并生成新码</button></> : <button onClick={() => void beginTOTP()}>设置 TOTP</button>}
      {pending && <div><p>将以下密钥手动添加到认证器（SHA-1、六位、30 秒）。设置会在五分钟后过期。</p><code>{pending.secret}</code><p><a href={pending.uri}>在认证器中打开</a></p><label>新认证器验证码<input autoComplete="one-time-code" inputMode="numeric" value={setupOTP} onChange={(e) => setSetupOTP(e.target.value)} /></label><button onClick={() => void finishTOTP()}>验证并启用</button></div>}
    </fieldset>
    <fieldset disabled={busy || security.isPending}><legend>Passkey</legend>
      {!security.data?.passkeyAvailable && <p>服务端尚未配置 HTTPS 管理来源（--admin-origin），无法注册或使用 Passkey。</p>}
      <label>认证器名称<input maxLength={128} value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：个人安全密钥" /></label>
      <button disabled={!security.data?.passkeyAvailable || !name.trim()} onClick={() => void register()}>添加 Passkey</button>
      <ul>{security.data?.passkeys.map((key) => <li key={key.id}>{key.name} <button onClick={() => void change(SecurityActionKind.PASSKEY_DELETE, key.id)}>删除</button></li>)}</ul>
    </fieldset><p>认证设备全部丢失时，需要 hub 主机访问权限；由运维者运行本机安全重置命令，然后重新注册认证器。</p>
  </section>;
}
