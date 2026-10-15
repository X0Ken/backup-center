# ZFS 备份服务一体机 — 技术设计方案


## 一、方案定位

本方案为一套软硬一体化的备份服务平台，采用单机部署形态，以 ZFS 作为存储底座，对外提供 iSCSI 和 NFS 访问接口，通过定时快照实现快速备份与恢复。平台包含 Web 管理界面，用户可通过浏览器完成仓库管理、快照策略配置、恢复操作等全部日常运维工作。

一体机形态的核心设计原则：

- **单机自治**：所有组件（ZFS 存储层、协议服务层、管理面、Web UI）运行在同一台物理服务器上，无需外部依赖
- **开箱即用**：预装操作系统、ZFS、协议服务和平台软件，上电后通过 Web 界面完成初始配置即可投入使用
- **ZFS 深度集成**：不做 ZFS 命令的简单封装，而是将 ZFS 的快照、克隆、send/receive、压缩、校验等能力充分融入平台的产品功能中


## 二、硬件配置建议

### 2.1 推荐配置（面向 50-100TB 可用容量场景）

| 组件 | 规格 | 说明 |
|------|------|------|
| CPU | 8-16 核 x86_64（Intel Xeon Silver / AMD EPYC 4004 级别） | ZFS 的压缩、校验和、加密为 CPU 密集型操作 |
| 内存 | 64-128 GB ECC DDR5 | ZFS ARC 缓存对读性能影响显著。不启用去重时，16 GB 是工作下限，32 GB 适合多用户场景。备份场景中快照元数据较多，建议 64 GB 起步 |
| 系统盘 | 2 × 240 GB 企业级 SSD，硬件 RAID1 | 安装操作系统和平台软件，不存放备份数据 |
| 数据盘 | 8-12 个 3.5" SATA/SAS 盘位，推荐 7200 RPM 企业级 HDD | 建议 RAIDZ2 或 Mirror 配置，兼顾容量与冗余 |
| HBA | LSI/Broadcom 9400 系列 SAS HBA | ZFS 需要直通磁盘访问，不能使用硬件 RAID 卡 |
| 网络 | 2 × 10GbE 或 2 × 25GbE（建议双口绑定） | iSCSI/NFS 流量与管理流量建议分网口或 VLAN 隔离 |
| 电源 | 冗余电源 | 一体机形态建议配置 |
| 机箱 | 4U 机架式，8-12 盘位热插拔 | |

### 2.2 ZFS 池规划建议

```
pool: backup-pool
  vdev: raidz2-0  (6 × 16TB HDD)  → 约 64TB 可用
  vdev: raidz2-1  (6 × 16TB HDD)  → 约 64TB 可用
  总计: ~128TB 原始容量，~100TB 可用（扣除校验和预留）

特殊 vdev:
  SLOG: 2 × 200GB 企业级 SSD (Mirror)  — 加速同步写入
  L2ARC: 1 × 1TB NVMe SSD (可选)       — 加速随机读
```

SLOG（Separate Intent Log）对于 iSCSI 场景尤为重要——iSCSI 的同步写入延迟直接影响源服务器的 I/O 性能。L2ARC 在备份数据的读回场景中可提升恢复速度。


