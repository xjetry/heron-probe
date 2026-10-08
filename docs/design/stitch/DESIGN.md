---
name: Heron Monitor
colors:
  primary: '#5B9BF8'
  on-primary: '#0B0D12'
  primary-light-mode: '#2563EB'
  background: '#0B0D12'
  surface: '#12151C'
  surface-raised: '#181C25'
  surface-hover: '#1C212C'
  outline: '#232836'
  outline-strong: '#2F3646'
  on-surface: '#E6E8EE'
  on-surface-variant: '#9AA3B5'
  on-surface-muted: '#5F6878'
  light-background: '#F6F7F9'
  light-surface: '#FFFFFF'
  light-outline: '#E4E7EC'
  light-on-surface: '#111827'
  light-on-surface-variant: '#4B5563'
  light-on-surface-muted: '#9CA3AF'
  status-online: '#10B981'
  status-attention: '#F59E0B'
  status-offline: '#F43F5E'
  status-never: '#6B7280'
  status-maintenance: '#A78BFA'
typography:
  headline-lg:
    fontFamily: Inter
    fontSize: 22px
    fontWeight: '600'
    lineHeight: 28px
  headline-md:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: '600'
    lineHeight: 22px
  body-public:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  body-admin:
    fontFamily: Inter
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 18px
  label:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: '500'
    lineHeight: 16px
  mono-md:
    fontFamily: JetBrains Mono
    fontSize: 13px
    fontWeight: '500'
    lineHeight: 18px
  mono-sm:
    fontFamily: JetBrains Mono
    fontSize: 12px
    fontWeight: '400'
    lineHeight: 16px
rounded:
  sm: 4px
  DEFAULT: 6px
  full: 9999px
spacing:
  xs: 4px
  sm: 8px
  md: 12px
  lg: 16px
  xl: 24px
---

## Brand & Style

Heron 是自托管的服务器监控探针：agent 采集主机指标上报，hub 存储、展示并提供查询。界面有两块，共用这一套设计系统：公开页（访客看一组服务器的实时状态与历史，不登录）与管理面板（唯一的管理员管理节点、探测、告警、通知与系统设置）。所有界面文字使用简体中文。

深色优先的运维台风格，同时提供等价的浅色模式（默认跟随系统）。克制、精确、可快速扫读：近黑中性底色，1px 细边框分层，不用渐变、网格背景、扫描线、霓虹光效或装饰性插画。浅色模式是白与极浅灰底、留白更多、卡片用极淡阴影。

最重要的规则：只展示 Heron 真实存在的数据与功能，不编造指标、状态原因、导航入口、页脚或文案。详见 Data Contract 一节。

## Colors

- 主色默认「鹭蓝」（深色 #5B9BF8，浅色 #2563EB），只用于链接、选中、焦点环、主按钮。站点管理员可以替换主色，所以任何含义都不能只靠主色表达。
- 状态色固定，不随主色变化：在线 #10B981；需关注 #F59E0B（到期 ≤30 天、agent 版本落后、探测失败）；离线 #F43F5E；从未上报 中性灰 #6B7280；维护中 #A78BFA。
- 状态一律用「色点 + 文字」表达，不只靠颜色。
- 进度条按阈值着色：<70% 中性/主色，70–90% 琥珀，>90% 玫红。
- 深色：画布 #0B0D12，卡片 #12151C，抬升层 #181C25，悬停 #1C212C，边框 #232836 / 强边框 #2F3646，文字 #E6E8EE / 次要 #9AA3B5 / 弱 #5F6878。
- 浅色：画布 #F6F7F9，卡片 #FFFFFF，边框 #E4E7EC，文字 #111827 / 次要 #4B5563 / 弱 #9CA3AF。

## Typography

- 界面文字：Inter + 系统中文字体（PingFang SC / Noto Sans SC）。中文标签、标题、按钮、说明一律不用等宽字体。
- JetBrains Mono 只用于数字、单位、时间、IP 地址、版本号，并开启 tabular-nums。
- 管理端正文 13px，公开页正文 14px。

## Layout & Spacing

- 管理端：左侧固定侧栏 232px（窄屏收为抽屉），顶栏 48px 含面包屑、公开页入口、GitHub 仓库图标、明暗切换、登出。
- 表格：表头 32px，行 36px，数值右对齐，行操作收进 ⋯ 菜单，不在行内堆多个按钮。
- 输入框与按钮高 32px。
- 新建与编辑在右侧抽屉（宽 480px）里完成，不把表单铺在列表页顶部。
- 公开总览默认视图是卡片网格（每卡约 170px 高）；第二视图是按地区分组的状态墙（方块约 128×56px）加右侧固定详情面板。访客选过的视图按浏览器记住。

