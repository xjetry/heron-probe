import { type ReactNode, useState } from "react";
import { CopyableText } from "./CopyableText";
import { needsInsecureHTTP } from "../lib/transport";
import { isRelease } from "../lib/version";
import { loadProxyPort, parseProxyPort, saveProxyPort, sshProxyArgs } from "../lib/installProxy";

// 安装脚本只装自己所属的版本（spec §5.7），版本由取哪个 URL 的脚本决定：hub 为正式版本（带 v 前缀的合法
// semver，与节点落后判定同一个解析）时取同版本 release 的脚本，装上的 agent 与 hub 同版本；否则只能取最新 release。
const scriptUrl = (hubVersion: string) =>
  isRelease(hubVersion)
    ? `https://github.com/xjetry/heron-probe/releases/download/${hubVersion}/install.sh`
    : "https://github.com/xjetry/heron-probe/releases/latest/download/install.sh";

// origin 是 agent 访问 hub 的地址，也是判定要不要 --insecure-http 的依据：命令里的 --hub 与这个判定取同一个值。
// registerKey 是注册窗口的 key 或指定节点的安装凭据；注册后由 agent 保存另行签发的运行 token。
// "国内主机"只改变这里生成的命令（SSH 反代参数与 --update-source hub），hub 不记录：节点实际从哪取更新由
// 更新器上报，再存一份会出现两个可能不一致的来源。每次打开默认关闭：能直连 GitHub 的主机不需要 SSH 反代，
// 经 hub 中转也只是多占 hub 的出口带宽。
export function InstallCommands({ hubVersion, origin, registerKey, reRegister = false, banner }: { hubVersion: string; origin: string; registerKey: string; reRegister?: boolean; banner?: ReactNode }) {
  const [domestic, setDomestic] = useState(false);
  const [portText, setPortText] = useState(loadProxyPort);
  const port = parseProxyPort(portText);
  const url = scriptUrl(hubVersion);
  const insecure = needsInsecureHTTP(origin);
  const args = `--hub ${origin} --key ${registerKey}${insecure ? " --insecure-http" : ""}${reRegister ? " --re-register" : ""}${domestic ? " --update-source hub" : ""}`;
  const editPort = (text: string) => {
    setPortText(text);
    const parsed = parseProxyPort(text);
    if (parsed !== null) saveProxyPort(parsed);
  };
  return (
    <>
      {banner}
      <label className="inline"><input type="checkbox" checked={domestic} onChange={(e) => setDomestic(e.target.checked)} />国内主机（连不上 GitHub 与 CDN：安装经 SSH 反代走本机代理，在线更新经 hub 中转）</label>
      {domestic && <div className="install-command">
        <strong>第一步：SSH 反代</strong>
        <p>在开着代理的电脑上以 root 登录目标主机：<code>ssh root@主机地址</code> 后面接上这段参数，然后在登录后的 shell 里执行第二步的安装命令。</p>
        <label className="inline">本机代理端口<input aria-label="本机代理端口" inputMode="numeric" value={portText} onChange={(e) => editPort(e.target.value)} /></label>
        {port === null
          ? <p role="alert" className="error">本机代理端口须为 1–65535 的整数</p>
          : <CopyableText label="SSH 反代参数" copyLabel="复制 SSH 反代参数" value={sshProxyArgs(port)} />}
        <p className="muted">参数把本机代理端口反向转发到目标主机的同一端口，并在登录后的 shell 里设好 http_proxy、https_proxy 与 all_proxy；本机代理端口要同时接受 HTTP 与 SOCKS5（如 Clash 的混合端口）。sudo 可能丢掉这些变量，请以 root 登录或用 sudo -E。代理只用于这次安装，装好后 agent 直接连 hub。</p>
      </div>}
      {domestic && <p><strong>第二步：在上面登录的 shell 里执行安装命令</strong></p>}
      <div className="install-command"><strong>curl</strong><CopyableText label="curl 安装命令" copyLabel="复制 curl 命令" value={`curl -fsSL ${url} | sh -s -- ${args}`} /></div>
      <div className="install-command"><strong>wget</strong><CopyableText label="wget 安装命令" copyLabel="复制 wget 命令" value={`wget -qO- ${url} | sh -s -- ${args}`} /></div>
      {domestic && <p className="muted">命令带 --update-source hub：在线更新经 hub 中转取官方签名产物，不直连 GitHub。OpenRC 主机（如 Alpine）不支持在线更新，安装脚本会拒绝这个参数，请删掉它再执行。</p>}
      {!isRelease(hubVersion) && <p className="muted">hub 不是正式版本（{hubVersion || "未知"}），脚本取自最新 release，将安装最新 release。</p>}
      {insecure && <p className="muted">hub 地址是 http 且不是 loopback IP，命令带 --insecure-http：节点 token 与指标将明文传输。</p>}
      <p className="muted">安装命令的可信来源是 README 与 GitHub Release：这里的命令由 hub 提供，hub 失守时不可信。</p>
    </>
  );
}