## 三、系统架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                     ZFS 备份服务一体机                                │
│                                                                      │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │                    Web 管理界面 (浏览器访问)                     │  │
│  │   React SPA → Nginx 反向代理 → 管理 API (:8080)                │  │
│  └────────────────────────────┬───────────────────────────────────┘  │
│                               │                                      │
│  ┌────────────────────────────▼───────────────────────────────────┐  │
│  │                平台管理服务 (Go 单体应用)                        │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐         │  │
│  │  │ 仓库管理  │ │ 快照调度  │ │ 策略引擎  │ │ 恢复引擎  │         │  │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘         │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐         │  │
│  │  │ 协议管理  │ │ 监控告警  │ │ 用户权限  │ │ 操作审计  │         │  │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘         │  │
│  │                                                               │  │
│  │  SQLite (元数据)  ·  robfig/cron (调度)  ·  JWT (认证)       │  │
│  └────────────────────────────┬───────────────────────────────────┘  │
│                               │ ZFS CLI 调用                          │
│  ┌────────────────────────────▼───────────────────────────────────┐  │
│  │                    ZFS 存储层                                   │  │
│  │                                                                │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐          │  │
│  │  │ 仓库: MySQL  │  │ 仓库: File   │  │ 仓库: 通用   │          │  │
│  │  │ Dataset      │  │ Dataset      │  │ Dataset      │          │  │
│  │  │ @snap-0200   │  │ @snap-0600   │  │ @snap-1200   │          │  │
│  │  │ @snap-0400   │  │ @snap-1200   │  │ @snap-1800   │          │  │
│  │  └──────┬──────┘  └──────┬───────┘  └──────┬───────┘          │  │
│  │         │                 │                  │                  │  │
│  │  ┌──────▼─────────────────▼──────────────────▼───────┐        │  │
│  │  │         协议服务层                                 │        │  │
│  │  │  NFS Server (nfs-kernel-server)                   │        │  │
│  │  │  iSCSI Target (targetcli / LIO)                   │        │  │
│  │  └───────────────────────────────────────────────────┘        │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
          │                          │
          ▼                          ▼
   ┌────────────┐            ┌────────────┐
   │ 源服务器 A  │            │ 源服务器 B  │
   │ mount NFS  │            │ iSCSI LUN  │
   └────────────┘            └────────────┘