## Elevation & Depth

层级靠 1px 边框与表面明度分层，不用重阴影。只有弹层与抽屉带阴影 0 4px 16px rgba(0,0,0,0.45)。

## Shapes

控件 4px 圆角，卡片 6px；全圆角只给状态点与胶囊。

## Components

- 状态徽章：色点 + 文字，背景为状态色 10% 透明度、1px 状态色 40% 边框。
- 国家徽章：国旗 emoji + 两位国家码。
- 迷你进度条：标签 + 百分比 + 细条，按阈值着色。
- 迷你趋势线：仅在卡片视图与详情面板中用于网络速率。
- 时序图：1px 细网格；没有方块图例；图例在没有悬停时显示最新值，悬停时显示该时刻的值；均值实线，峰值同色浅线；窗口内没有数据时显示一行占位说明。时间窗口 1h / 6h / 24h / 7d / 30d。
- 口径说明：一个 ⓘ 图标，悬停或点击展开，不常驻占据页面。
- 空状态：一句说明 + 一个主操作。

## Data Contract

公开页每个节点只有这些字段：名称、国家/地区码、标签、公开备注（站长写的一行说明，可为空）、在线与否、维护中与否、最后上报时间（从未上报时为空）、系统（发行版）、架构、虚拟化类型、CPU 型号与核数、运行时长、CPU%、负载 1/5/15、内存已用/总量、交换已用/总量、磁盘已用/总量、网络实时下行/上行速率、本计费周期下行/上行流量、磁盘读写速率、TCP/UDP 连接数、进程数、CPU steal / iowait、按核负载、费用（金额 + 币种 + 周期）、到期日与剩余天数。另有历史时序图与探测（ICMP/TCP/HTTP/DNS）延迟对比。

公开页禁止出现：IP 地址、主机名、内核版本、agent 版本、任何「可用率 / 在线率 / SLA / 99.9%」百分比、离线原因（如宿主宕机、网络不可达、路由黑洞）、同步延迟或 RTT 徽章、隐私模式说明、拓扑图、事件日志、告警历史、页脚版本号与 API 路径、手动刷新按钮（数据每 2 秒自动刷新）、通知铃铛。公开页顶栏只有：logo、站点标题（站长自定义，默认「服务器状态」）、GitHub 仓库图标（只有图标，没有文字）、明暗切换、「登录」入口。

节点状态只有四种：在线、离线（附「最后上报 N 前」）、从未上报、维护中。离线就只说离线与最后上报时间，不推测原因。地区分组可以显示「18 / 41 在线」这种当前计数，但不要写成「在线率 43.9%」。

管理面板额外可见：IPv4/IPv6 出口地址及其探测状态（正常/探测失败/不支持/等待上报）、主机名、内核、agent 版本及是否低于 hub 绑定版本、执行环境、上报覆盖率（必须注明它不是在线率）、私有备注、是否公开、排序、告警规则（类型：离线、探测、到期、资源、证书到期）、告警事件（事件是状态转换：触发 / 恢复，带观测值与投递记录，可标注是否处于维护静默内；另有登录成功、登录锁定、备份失败/恢复等系统事件）、维护静默、通知渠道、API token、注册窗口、在线更新、存储与备份、外观与主题、安全（会话、TOTP、Passkey）。

标签与地区由数据动态产生，数量不定（可能 0 个，也可能几十个），站长随时增删。筛选器不得把它们画成固定的一排胶囊：地区用多选下拉，选项是当前节点实际出现的国家（加「未知」），每项带节点数；标签用可搜索的多选下拉，已选标签以可移除的胶囊排在下拉旁，放不下时折叠为「+N」。状态墙的分组（按地区或按标签切换）同样随数据生成，组数不定；按标签分组时一个节点出现在它的每个标签组里，无标签的节点在最后的「无标签」组。

不写评价性结论或解读文字（如「状态平稳」「无宿主竞争」「存储通畅」「路由质量良好」），也不写数据里没有的规格细节（如 KVM、NVMe、「东京 → 上海」这类线路方向、探测间隔、发包数、采样点数）。维护中只显示「维护中」，不附原因。

页面标题只用站点标题「服务器状态」，不加 Heron 或 Heron Monitor 前缀。节点名、中文、标签一律 Inter；等宽字体只给数字、单位、时间。

不要给任何东西加没有数据来源的状态徽章或判断（如探测卡上的「正常」「健康」「良好」）；状态只有节点的四种。公开页顶栏里除 GitHub 仓库图标外不放任何导航链接，即便换个名字（如「网络监控」「事件历史」）也不行。
