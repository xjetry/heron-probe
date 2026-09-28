import { useIsMutating } from "@tanstack/react-query";

// SAVE_SETTINGS 是全部 UpdateSettings 表单共用的 mutationKey，做保存互斥。消费者：外观页的外观表单（pages/Appearance.tsx）
// 与通知页的登录通知表单（pages/Channels.tsx）。两者的 useMutation 都带这个键，并用 useSettingsSaving 在任一个在途时禁用
// 自己的编辑与提交。新增的设置表单同样要带上它，否则互斥对它不成立。
//
// 为什么互斥：一个表单若连带重发它不编辑的组（外观五项整体替换，要提交就得整组重发），它发出的是提交那一刻读到的已保存值；
// 另一个表单此时改了那一组，两个请求到达 hub 的先后决定结果，后到的把先到的改动覆盖回去。键上有在途的保存时，其余表单
// 都不能提交；各表单的 onSuccess 返回设置的刷新 promise，在途一直持续到设置重新读完，下一个表单提交时读到的已是新值。
// 外观表单与登录通知表单各只提交自己那一组（hub 对缺席的组不改），彼此本不会覆盖；键共用是全部设置表单的约定，重发别组的
// 表单加入时不必再逐对分析。
export const SAVE_SETTINGS = ["probe.v1.AdminService/UpdateSettings"] as const;

export const useSettingsSaving = (): boolean => useIsMutating({ mutationKey: SAVE_SETTINGS }) > 0;
