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
    label: r.repository.name,
    value: r.repository.id,
  }));
export const kindLabels: Row = {
  "repository.create": "创建仓库",
  "repository.delete": "删除仓库",
  "snapshot.create": "创建快照",
  "snapshot.scheduled": "策略快照",
  "snapshot.delete": "删除快照",
  "restore.clone": "克隆恢复",
  "restore.rollback": "原地回滚",
  "export.apply": "应用协议配置",
  replicate: "远程复制",
  "pool.scrub": "池校验",
};
