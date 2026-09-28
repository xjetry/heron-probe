import type { MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import type { BackupSettings, BackupSettingsSchema, Settings, SettingsSchema, UpdateSettingsRequest } from "../gen/probe/v1/admin_pb";
import { ascending } from "../lib/ids";
import type { AdminImpl } from "./harness";

// 状态只存普通的初始化对象：消息对象（带 $typeName、嵌套字段也是消息）与初始化对象混着展开，得到的值两种形态都不是。
export type SettingsInit = Exclude<MessageInitShape<typeof SettingsSchema>, Settings>;
type BackupInit = Exclude<MessageInitShape<typeof BackupSettingsSchema>, BackupSettings>;

// 请求里的 backup 是消息对象；状态只取领域字段拼成普通的初始化对象，不混入消息元数据。语义同 store 的 saveBackup：
// endpoint、bucket、区域（空串存为 auto）、access key、前缀整体替换；secret、四个数值与 notify 缺席即不变；secret 只写
// 不读，回显 hasSecret；渠道排序去重。
function savedBackup(b: BackupSettings, old: MessageInitShape<typeof BackupSettingsSchema> | undefined): BackupInit {
  return {
    endpoint: b.endpoint, bucket: b.bucket, region: b.region || "auto", accessKey: b.accessKey, prefix: b.prefix,
    configIntervalS: b.configIntervalS ?? old?.configIntervalS ?? 300, metricsIntervalS: b.metricsIntervalS ?? old?.metricsIntervalS ?? 86400,
    configKeep: b.configKeep ?? old?.configKeep ?? 48, metricsKeep: b.metricsKeep ?? old?.metricsKeep ?? 14,
    notify: { channelIds: b.notify ? ascending(new Set(b.notify.channelIds)) : [...(old?.notify?.channelIds ?? [])] },
    hasSecret: b.secret !== undefined ? b.secret !== "" : (old?.hasSecret ?? false),
  };
}

// 有状态的 hub 替身，与 store 的 SaveSettings 同语义：外观整体替换（标题去首尾空白，代表 hub 的清洗）；总闸、国家查询
// 两项与 backup 缺席即不变（backup 各项见 savedBackup）；回显保存后的全部设置。set 模拟别处（另一个面板、脚本）改了
// 设置；holdReads 让之后的 GetSettings 挂起到 releaseReads；failReads 让之后的 GetSettings 一直失败（hub 重启、网络中断），
// 保存后的刷新因此拿不到新值；holdSaves 让之后的 UpdateSettings 在记下请求后挂起到 releaseSaves，保存因此一直在途，
// 状态在放行后才改。
export function statefulHub(initial: SettingsInit) {
  let state: SettingsInit = initial;
  const sent: UpdateSettingsRequest[] = [];
  let heldReads: Promise<void> | null = null;
  let releaseReads = () => {};
  let heldSaves: Promise<void> | null = null;
  let releaseSaves = () => {};
  let failing = false;
  const impl: AdminImpl = {
    getSettings: async () => {
      if (failing) throw new ConnectError("hub restarting", Code.Unavailable);
      if (heldReads) await heldReads;
      return { settings: state };
    },
    updateSettings: async (req) => {
      sent.push(req);
      if (heldSaves) await heldSaves;
      const s = req.settings!;
      state = {
        title: s.title.trim(), theme: s.theme, accentColor: s.accentColor, logo: s.logo, customCss: s.customCss,
        publicEnabled: s.publicEnabled ?? state.publicEnabled, geoEnabled: s.geoEnabled ?? state.geoEnabled, geoUrl: s.geoUrl ?? state.geoUrl,
        backup: s.backup ? savedBackup(s.backup, state.backup) : state.backup,
      };
      return { settings: state };
    },
  };
  return {
    impl, sent, state: () => state,
    set: (patch: SettingsInit) => { state = { ...state, ...patch }; },
    holdReads: () => { heldReads = new Promise((r) => { releaseReads = () => { heldReads = null; r(); }; }); },
    releaseReads: () => releaseReads(),
    failReads: () => { failing = true; },
    holdSaves: () => { heldSaves = new Promise((r) => { releaseSaves = () => { heldSaves = null; r(); }; }); },
    releaseSaves: () => releaseSaves(),
  };
}