```


## 四、功能清单

以下为平台完整功能矩阵，按模块划分。

### 4.1 仓库管理

| 功能项 | 说明 |
|--------|------|
| 创建仓库 | 在指定池上创建 ZFS Dataset，可配置 recordsize、compression、quota 等属性 |
| 删除仓库 | 支持安全删除（需二次确认，检查快照和克隆依赖） |
| 仓库列表 | 展示所有仓库的名称、容量、已用空间、快照数量、协议状态 |
| 属性编辑 | 修改 recordsize、compression、sync、atime 等 ZFS 属性 |
| 容量配额 | 设置仓库级 quota 和 reservation |
| 加密仓库 | 创建加密 Dataset，管理密钥加载/卸载 |

### 4.2 快照管理

| 功能项 | 说明 |
|--------|------|
| 手动快照 | 一键为指定仓库创建快照，命名自动带时间戳 |
| 定时快照 | 通过 Cron 表达式配置定时任务，支持分钟/小时/天/周/月级别 |
| 递归快照 | 对仓库及其子数据集执行 `zfs snapshot -r` |
| 快照浏览 | 以时间线或列表形式展示快照，显示创建时间和占用空间 |
| 快照删除 | 单个或批量删除，检查克隆依赖后执行 |
| 快照保留策略 | 分层保留：小时级/天级/周级/月级/年级，自动清理过期快照 |
| 快照克隆 | 从快照创建可写克隆，用于快速恢复或提供服务 |
| 快照回滚 | 将仓库回滚到指定快照状态（丢弃之后的变更） |
| 快照对比 | 显示两个快照之间的空间差异 |
| 快照挂载 | 将快照通过 `.zfs/snapshot/` 路径暴露为只读目录 |

### 4.3 协议管理

| 功能项 | 说明 |
|--------|------|
| NFS 导出 | 为仓库配置 NFS 导出，设置访问控制（IP/网段、读写权限） |
| NFS 客户端管理 | 查看当前挂载的 NFS 客户端列表 |
| iSCSI Target 创建 | 将 zvol 或克隆卷配置为 iSCSI LUN |
| iSCSI ACL | 配置 initiator 访问控制（IQN 白名单） |
| iSCSI 状态 | 查看当前 iSCSI 会话、连接状态和吞吐量 |
| 协议切换 | 同一仓库的 NFS/iSCSI 导出配置的启用/禁用 |

### 4.4 恢复管理

| 功能项 | 说明 |
|--------|------|
| 克隆恢复 | 从快照克隆为新的可写数据集，可选择是否通过 NFS/iSCSI 立即导出 |
| 回滚恢复 | 将仓库回滚到历史快照，适用于源服务仍在运行的场景 |
| 文件级恢复 | 通过快照只读挂载路径直接复制单个文件 |
| 恢复预览 | 在恢复前浏览快照内容，确认目标版本 |
| 恢复记录 | 记录每次恢复操作的详细信息（操作人、时间、源快照、目标位置） |

### 4.5 复制与异地备份

| 功能项 | 说明 |
|--------|------|
| 复制任务创建 | 配置源仓库到远程 ZFS 主机的 send/receive 任务 |
| 全量/增量复制 | 首次全量，后续自动增量 |
| 复制调度 | 与快照调度联动，快照完成后自动触发复制 |
| 复制状态 | 显示上次复制时间、传输量、耗时、状态 |
| 断点续传 | 利用 ZFS resume token 支持中断后恢复传输 |

### 4.6 监控与告警

| 功能项 | 说明 |
|--------|------|
| 池健康 | ZFS pool 状态、scrub 进度、错误计数 |
| 磁盘 SMART | 各磁盘的 SMART 健康状态和温度 |
| 容量监控 | 池使用率、仓库容量、快照空间占用 |
| 性能指标 | 读写 IOPS、吞吐量、ARC 命中率、快照创建耗时 |
| 告警规则 | 自定义阈值（容量 >80%、快照失败、复制延迟等） |
| 告警通知 | 邮件、Webhook（钉钉/企业微信/Slack） |
| 操作日志 | 所有管理操作的审计日志 |

### 4.7 系统管理

| 功能项 | 说明 |
|--------|------|
| 用户管理 | 支持多用户，角色分为管理员和操作员 |
| 认证 | 本地账号密码 + 可选的 LDAP/AD 集成 |
| 系统初始化 | 首次启动向导：设置管理员密码、创建 ZFS 池、网络配置 |
| 系统信息 | 查看 OS 版本、ZFS 版本、运行时间、服务状态 |
| 网络配置 | 管理网口 IP、绑定、VLAN 配置 |
| 固件/软件更新 | 平台软件和系统组件的版本管理与升级 |
| 备份配置导出 | 导出平台配置为 JSON 文件，用于灾难恢复 |
| SNMP 集成 | 支持通过 SNMP 上报监控指标 |

### 4.8 备份工具集成（源服务器侧辅助）

平台本身不负责数据拉取（由源服务器上的备份工具完成），但提供辅助能力：

| 功能项 | 说明 |
|--------|------|
| 备份脚本生成器 | 根据仓库类型（MySQL/文件/通用），生成推荐的 rsync/XtraBackup 脚本模板 |
| 客户端配置向导 | 分步指导源服务器完成 NFS 挂载或 iSCSI 连接 |
| 连接测试 | 验证源服务器到仓库的网络连通性和挂载状态 |


## 五、Web 管理界面设计

### 5.1 界面布局

采用经典的左侧导航 + 右侧内容区布局，参考企业级存储管理平台的交互模式。

```
┌─────────────────────────────────────────────────────────────┐
│  🔒 ZFS Backup Appliance    [搜索]    [通知] [admin ▼]     │
├──────────┬──────────────────────────────────────────────────┤
│          │                                                  │
│  📊 概览  │              内容区域                             │
│          │                                                  │
│  📦 仓库  │  ┌─────────────────────────────────────────┐    │
│          │  │  存储池状态卡片                          │    │
│  📸 快照  │  │  backup-pool: 健康  |  使用率: 62%       │    │
│          │  │  容量: 100TB / 161TB                     │    │
│  🔗 协议  │  └─────────────────────────────────────────┘    │
│          │                                                  │
│  ♻️ 恢复  │  ┌──────────────┐  ┌──────────────┐            │
│          │  │ 仓库总数: 12  │  │ 快照总数: 847│            │
│  🔄 复制  │  └──────────────┘  └──────────────┘            │
│          │                                                  │
│  📈 监控  │  ┌─────────────────────────────────────────┐    │
│          │  │  最近快照活动（时间线/列表）              │    │
│  ⚙️ 系统  │  │  mysql-repo  @2026-10-15-0200  ✅ 2分钟前│    │
│          │  │  files-repo  @2026-10-15-0000  ✅ 2小时前│    │
│          │  └─────────────────────────────────────────┘    │
│          │                                                  │
└──────────┴──────────────────────────────────────────────────┘
```

### 5.2 核心页面说明

#### 概览页（Dashboard）

作为管理员的“驾驶舱”，提供系统运行状态的全局视图：

- **存储池健康卡片**：ZFS pool 状态（ONLINE/DEGRADED/FAULTED）、使用率进度条、容量分布
- **快速统计**：仓库数量、快照总数、活跃协议连接数、今日快照创建次数
- **最近活动时间线**：最新的快照创建、复制完成、告警事件
- **容量趋势图**：近 30 天的池使用率变化折线图（ECharts 渲染）

#### 仓库管理页

左侧为仓库列表（支持搜索和排序），右侧为选中仓库的详情面板：

- **基本信息**：名称、路径、创建时间、已用/可用空间、压缩比
- **快照列表**：按时间倒序排列，显示快照名、创建时间、占用空间、保留标记
- **协议配置**：当前 NFS 导出和 iSCSI Target 的状态，一键启用/禁用
- **操作按钮**：创建快照（立即）、配置定时任务、克隆、回滚、删除
- **属性编辑**：以表单形式编辑 ZFS 属性（recordsize、compression 等），带实时校验

#### 快照管理页

以统一视图展示所有仓库的快照，支持按仓库、时间范围筛选：

- **快照策略配置面板**：以表单形式配置分层保留策略（小时/天/周/月/年的保留数量），提供预览效果
- **快照详情抽屉**：点击快照展开详情，显示父快照、占用空间、克隆依赖，提供“克隆为仓库”“回滚到此快照”“通过 NFS 导出”等操作

#### 恢复操作页

以向导式流程引导用户完成恢复：

```
步骤 1: 选择源仓库 → 步骤 2: 选择目标快照 → 步骤 3: 选择恢复方式
                                                    ├─ 克隆为新仓库（推荐）
                                                    ├─ 回滚到快照
                                                    └─ 导出为 NFS/iSCSI 共享
