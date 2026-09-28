import { flag } from "../lib/country";

// 国家 / 地区徽章：旗帜加国家码。旗帜在一些系统上只显示成两个字母，国家码照写一遍，徽章在哪里都读得出来。
// 没有国家时不渲染。
export function CountryBadge({ code }: { code: string }) {
  if (code === "") return null;
  return (
    <span className="country-badge" title={`国家 / 地区 ${code}`}>
      <span aria-hidden="true">{flag(code)}</span> {code}
    </span>
  );
}
