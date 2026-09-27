// 国家码到区域指示符旗帜：A–Z 依次对应 U+1F1E6–U+1F1FF，两个连写即旗帜，不引入图片资源。hub 只下发两个大写字母
// （查询应答与手动指定同用 geo.IsCountryCode 判定），其它形状返回空串，不拼出半个旗帜。
export function flag(code: string): string {
  if (!/^[A-Z]{2}$/.test(code)) return "";
  return String.fromCodePoint(...[...code].map((c) => 0x1f1e6 + c.charCodeAt(0) - 0x41));
}