步骤 4: 确认配置并执行 → 步骤 5: 查看执行结果
```

#### 监控页

- **池状态面板**：pool 拓扑图（vdev 结构可视化）、scrub 进度、错误日志
- **磁盘健康**：各磁盘的 SMART 状态、温度、通电时间
- **性能图表**：读写吞吐量、IOPS、ARC 命中率（时间范围可切换：1h/24h/7d/30d）
- **告警列表**：当前活跃告警和已确认告警

### 5.3 交互设计要点

- **危险操作二次确认**：删除仓库、回滚快照、销毁克隆等操作弹出确认对话框，要求输入仓库名称确认
- **操作反馈**：所有异步操作（创建快照、克隆、复制）显示进度条和状态提示
- **实时更新**：监控数据通过 WebSocket 或 SSE 推送，无需手动刷新
- **响应式布局**：适配 1280px 以上的桌面浏览器，暂不考虑移动端


## 六、技术栈选型

### 6.1 整体技术栈

| 层次 | 技术 | 选型理由 |
|------|------|----------|
| **操作系统** | Ubuntu 24.04 LTS (Server) | LTS 长期支持，OpenZFS 2.2/2.3 原生支持，社区活跃 |
| **存储层** | OpenZFS 2.3+ | 快照、克隆、send/receive、压缩、校验和、加密 |
| **协议服务** | NFS (nfs-kernel-server) + iSCSI (targetcli/LIO) | 均为内核态实现，性能好，与 ZFS 集成成熟 |
| **后端语言** | Go 1.22+ | 单二进制部署、goroutine 并发、ZFS 命令封装方便 |
| **后端框架** | Gin | 轻量、成熟、中间件生态丰富 |
| **ORM** | GORM + SQLite | 单机场景无需独立数据库，SQLite 零运维；GORM 提供模型管理和迁移 |
| **任务调度** | robfig/cron v3 | Go 生态中最成熟的 Cron 库，支持秒级精度 |
| **前端框架** | React 18 + TypeScript | 组件化、类型安全、生态成熟 |
| **UI 组件库** | ArcoDesign | 字节跳动开源，企业级后台组件丰富，中文文档完善 |
| **前端构建** | Vite | 开发热更新快，生产构建优化好 |
| **状态管理** | Zustand | 轻量、TypeScript 友好，适合中小型后台应用 |
| **图表** | ECharts | 功能全面，监控图表场景首选 |
| **Web 服务器** | Nginx | 反向代理、静态资源托管、SSL 终止 |
| **进程管理** | systemd | 系统级服务管理，开机自启，日志集成 journald |

这一技术栈组合参考了多个开源备份管理平台的实际选型：BackupX 采用 Go · Gin · GORM · SQLite · robfig/cron 后端和 React 18 · TypeScript · ArcoDesign · Vite 前端，在单机部署场景中验证了这套组合的可行性。

### 6.2 后端架构

```
cmd/
  backupd/              # 主程序入口
