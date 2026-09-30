import { type ReactNode } from "react";
import { CopyableText } from "./CopyableText";
import { needsInsecureHTTP } from "../lib/transport";
import { isRelease } from "../lib/version";

// 安装脚本只装自己所属的版本（spec §5.7），版本由取哪个 URL 的脚本决定：hub 为正式版本（带 v 前缀的合法
// semver，与节点落后判定同一个解析）时取同版本 release 的脚本，装上的 agent 与 hub 同版本；否则只能取最新 release。
const scriptUrl = (hubVersion: string) =>
  isRelease(hubVersion)
    ? `https://github.com/xjetry/heron-probe/releases/download/${hubVersion}/install.sh`
    : "https://github.com/xjetry/heron-probe/releases/latest/download/install.sh";

// origin 是 agent 访问 hub 的地址，也是判定要不要 --insecure-http 的依据：命令里的 --hub 与这个判定取同一个值。
// registerKey 是注册窗口的一次性 key，或面板预创建的节点 token：两者都能被 `heron-agent register` 接受。
export function InstallCommands({ hubVersion, origin, registerKey, banner }: { hubVersion: string; origin: string; registerKey: string; banner?: ReactNode }) {
  const url = scriptUrl(hubVersion);
  const insecure = needsInsecureHTTP(origin);
  const args = `--hub ${origin} --key ${registerKey}${insecure ? " --insecure-http" : ""}`;
  return (
    <>
      {banner}
      <div className="install-command"><strong>curl</strong><CopyableText label="curl 安装命令" copyLabel="复制 curl 命令" value={`curl -fsSL ${url} | sh -s -- ${args}`} /></div>
      <div className="install-command"><strong>wget</strong><CopyableText label="wget 安装命令" copyLabel="复制 wget 命令" value={`wget -qO- ${url} | sh -s -- ${args}`} /></div>
      {!isRelease(hubVersion) && <p className="muted">hub 不是正式版本（{hubVersion || "未知"}），脚本取自最新 release，将安装最新 release。</p>}
      {insecure && <p className="muted">hub 地址是 http 且不是 loopback IP，命令带 --insecure-http：节点 token 与指标将明文传输。</p>}
      <p className="muted">安装命令的可信来源是 README 与 GitHub Release：这里的命令由 hub 提供，hub 失守时不可信。</p>
    </>
  );
}
