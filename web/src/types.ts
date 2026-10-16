import type { Field, Row } from "./components";
export type Tools = {
  rev: number;
  admin: boolean;
  form: (
    title: string,
    fields: Field[],
    submit: (v: Row) => Promise<unknown>,
    warning?: string,
  ) => void;
  perform: (kind: string, payload: Row) => Promise<any>;
  refresh: () => void;
  repos: Row[] | null;
  showText: (v: { title: string; content: string }) => void;
};
export const repoOptions = (rows: Row[] | null) =>
  (rows || []).map((r) => ({
    label: r.repository.display_name || r.repository.name,
    value: r.repository.id,
  }));
export const kindLabels: Row = {
  "peer.invite": "生成平台配对码",
  "peer.paired": "建立平台接收信任",
  "peer.save": "连接异地目标平台",
  "peer.delete": "移除平台信任",
  "replica.create": "创建异地接收仓库",
  "replica.receive": "接收异地备份",
  "repository.create": "创建仓库",
  "repository.delete": "删除仓库",
  "repository.properties": "修改仓库属性",
  "retention.prune": "清理过期快照",
  "key.load": "加载加密密钥",
  "key.unload": "卸载加密密钥",
  "snapshot.create": "创建快照",
  "snapshot.scheduled": "策略快照",
  "snapshot.delete": "删除快照",
  "snapshot.hold": "保护快照",
  "snapshot.release": "解除快照保护",
  "restore.clone": "基于快照创建仓库",
  "restore.rollback": "原地回滚",
  "export.apply": "设置共享访问",
  "sync.run": "数据同步",
  replicate: "远程复制",
  "pool.scrub": "池校验",
};

export const actionLabels: Row = {
  ...kindLabels,
  "repository.properties": "修改仓库属性",
  "retention.prune": "清理过期快照",
  "key.load": "加载加密密钥",
  "key.unload": "卸载加密密钥",
  "auth.login": "登录",
  "auth.login.failed": "登录失败",
  "user.password": "修改密码",
  "sync.save": "保存同步任务",
  "sync.pause": "暂停自动同步",
  "sync.resume": "启用自动同步",
  "sync.stop": "停止本轮同步",
  "sync.delete": "删除同步任务配置",
  "policy.save": "保存快照策略",
  "replication.save": "保存异地复制配置",
  "replication.delete": "删除异地复制配置",
  "ssh.trust": "信任 SSH 主机",
  "user.save": "保存用户",
  "settings.save": "保存系统设置",
  "config.export": "导出配置",
  "config.checkpoint": "生成配置检查点",
  "config.backup": "备份加密配置",
  "alert.ack": "确认告警",
};
export function actionLabel(action: string): string {
  if (actionLabels[action]) return actionLabels[action];
  const parts = action.split(".");
  const status = parts.pop()!;
  const suffix: Row = {
    requested: "已提交",
    succeeded: "已完成",
    failed: "失败",
    interrupted: "已中断",
  };
  const base = parts.join(".");
  return actionLabels[base] && suffix[status]
    ? `${actionLabels[base]} · ${suffix[status]}`
    : action;
}
export function auditDetail(value: string): string {
  const labels: Row = {
    "encrypted bundle": "加密配置备份包",
    loaded: "密钥已加载",
    available: "可用",
    unavailable: "不可用",
    协议配置已应用: "共享访问设置已应用",
  };
  return labels[value] || value || "—";
}