internal/
  api/                  # HTTP handler + 路由
    repository.go       # 仓库 CRUD API
    snapshot.go         # 快照管理 API
    protocol.go         # NFS/iSCSI 配置 API
    restore.go          # 恢复操作 API
    monitor.go          # 监控数据 API
    system.go           # 系统管理 API
  service/              # 业务逻辑层
    repository_svc.go   # 仓库业务逻辑
    snapshot_svc.go     # 快照调度与执行
    retention_svc.go    # 保留策略引擎
    protocol_svc.go     # 协议配置管理
    restore_svc.go      # 恢复操作编排
    replication_svc.go  # 复制任务管理
  zfs/                  # ZFS 命令封装层
    pool.go             # 池操作
    dataset.go          # Dataset 操作
    snapshot.go         # 快照操作
    send.go             # send/receive 封装
  protocol/             # 协议服务封装
    nfs.go              # NFS 导出管理
    iscsi.go            # iSCSI Target 管理
  model/                # GORM 数据模型
    repository.go
    snapshot.go
    policy.go
    user.go
    audit.go
  scheduler/            # 任务调度
    cron.go             # Cron 调度器
    jobs.go             # 定时任务定义
  monitor/              # 监控采集
    collector.go        # 指标采集
    alert.go            # 告警引擎
```

**ZFS 命令封装示例**：

```go
// zfs/snapshot.go
func CreateSnapshot(dataset string, snapName string) error {
    fullName := fmt.Sprintf("%s@%s", dataset, snapName)
    cmd := exec.Command("zfs", "snapshot", fullName)
    output, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("创建快照失败: %s, %s", fullName, string(output))
    }
    return nil
}

func ListSnapshots(dataset string) ([]SnapshotInfo, error) {
    cmd := exec.Command("zfs", "list", "-t", "snapshot",
        "-H", "-p", "-o", "name,creation,used,refer",
        "-s", "creation", "-r", dataset)
    // ... 解析输出
}
```

### 6.3 数据模型

```sql
-- 仓库表
CREATE TABLE repositories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    dataset     TEXT NOT NULL UNIQUE,   -- ZFS 数据集路径
    pool        TEXT NOT NULL,
    recordsize  TEXT DEFAULT '128K',
    compression TEXT DEFAULT 'lz4',
    quota       TEXT,
    protocol    TEXT,                    -- 'nfs' | 'iscsi' | 'both' | 'none'
    status      TEXT DEFAULT 'active',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 快照策略表
