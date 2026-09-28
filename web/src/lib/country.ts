// 国家码到区域指示符旗帜：A–Z 依次对应 U+1F1E6–U+1F1FF，两个连写即旗帜，不引入图片资源。hub 只下发两个大写字母
// （查询应答与手动指定同用 store.IsCountryCode 判定），其它形状返回空串，不拼出半个旗帜。
// hub 为每个节点记住答案的地址数（internal/hub/geo 的 answersPerNode，countryLimits.test.ts 对照）。面板文案按它写出
// "不再外呼"的上界：超过这个数的地址轮换时会再查，文案不能许诺无条件的"每地址一次"。
export const ANSWERS_PER_NODE = 4;

export function flag(code: string): string {
  if (!/^[A-Z]{2}$/.test(code)) return "";
  return String.fromCodePoint(...[...code].map((c) => 0x1f1e6 + c.charCodeAt(0) - 0x41));
}
