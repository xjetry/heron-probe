import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { useIsMutating, useQueryClient } from "@tanstack/react-query";
import { AdminService, GetSettingsResponseSchema, type Settings } from "../gen/probe/v1/admin_pb";

// SAVE_SETTINGS 是全部 UpdateSettings 表单共用的 mutationKey，做保存互斥。消费者：外观页的外观表单与国家 / 地区查询表单
// （pages/Appearance.tsx）、备份表单（components/BackupSettingsForm.tsx）。三者的 useMutation 都带这个键，并用
// useSettingsSaving 在任一个在途时禁用自己的编辑与提交。新增的设置表单同样要带上它，否则互斥对它不成立。
//
// 为什么互斥：UpdateSettings 整体替换外观五项，查询表单与备份表单要提交就得连带重发外观，发出的是提交那一刻缓存里的
// 已保存值；外观表单此时保存了新外观，两个请求到达 hub 的先后决定结果，后到的把先到的外观覆盖回去。键上有在途的保存时，
// 其余表单都不能提交；各表单的 onSuccess 返回 useAdoptSavedSettings 的 promise，回显先写进缓存，在途一直持续到重新拉取
// 结束，下一个表单提交时读到的已是新值。
export const SAVE_SETTINGS = ["probe.v1.AdminService/UpdateSettings"] as const;

export const useSettingsSaving = (): boolean => useIsMutating({ mutationKey: SAVE_SETTINGS }) > 0;

// 设置表单保存成功后都经它：先把 hub 的回显写进 getSettings 的缓存，再失效。回显就是库里的已保存值（外观是清洗后写入的
// 值，总闸、国家查询两项与备份由 SaveSettings 在同一个写事务里读回），缓存据此更新，不依赖刷新成功。只失效时，刷新一旦
// 失败，缓存就停在保存前的值：查询表单与备份表单按缓存整体替换外观，会把刚保存的外观改回去；外观表单没动过的总闸开关
// 显示缓存值，也停在保存前；重新进入页面时查询表单与备份表单显示的也是保存前的值。写与失效用同一个键，作用在同一组查询上。
export function useAdoptSavedSettings() {
  const qc = useQueryClient();
  return (settings: Settings | undefined) => {
    const queryKey = createConnectQueryKey({ schema: AdminService.method.getSettings, cardinality: "finite" });
    qc.setQueriesData({ queryKey }, () => create(GetSettingsResponseSchema, { settings }));
    return qc.invalidateQueries({ queryKey });
  };
}