CREATE TABLE snapshot_policies (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repository_id   INTEGER REFERENCES repositories(id),
    cron_expr       TEXT NOT NULL,       -- Cron 表达式
    keep_hourly     INTEGER DEFAULT 24,
    keep_daily      INTEGER DEFAULT 30,
    keep_weekly     INTEGER DEFAULT 12,
    keep_monthly    INTEGER DEFAULT 12,
    enabled         INTEGER DEFAULT 1,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 快照记录表（用于快速查询，实际状态以 ZFS 为准）
CREATE TABLE snapshot_records (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repository_id   INTEGER REFERENCES repositories(id),
    snapshot_name   TEXT NOT NULL,       -- 如 2026-10-15-0200
    zfs_snapshot    TEXT NOT NULL,       -- 完整路径 pool/dataset@snapname
    created_at      DATETIME,
    size_bytes      INTEGER,
    retention_tag   TEXT,                -- 'hourly' | 'daily' | 'weekly' | ...
    UNIQUE(zfs_snapshot)
);

-- 复制任务表
CREATE TABLE replication_jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repository_id   INTEGER REFERENCES repositories(id),
    target_host     TEXT NOT NULL,
    target_dataset  TEXT NOT NULL,
    last_snapshot   TEXT,
    last_run_at     DATETIME,
    status          TEXT DEFAULT 'pending',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 操作审计表
CREATE TABLE audit_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER,
    action      TEXT NOT NULL,
    target      TEXT,
    detail      TEXT,
    ip_address  TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 6.4 前端页面结构

```
web/src/
├── components/
│   ├── layout/
│   │   ├── AppLayout.tsx        # 左侧导航 + 顶栏布局
│   │   └── Sidebar.tsx
│   ├── common/
│   │   ├── ConfirmDialog.tsx    # 危险操作确认框
│   │   ├── StatusBadge.tsx      # 状态标签
│   │   ├── CapacityBar.tsx      # 容量进度条
│   │   └── TimeLine.tsx         # 操作时间线
│   └── charts/
│       ├── UsageTrend.tsx       # 容量趋势图
│       └── PerformanceChart.tsx # 性能监控图
├── pages/
│   ├── Dashboard.tsx            # 概览页
│   ├── Repositories/
│   │   ├── RepositoryList.tsx
│   │   └── RepositoryDetail.tsx
│   ├── Snapshots/
│   │   ├── SnapshotList.tsx
│   │   └── PolicyEditor.tsx     # 保留策略配置
│   ├── Protocols/
│   │   └── ProtocolManager.tsx  # NFS/iSCSI 配置
│   ├── Restore/
│   │   └── RestoreWizard.tsx    # 恢复向导
│   ├── Monitoring/
│   │   └── MonitorPanel.tsx
│   └── System/
│       ├── UserManagement.tsx
│       └── SystemInfo.tsx
├── services/                    # API 调用封装
│   ├── api.ts                   # Axios 实例 + 拦截器
│   ├── repository.ts
│   ├── snapshot.ts
│   └── monitor.ts
├── stores/                      # Zustand 状态
│   ├── authStore.ts
│   └── repoStore.ts
└── types/                       # TypeScript 类型定义
    ├── repository.ts
    └── snapshot.ts
```


## 七、ZFS 核心操作流程

### 7.1 仓库创建

```bash
# 平台执行的操作序列
zfs create backup-pool/mysql-repo
zfs set recordsize=128K backup-pool/mysql-repo
zfs set compression=lz4 backup-pool/mysql-repo
zfs set quota=2T backup-pool/mysql-repo

# NFS 导出（平台通过修改 /etc/exports 并执行 exportfs 实现）
echo "/backup-pool/mysql-repo 203.0.113.0/24(rw,sync,no_subtree_check,no_root_squash)" >> /etc/exports
exportfs -ra
```

### 7.2 定时快照

快照创建是 ZFS 的原子操作，无论数据量大小都在毫秒级完成。Go 调度器通过 robfig/cron 触发，调用封装好的 `zfs snapshot` 命令：

```go
// scheduler/jobs.go
func (s *Scheduler) RegisterSnapshotJob(repoID int, cronExpr string) {
    s.cron.AddFunc(cronExpr, func() {
        repo := getRepository(repoID)
        snapName := time.Now().Format("2006-01-02-1504")
        if err := zfs.CreateSnapshot(repo.Dataset, snapName); err != nil {
            s.alert.Notify(fmt.Sprintf("仓库 %s 快照失败: %v", repo.Name, err))
            return
        }
        // 记录快照元数据
        recordSnapshot(repoID, snapName)
        // 执行保留策略清理
        s.retentionSvc.Prune(repoID)
    })
}
```

### 7.3 保留策略实现

参考 Son-Father-Grandfather（SFG）轮转方案，实现分层保留：

```go
// service/retention_svc.go
func (s *RetentionService) Prune(repoID int) error {
    policy := getPolicy(repoID)
    snapshots := listSnapshots(repoID)

    // 按时间分类
    hourly := filterByInterval(snapshots, "hour")
    daily := filterByInterval(snapshots, "day")
    weekly := filterByInterval(snapshots, "week")
    monthly := filterByInterval(snapshots, "month")

    // 构建保留集合
    keepSet := make(map[string]bool)
    markKeep(keepSet, hourly, policy.KeepHourly)
    markKeep(keepSet, daily, policy.KeepDaily)
    markKeep(keepSet, weekly, policy.KeepWeekly)
    markKeep(keepSet, monthly, policy.KeepMonthly)

    // 删除不在保留集合中的快照
    for _, snap := range snapshots {
        if !keepSet[snap.Name] {
            if err := zfs.DestroySnapshot(snap.FullName); err != nil {
                log.Printf("删除快照失败: %s, %v", snap.FullName, err)
            }
        }
    }
    return nil
}
```

### 7.4 快速恢复

```bash
# 从快照克隆（平台通过 API 触发）
zfs clone backup-pool/mysql-repo@2026-10-15-0200 \
         backup-pool/mysql-restore-20261015

# 将克隆通过 NFS 导出，供源服务器或备用服务器挂载
exportfs -o rw,no_root_squash *:/backup-pool/mysql-restore-20261015
```

ZFS 克隆是 O(1) 操作，克隆卷与快照共享数据块，仅在被写入时才分配新空间。恢复时间取决于源服务器重新挂载和启动服务的时间，通常在秒级到分钟级。


## 八、ZFS 性能调优建议

### 8.1 池级别

| 调优项 | 建议值 | 说明 |
|--------|--------|------|
| ashift | 12（4K 扇区）或 13（8K） | 创建池时指定，不可更改。现代大容量 HDD 建议 ashift=12 |
| compression | lz4（默认）或 zstd | lz4 速度最快，zstd 压缩率更高但消耗更多 CPU |
| atime | off | 备份场景不需要记录访问时间 |
| xattr | sa | 将扩展属性存储在 inode 中，减少额外 I/O |

### 8.2 Dataset 级别

| 场景 | recordsize | sync | 说明 |
|------|-----------|------|------|
| MySQL/PostgreSQL | 128K | standard | 匹配 InnoDB 页大小 |
| 大文件（视频、归档） | 1M | standard | 减少元数据开销 |
| iSCSI zvol | volblocksize=16K | standard | 匹配典型块设备 I/O |
| 小文件（配置文件） | 32K 或 64K | standard | 减少空间浪费 |

### 8.3 复制性能

使用 `zfs send -c` 发送压缩流，避免在发送端解压、接收端重新压缩，可显著降低 CPU 占用和网络传输量：

```bash
zfs send -c -i pool/repo@snap1 pool/repo@snap2 | \
  mbuffer -m 2G -s 1M | \
  ssh backup-host zfs receive -u tank/repo
```

mbuffer 的缓冲区（`-m`）和块大小（`-s`）调整可平滑网络抖动，建议在大带宽延迟积链路上增大缓冲区。


## 九、部署与初始化流程

### 9.1 一体机出厂状态

- Ubuntu 24.04 Server 已安装，OpenZFS 2.3 已编译安装
- NFS 内核服务和 targetcli 已安装并配置为 systemd 服务
- 平台 Go 二进制和前端静态文件已部署，Nginx 配置就绪
- systemd 服务 `backupd.service` 已注册

### 9.2 首次启动向导

用户首次通过浏览器访问 `https://<一体机IP>` 时，进入初始化向导：

1. **设置管理员密码**：创建初始 admin 账户
2. **网络配置**：确认管理 IP 和业务 IP（如未自动获取）
3. **创建存储池**：向导列出可用磁盘，提供 RAIDZ1/RAIDZ2/Mirror 拓扑选择，自动计算可用容量
4. **创建第一个仓库**：输入仓库名称，选择协议类型（NFS/iSCSI），生成配置
5. **完成**：显示连接信息（NFS 挂载命令 / iSCSI 连接参数），可复制给源服务器使用

### 9.3 日常运维

- **快照策略调整**：在 Web 界面的快照策略页面修改保留数量，无需重启服务
- **容量扩展**：新增磁盘到 pool（`zpool add`），通过 Web 界面触发
- **系统更新**：通过 Web 界面检查更新，自动下载并重启服务
- **配置备份**：定期导出平台配置 JSON，存储在独立位置


## 十、典型使用场景

### 场景：MySQL 文件级备份

```
1. 管理员在 Web 界面创建仓库 "mysql-repo"（NFS 协议）
2. 平台生成 NFS 挂载命令，管理员复制到 MySQL 服务器执行
3. MySQL 服务器运行 XtraBackup 将数据写入 NFS 挂载的仓库目录
4. 平台按 Cron 表达式（如每小时）创建 ZFS 快照
5. 保留策略自动清理：24 个最近小时快照 + 30 个天快照 + 12 个月快照
6. MySQL 故障时：
   a. 在 Web 界面选择最新快照，点击"克隆恢复"
   b. 克隆卷通过 NFS 自动导出
   c. 备用 MySQL 服务器挂载克隆卷并启动服务
   d. 整个恢复过程 < 1 分钟
```

该场景的核心优势在于：ZFS 快照创建不受数据量影响，无论 MySQL 数据目录是 100GB 还是 10TB，快照都在毫秒级完成；而传统的 mysqldump 方式则需要完整遍历数据，耗时与数据量成正比。增量快照的空间占用仅与快照之间的变化数据量相关，大幅节省备份存储空间。
